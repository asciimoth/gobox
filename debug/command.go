package debug

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/asciimoth/gobox"
)

type command struct {
	instance   *sandbox
	executable string
	arguments  []string
	directory  string

	mu         sync.Mutex
	cmd        *exec.Cmd
	boundary   processBoundary
	started    bool
	finished   bool
	done       chan struct{}
	exit       gobox.Exit
	waitErr    error
	killAsked  bool
	stdinUsed  bool
	stdoutUsed bool
	stderrUsed bool
}

var _ gobox.Command = (*command)(nil)

func newCommand(instance *sandbox, executable string, arguments []string, directory string) *command {
	return &command{instance: instance, executable: executable, arguments: append([]string(nil), arguments...), directory: directory, done: make(chan struct{})}
}

func (command *command) prepareLocked() *exec.Cmd {
	if command.cmd == nil {
		command.cmd = exec.Command(command.executable, command.arguments...)
		command.cmd.Dir = command.directory
		prepareProcess(command.cmd)
	}
	return command.cmd
}

func (command *command) StdinPipe() (io.WriteCloser, error) {
	command.mu.Lock()
	defer command.mu.Unlock()
	if command.started || command.finished || command.stdinUsed {
		return nil, gobox.ErrConflict
	}
	command.stdinUsed = true
	return command.prepareLocked().StdinPipe()
}

func (command *command) StdoutPipe() (io.ReadCloser, error) {
	command.mu.Lock()
	defer command.mu.Unlock()
	if command.started || command.finished || command.stdoutUsed {
		return nil, gobox.ErrConflict
	}
	command.stdoutUsed = true
	return command.prepareLocked().StdoutPipe()
}

func (command *command) StderrPipe() (io.ReadCloser, error) {
	command.mu.Lock()
	defer command.mu.Unlock()
	if command.started || command.finished || command.stderrUsed {
		return nil, gobox.ErrConflict
	}
	command.stderrUsed = true
	return command.prepareLocked().StderrPipe()
}

func (command *command) Start(ctx context.Context) error {
	if ctx == nil {
		return gobox.ErrInvalid
	}
	command.mu.Lock()
	if command.started || command.finished {
		command.mu.Unlock()
		return gobox.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		command.finishLocked(gobox.Exit{Killed: true}, err)
		command.mu.Unlock()
		return err
	}
	command.instance.mu.Lock()
	closed := command.instance.closed
	command.instance.mu.Unlock()
	if closed {
		command.finishLocked(gobox.Exit{Killed: true}, gobox.ErrClosed)
		command.mu.Unlock()
		return gobox.ErrClosed
	}
	native := command.prepareLocked()
	if err := native.Start(); err != nil {
		err = fmt.Errorf("%w: %v", gobox.ErrLaunch, err)
		command.finishLocked(gobox.Exit{}, err)
		command.mu.Unlock()
		return err
	}
	command.started = true
	boundary, err := attachProcess(native)
	if err != nil {
		_ = native.Process.Kill()
		_, _ = native.Process.Wait()
		err = fmt.Errorf("%w: process boundary: %v", gobox.ErrLaunch, err)
		command.finishLocked(gobox.Exit{Killed: true}, err)
		command.mu.Unlock()
		return err
	}
	command.boundary = boundary
	command.mu.Unlock()
	go command.wait()
	go command.watch(ctx)
	return nil
}

func (command *command) watch(ctx context.Context) {
	select {
	case <-ctx.Done():
		_ = command.Kill()
	case <-command.instance.Done():
		_ = command.Kill()
	case <-command.done:
	}
}

func (command *command) wait() {
	err := command.cmd.Wait()
	command.mu.Lock()
	exit := processExit(command.cmd.ProcessState, command.killAsked)
	if err != nil {
		err = &gobox.ExitError{Exit: exit}
	}
	boundary := command.boundary
	command.boundary = nil
	command.finishLocked(exit, err)
	command.mu.Unlock()
	if boundary != nil {
		_ = boundary.Close()
	}
	command.instance.commandDone(command)
}

func (command *command) finishLocked(exit gobox.Exit, err error) {
	if command.finished {
		return
	}
	command.finished = true
	command.exit = exit
	command.waitErr = err
	close(command.done)
}

func (command *command) Wait() (gobox.Exit, error) {
	command.mu.Lock()
	if !command.started && !command.finished {
		command.mu.Unlock()
		return gobox.Exit{}, gobox.ErrConflict
	}
	done := command.done
	command.mu.Unlock()
	<-done
	command.mu.Lock()
	defer command.mu.Unlock()
	return command.exit, command.waitErr
}

func (command *command) Signal(signal gobox.Signal) error {
	command.mu.Lock()
	defer command.mu.Unlock()
	if !command.started {
		return gobox.ErrConflict
	}
	if command.finished || command.boundary == nil {
		return nil
	}
	if signal == gobox.Kill {
		command.killAsked = true
	}
	return command.boundary.Signal(signal)
}

func (command *command) Kill() error {
	command.mu.Lock()
	defer command.mu.Unlock()
	if !command.started {
		if command.finished {
			return nil
		}
		return gobox.ErrConflict
	}
	if command.finished || command.boundary == nil {
		return nil
	}
	command.killAsked = true
	return command.boundary.Kill()
}

func (command *command) Done() <-chan struct{} { return command.done }

type processBoundary interface {
	Signal(gobox.Signal) error
	Kill() error
	Close() error
}
