package gobox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
)

// TemplateID identifies a host-defined sandbox template.
type TemplateID string

// ProgramID identifies a host-approved program image.
type ProgramID string

// Root identifies one of a sandbox's storage roots.
type Root uint8

const (
	// Temporary is instance storage that is removed after verified cleanup.
	Temporary Root = iota
	// Persistent is owner-and-slot storage that survives instance replacement.
	Persistent
)

// Path is a portable path relative to a sandbox storage root.
type Path struct {
	Root Root
	Rel  string
}

// Owner is a durable identity assigned by the embedding host. Name is a
// display label and is not a storage identity.
type Owner struct {
	Workspace    string
	Key          string
	Epoch        uint64
	Class        string
	Name         string
	NameRevision uint64
}

// OwnerRename describes one idempotent owner-label change.
type OwnerRename struct {
	Owner       Owner
	NewName     string
	OperationID string
}

// Service is the host authority for owner-bound sandbox capabilities. A host
// must not give this interface to untrusted extensions.
type Service interface {
	BindOwner(context.Context, Owner) (OwnerSandboxes, error)
	RebindOwner(context.Context, OwnerRename) (Owner, error)
	ReleaseOwner(context.Context, Owner) error
	Close() error
}

// OwnerIssuer is a host-only extension used when a logical owner first needs
// sandboxes. A nonempty Key is restored metadata and must already be authorized
// by the durable registry. An empty Key requests a new unguessable identity.
type OwnerIssuer interface {
	IssueOwner(context.Context, Owner) (Owner, error)
}

// OwnerBinder combines identity issue or restore with binding. This operation
// prevents a canceled bind from leaving a new durable owner without a host
// handle.
type OwnerBinder interface {
	IssueAndBindOwner(context.Context, Owner) (Owner, OwnerSandboxes, error)
}

// OwnerRevoker lets a host synchronously revoke live capabilities before
// slower native process cleanup is complete.
type OwnerRevoker interface {
	RevokeOwner(context.Context, Owner) error
}

// OwnerSandboxes is the capability that a host gives to one logical owner.
// Slot names are local to that owner.
type OwnerSandboxes interface {
	Acquire(context.Context, string, TemplateID) (Sandbox, error)
	Remove(context.Context, string) error
}

// Sandbox is one reusable, owner-bound execution environment.
type Sandbox interface {
	Status() Status
	FS(Root) (Files, error)
	ChildPath(Path) (string, error)
	ProgramPath(ProgramID) (string, error)
	ProcessContext() ProcessContext
	NewCommand(ProgramID, Launch) (Command, error)
	DialContext(context.Context, string, string) (net.Conn, error)
	Listen(context.Context, string, string) (net.Listener, error)
	ListenPacket(context.Context, string, string) (net.PacketConn, error)
	DialUnix(context.Context, Path) (net.Conn, error)
	ListenUnix(context.Context, Path) (net.Listener, error)
	Done() <-chan struct{}
	Err() error
	Close() error
}

// ProcessContext describes values that a child program can use when it must
// address its controller.
type ProcessContext struct {
	Platform      string
	ControllerPID int
}

// Launch contains typed inputs for a protected program and its working
// directory. The backend, not the caller, converts inputs to arguments.
type Launch struct {
	Inputs    map[string]Value
	Directory Path
}

// ValueKind identifies the selected member of Value.
type ValueKind string

const (
	TextValue     ValueKind = "text"
	IntegerValue  ValueKind = "integer"
	BooleanValue  ValueKind = "boolean"
	PathValue     ValueKind = "path"
	ProgramValue  ValueKind = "program"
	EndpointValue ValueKind = "endpoint"
)

// Value is a tagged launch-input union. Exactly one value field must match
// Kind. Use Validate before a backend converts a value to a child argument.
type Value struct {
	Kind     ValueKind
	Text     string
	Int      int64
	Bool     bool
	Path     Path
	Program  ProgramID
	Endpoint string
}

// Signal is a portable command signal.
type Signal string

const (
	Interrupt Signal = "interrupt"
	Terminate Signal = "terminate"
	Kill      Signal = "kill"
)

// Command is one single-use child launch in a Sandbox.
type Command interface {
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.ReadCloser, error)
	StderrPipe() (io.ReadCloser, error)
	Start(context.Context) error
	Wait() (Exit, error)
	Signal(Signal) error
	Kill() error
	Done() <-chan struct{}
}

// Exit is the stable result of a command.
type Exit struct {
	Code   int
	Signal Signal
	Killed bool
}

// ExitError reports a command that started but did not exit successfully.
type ExitError struct{ Exit Exit }

func (e *ExitError) Error() string {
	return fmt.Sprintf("sandbox command exited with code %d (signal %q, killed %t)", e.Exit.Code, e.Exit.Signal, e.Exit.Killed)
}

// OpenOptions defines portable file-open behavior.
type OpenOptions struct {
	Read, Write, Create, Exclusive, Truncate, Append bool
	Mode                                             fs.FileMode
}

// Files provides confined operations below one sandbox root.
type Files interface {
	MkdirAll(context.Context, string, fs.FileMode) error
	TempDir(context.Context, string, string) (string, error)
	Open(context.Context, string, OpenOptions) (File, error)
	ReadFile(context.Context, string, int64) ([]byte, error)
	WriteFile(context.Context, string, []byte, fs.FileMode) error
	Rename(context.Context, string, string) error
	Stat(context.Context, string) (fs.FileInfo, error)
	ReadDir(context.Context, string, int) ([]fs.DirEntry, error)
	Remove(context.Context, string) error
	RemoveAll(context.Context, string) error
}

// File is a confined file handle. Implementations must not expose a native
// handle through this interface or through FileInfo.Sys.
type File interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
	Stat() (fs.FileInfo, error)
	Sync() error
}

var (
	ErrDenied      = errors.New("sandbox policy denied")
	ErrUnsupported = errors.New("sandbox operation unsupported")
	ErrUnavailable = errors.New("sandbox feature unavailable")
	ErrClosed      = errors.New("sandbox owner closed")
	ErrStale       = errors.New("sandbox generation stale")
	ErrConflict    = errors.New("sandbox operation conflict")
	ErrQuota       = errors.New("sandbox quota exceeded")
	ErrLaunch      = errors.New("sandbox launch failed")
	ErrInvalid     = errors.New("invalid sandbox request")
)
