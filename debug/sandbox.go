package debug

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/asciimoth/gobox"
)

type ownerSandboxes struct {
	backend     *Backend
	mu          sync.Mutex
	owner       gobox.Owner
	epoch       uint64
	slots       map[string]*sandbox
	generations map[string]uint64
	closed      bool
}

var _ gobox.OwnerSandboxes = (*ownerSandboxes)(nil)

func (owner *ownerSandboxes) Acquire(ctx context.Context, slot string, templateID gobox.TemplateID) (gobox.Sandbox, error) {
	if err := ctxError(ctx); err != nil {
		return nil, err
	}
	if err := gobox.ValidateName(slot); err != nil {
		return nil, err
	}
	template, ok := owner.backend.config.templates[templateID]
	if !ok {
		return nil, gobox.ErrDenied
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil, gobox.ErrClosed
	}
	if current := owner.slots[slot]; current != nil {
		current.mu.Lock()
		closed := current.closed
		currentTemplate := current.templateID
		current.mu.Unlock()
		if !closed {
			if currentTemplate != templateID {
				return nil, gobox.ErrConflict
			}
			return current, nil
		}
	}
	owner.generations[slot]++
	generation := owner.generations[slot]
	storageOwner := owner.owner.Key[:24]
	temporary := filepath.Join(owner.backend.config.root, "temporary", owner.backend.session, storageOwner, slot, strconv.FormatUint(generation, 10))
	persistent := filepath.Join(owner.backend.config.root, "persistent", storageOwner, slot)
	if err := os.MkdirAll(temporary, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(persistent, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return nil, err
	}
	temporaryRoot, err := os.OpenRoot(temporary)
	if err != nil {
		_ = os.RemoveAll(temporary)
		return nil, err
	}
	persistentRoot, err := os.OpenRoot(persistent)
	if err != nil {
		_ = temporaryRoot.Close()
		_ = os.RemoveAll(temporary)
		return nil, err
	}
	instance := &sandbox{
		owner: owner, templateID: templateID, template: template, generation: generation,
		temporaryPath: temporary, persistentPath: persistent,
		temporaryRoot: temporaryRoot, persistentRoot: persistentRoot,
		done: make(chan struct{}), commands: make(map[*command]struct{}), resources: make(map[io.Closer]struct{}),
	}
	owner.slots[slot] = instance
	return instance, nil
}

func (owner *ownerSandboxes) Remove(ctx context.Context, slot string) error {
	if err := ctxError(ctx); err != nil {
		return err
	}
	if err := gobox.ValidateName(slot); err != nil {
		return err
	}
	owner.mu.Lock()
	instance := owner.slots[slot]
	delete(owner.slots, slot)
	owner.mu.Unlock()
	if instance == nil {
		return nil
	}
	return instance.Close()
}

func (owner *ownerSandboxes) rename(updated gobox.Owner) {
	owner.mu.Lock()
	owner.owner = updated
	owner.mu.Unlock()
}

func (owner *ownerSandboxes) close() error {
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return nil
	}
	owner.closed = true
	instances := make([]*sandbox, 0, len(owner.slots))
	for _, instance := range owner.slots {
		instances = append(instances, instance)
	}
	owner.slots = make(map[string]*sandbox)
	owner.mu.Unlock()
	var result error
	for _, instance := range instances {
		result = errors.Join(result, instance.Close())
	}
	return result
}

type sandbox struct {
	owner          *ownerSandboxes
	templateID     gobox.TemplateID
	template       Template
	generation     uint64
	temporaryPath  string
	persistentPath string
	temporaryRoot  *os.Root
	persistentRoot *os.Root

	mu        sync.Mutex
	closed    bool
	err       error
	done      chan struct{}
	commands  map[*command]struct{}
	resources map[io.Closer]struct{}
}

var _ gobox.Sandbox = (*sandbox)(nil)

func (instance *sandbox) Status() gobox.Status {
	instance.owner.mu.Lock()
	owner := instance.owner.owner
	instance.owner.mu.Unlock()
	status := debugStatus(owner, instance.templateID, instance.generation)
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.closed {
		status.State = gobox.Closed
	}
	if instance.err != nil {
		status.State = gobox.Failed
	}
	return status
}

func (instance *sandbox) FS(root gobox.Root) (gobox.Files, error) {
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.closed {
		return nil, gobox.ErrClosed
	}
	switch root {
	case gobox.Temporary:
		return &files{instance: instance, root: instance.temporaryRoot, rootID: root}, nil
	case gobox.Persistent:
		return &files{instance: instance, root: instance.persistentRoot, rootID: root}, nil
	default:
		return nil, gobox.ErrInvalid
	}
}

func (instance *sandbox) ChildPath(value gobox.Path) (string, error) {
	if err := gobox.ValidatePath(value, true); err != nil {
		return "", err
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.closed {
		return "", gobox.ErrClosed
	}
	base, err := instance.rootPath(value.Root)
	if err != nil {
		return "", err
	}
	if value.Rel == "" || value.Rel == "." {
		return base, nil
	}
	return filepath.Join(base, filepath.FromSlash(value.Rel)), nil
}

func (instance *sandbox) ProgramPath(id gobox.ProgramID) (string, error) {
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.closed {
		return "", gobox.ErrClosed
	}
	if !instance.programAllowed(id) {
		return "", gobox.ErrDenied
	}
	return instance.owner.backend.config.programs[id].Executable, nil
}

func (instance *sandbox) ProcessContext() gobox.ProcessContext {
	return gobox.ProcessContext{Platform: runtime.GOOS, ControllerPID: os.Getpid()}
}

func (instance *sandbox) NewCommand(id gobox.ProgramID, launch gobox.Launch) (gobox.Command, error) {
	if err := gobox.ValidatePath(launch.Directory, true); err != nil {
		return nil, err
	}
	instance.mu.Lock()
	if instance.closed {
		instance.mu.Unlock()
		return nil, gobox.ErrClosed
	}
	if !instance.programAllowed(id) {
		instance.mu.Unlock()
		return nil, gobox.ErrDenied
	}
	program := instance.owner.backend.config.programs[id]
	instance.mu.Unlock()
	directory, err := instance.ChildPath(launch.Directory)
	if err != nil {
		return nil, err
	}
	for name, value := range launch.Inputs {
		if gobox.ValidateName(name) != nil || value.Validate() != nil {
			return nil, fmt.Errorf("%w: launch input %q", gobox.ErrInvalid, name)
		}
	}
	var arguments []string
	if program.Arguments == nil {
		if len(launch.Inputs) != 0 {
			return nil, fmt.Errorf("%w: program %q does not accept inputs", gobox.ErrInvalid, id)
		}
	} else {
		arguments, err = program.Arguments(launch, instance.resolveValue)
		if err != nil {
			return nil, errors.Join(gobox.ErrInvalid, err)
		}
	}
	for _, argument := range arguments {
		if len(argument) > 1<<20 || !utf8.ValidString(argument) {
			return nil, gobox.ErrInvalid
		}
		for _, value := range []byte(argument) {
			if value == 0 {
				return nil, gobox.ErrInvalid
			}
		}
	}
	command := newCommand(instance, program.Executable, arguments, directory)
	instance.mu.Lock()
	if instance.closed {
		instance.mu.Unlock()
		return nil, gobox.ErrClosed
	}
	instance.commands[command] = struct{}{}
	instance.mu.Unlock()
	return command, nil
}

func (instance *sandbox) resolveValue(value gobox.Value) (string, error) {
	if err := value.Validate(); err != nil {
		return "", err
	}
	switch value.Kind {
	case gobox.TextValue:
		return value.Text, nil
	case gobox.IntegerValue:
		return strconv.FormatInt(value.Int, 10), nil
	case gobox.BooleanValue:
		return strconv.FormatBool(value.Bool), nil
	case gobox.PathValue:
		return instance.ChildPath(value.Path)
	case gobox.ProgramValue:
		return instance.ProgramPath(value.Program)
	case gobox.EndpointValue:
		return value.Endpoint, nil
	default:
		return "", gobox.ErrInvalid
	}
}

func (instance *sandbox) programAllowed(id gobox.ProgramID) bool {
	if _, ok := instance.owner.backend.config.programs[id]; !ok {
		return false
	}
	if len(instance.template.Programs) == 0 {
		return true
	}
	for _, allowed := range instance.template.Programs {
		if allowed == id {
			return true
		}
	}
	return false
}

func (instance *sandbox) rootPath(root gobox.Root) (string, error) {
	switch root {
	case gobox.Temporary:
		return instance.temporaryPath, nil
	case gobox.Persistent:
		return instance.persistentPath, nil
	default:
		return "", gobox.ErrInvalid
	}
}

func (instance *sandbox) Done() <-chan struct{} { return instance.done }

func (instance *sandbox) Err() error {
	instance.mu.Lock()
	defer instance.mu.Unlock()
	return instance.err
}

func (instance *sandbox) Close() error {
	instance.mu.Lock()
	if instance.closed {
		instance.mu.Unlock()
		return nil
	}
	instance.closed = true
	close(instance.done)
	commands := make([]*command, 0, len(instance.commands))
	for command := range instance.commands {
		commands = append(commands, command)
	}
	resources := make([]io.Closer, 0, len(instance.resources))
	for resource := range instance.resources {
		resources = append(resources, resource)
	}
	instance.mu.Unlock()
	var result error
	for _, command := range commands {
		if killErr := command.Kill(); !errors.Is(killErr, gobox.ErrConflict) {
			result = errors.Join(result, killErr)
		}
		_, waitErr := command.Wait()
		if !errors.Is(waitErr, gobox.ErrConflict) {
			var exitError *gobox.ExitError
			if !errors.As(waitErr, &exitError) {
				result = errors.Join(result, waitErr)
			}
		}
	}
	for _, resource := range resources {
		result = errors.Join(result, resource.Close())
	}
	result = errors.Join(result, instance.temporaryRoot.Close(), instance.persistentRoot.Close())
	result = errors.Join(result, os.RemoveAll(instance.temporaryPath))
	return result
}

func (instance *sandbox) commandDone(command *command) {
	instance.mu.Lock()
	delete(instance.commands, command)
	instance.mu.Unlock()
}

func (instance *sandbox) track(resource io.Closer) error {
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.closed {
		return gobox.ErrClosed
	}
	instance.resources[resource] = struct{}{}
	return nil
}

func (instance *sandbox) untrack(resource io.Closer) {
	instance.mu.Lock()
	delete(instance.resources, resource)
	instance.mu.Unlock()
}
