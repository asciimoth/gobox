package debug

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unicode/utf8"

	"github.com/asciimoth/gobox"
)

// Backend is an unsafe development implementation of gobox.Service.
type Backend struct {
	config     normalizedConfig
	mu         sync.Mutex
	registryMu sync.Mutex
	renameMu   sync.Mutex
	owners     map[string]*ownerSandboxes
	closed     bool
	session    string
}

type ownerRecord struct {
	Owner      gobox.Owner   `json:"owner"`
	LastRename *renameRecord `json:"last_rename,omitempty"`
}

type renameRecord struct {
	OperationID string      `json:"operation_id"`
	Before      gobox.Owner `json:"before"`
	NewName     string      `json:"new_name"`
}

var (
	_ gobox.Service        = (*Backend)(nil)
	_ gobox.OwnerIssuer    = (*Backend)(nil)
	_ gobox.OwnerBinder    = (*Backend)(nil)
	_ gobox.OwnerRevoker   = (*Backend)(nil)
	_ gobox.StatusReporter = (*Backend)(nil)
)

// New creates a debug backend. The returned service executes configured
// programs directly as children of the current process.
func New(config Config) (*Backend, error) {
	normalized, err := normalize(config)
	if err != nil {
		return nil, err
	}
	session, err := randomKey()
	if err != nil {
		if normalized.ephemeralRoot {
			_ = os.RemoveAll(normalized.root)
		}
		return nil, err
	}
	session = session[:12]
	if err = os.MkdirAll(filepath.Join(normalized.root, "temporary", session), 0o700); err != nil {
		if normalized.ephemeralRoot {
			_ = os.RemoveAll(normalized.root)
		}
		return nil, err
	}
	return &Backend{config: normalized, owners: make(map[string]*ownerSandboxes), session: session}, nil
}

func (backend *Backend) ServiceStatus() gobox.ServiceStatus {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	templates := make(map[gobox.TemplateID]gobox.Status, len(backend.config.templates))
	for id := range backend.config.templates {
		templates[id] = debugStatus(gobox.Owner{}, id, 0)
	}
	return gobox.ServiceStatus{
		Mode:              gobox.Off,
		Platform:          runtime.GOOS,
		EffectiveIdentity: currentIdentity(),
		Templates:         templates,
		ActiveOwners:      int64(len(backend.owners)),
	}
}

func (backend *Backend) IssueOwner(ctx context.Context, requested gobox.Owner) (gobox.Owner, error) {
	if err := ctxError(ctx); err != nil {
		return gobox.Owner{}, err
	}
	if err := backend.ensureOpen(); err != nil {
		return gobox.Owner{}, err
	}
	if requested.Key != "" {
		stored, err := backend.readOwner(requested.Key)
		if err != nil {
			return gobox.Owner{}, err
		}
		if !sameOwner(stored, requested) {
			return gobox.Owner{}, gobox.ErrStale
		}
		return stored, nil
	}
	if err := validateOwner(requested, false); err != nil {
		return gobox.Owner{}, err
	}
	key, err := randomKey()
	if err != nil {
		return gobox.Owner{}, err
	}
	requested.Key = key
	if requested.Epoch == 0 {
		requested.Epoch = 1
	}
	if err = backend.writeOwnerRecord(ownerRecord{Owner: requested}, true); err != nil {
		return gobox.Owner{}, err
	}
	return requested, nil
}

func (backend *Backend) IssueAndBindOwner(ctx context.Context, requested gobox.Owner) (gobox.Owner, gobox.OwnerSandboxes, error) {
	owner, err := backend.IssueOwner(ctx, requested)
	if err != nil {
		return gobox.Owner{}, nil, err
	}
	bound, err := backend.BindOwner(ctx, owner)
	return owner, bound, err
}

func (backend *Backend) BindOwner(ctx context.Context, owner gobox.Owner) (gobox.OwnerSandboxes, error) {
	if err := ctxError(ctx); err != nil {
		return nil, err
	}
	stored, err := backend.readOwner(owner.Key)
	if err != nil {
		return nil, err
	}
	if !sameOwner(stored, owner) {
		return nil, gobox.ErrStale
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, gobox.ErrClosed
	}
	if current := backend.owners[owner.Key]; current != nil {
		current.mu.Lock()
		closed := current.closed
		current.mu.Unlock()
		if current.epoch != owner.Epoch || closed {
			return nil, gobox.ErrStale
		}
		return current, nil
	}
	bound := &ownerSandboxes{backend: backend, owner: owner, epoch: owner.Epoch, slots: make(map[string]*sandbox), generations: make(map[string]uint64)}
	backend.owners[owner.Key] = bound
	return bound, nil
}

func (backend *Backend) RebindOwner(ctx context.Context, rename gobox.OwnerRename) (gobox.Owner, error) {
	if err := ctxError(ctx); err != nil {
		return gobox.Owner{}, err
	}
	if err := backend.ensureOpen(); err != nil {
		return gobox.Owner{}, err
	}
	if rename.OperationID == "" || len(rename.OperationID) > 256 || !utf8.ValidString(rename.OperationID) ||
		len(rename.NewName) > 1024 || !utf8.ValidString(rename.NewName) {
		return gobox.Owner{}, gobox.ErrInvalid
	}
	backend.renameMu.Lock()
	defer backend.renameMu.Unlock()
	record, err := backend.readOwnerRecord(rename.Owner.Key)
	if err != nil {
		return gobox.Owner{}, err
	}
	stored := record.Owner
	if record.LastRename != nil && record.LastRename.OperationID == rename.OperationID {
		if record.LastRename.Before == rename.Owner && record.LastRename.NewName == rename.NewName {
			backend.propagateOwner(stored)
			return stored, nil
		}
		return gobox.Owner{}, gobox.ErrConflict
	}
	if !sameOwner(stored, rename.Owner) {
		return gobox.Owner{}, gobox.ErrStale
	}
	if stored.Name == rename.NewName {
		return stored, nil
	}
	before := stored
	stored.Name = rename.NewName
	stored.NameRevision++
	record.Owner = stored
	record.LastRename = &renameRecord{OperationID: rename.OperationID, Before: before, NewName: rename.NewName}
	if err = backend.writeOwnerRecord(record, false); err != nil {
		return gobox.Owner{}, err
	}
	backend.propagateOwner(stored)
	return stored, nil
}

func (backend *Backend) propagateOwner(owner gobox.Owner) {
	backend.mu.Lock()
	bound := backend.owners[owner.Key]
	backend.mu.Unlock()
	if bound != nil {
		bound.rename(owner)
	}
}

func (backend *Backend) ReleaseOwner(ctx context.Context, owner gobox.Owner) error {
	if err := ctxError(ctx); err != nil {
		return err
	}
	backend.mu.Lock()
	bound := backend.owners[owner.Key]
	if bound != nil && bound.epoch == owner.Epoch {
		delete(backend.owners, owner.Key)
	}
	backend.mu.Unlock()
	if bound == nil {
		return nil
	}
	if bound.epoch != owner.Epoch {
		return gobox.ErrStale
	}
	return bound.close()
}

func (backend *Backend) RevokeOwner(ctx context.Context, owner gobox.Owner) error {
	return backend.ReleaseOwner(ctx, owner)
}

func (backend *Backend) Close() error {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return nil
	}
	backend.closed = true
	owners := make([]*ownerSandboxes, 0, len(backend.owners))
	for _, owner := range backend.owners {
		owners = append(owners, owner)
	}
	backend.owners = make(map[string]*ownerSandboxes)
	backend.mu.Unlock()
	var result error
	for _, owner := range owners {
		result = errors.Join(result, owner.close())
	}
	result = errors.Join(result, os.RemoveAll(filepath.Join(backend.config.root, "temporary", backend.session)))
	if backend.config.ephemeralRoot {
		result = errors.Join(result, os.RemoveAll(backend.config.root))
	}
	return result
}

func (backend *Backend) ownerPath(key string) (string, error) {
	if !validKey(key) {
		return "", gobox.ErrInvalid
	}
	return filepath.Join(backend.config.root, "owners", key+".json"), nil
}

func (backend *Backend) readOwner(key string) (gobox.Owner, error) {
	record, err := backend.readOwnerRecord(key)
	return record.Owner, err
}

func (backend *Backend) readOwnerRecord(key string) (ownerRecord, error) {
	backend.registryMu.Lock()
	defer backend.registryMu.Unlock()
	name, err := backend.ownerPath(key)
	if err != nil {
		return ownerRecord{}, err
	}
	data, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return ownerRecord{}, gobox.ErrDenied
	}
	if err != nil {
		return ownerRecord{}, err
	}
	var record ownerRecord
	if err = json.Unmarshal(data, &record); err != nil || validateOwner(record.Owner, true) != nil {
		return ownerRecord{}, fmt.Errorf("%w: invalid debug owner record", gobox.ErrUnavailable)
	}
	return record, nil
}

func (backend *Backend) writeOwnerRecord(record ownerRecord, exclusive bool) error {
	backend.registryMu.Lock()
	defer backend.registryMu.Unlock()
	name, err := backend.ownerPath(record.Owner.Key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if exclusive {
		file, openErr := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr != nil {
			return openErr
		}
		_, writeErr := file.Write(data)
		return errors.Join(writeErr, file.Close())
	}
	return os.WriteFile(name, data, 0o600)
}

func (backend *Backend) ensureOpen() error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return gobox.ErrClosed
	}
	return nil
}

func validateOwner(owner gobox.Owner, requireKey bool) error {
	if requireKey && (!validKey(owner.Key) || owner.Epoch == 0) || owner.Workspace == "" || owner.Class == "" ||
		len(owner.Workspace) > 1024 || len(owner.Class) > 1024 || len(owner.Name) > 1024 ||
		!utf8.ValidString(owner.Workspace) || !utf8.ValidString(owner.Class) || !utf8.ValidString(owner.Name) {
		return gobox.ErrInvalid
	}
	return nil
}

func sameOwner(first, second gobox.Owner) bool { return first == second }

func validKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

func randomKey() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func ctxError(ctx context.Context) error {
	if ctx == nil {
		return gobox.ErrInvalid
	}
	return ctx.Err()
}
