//go:build unix

package debug

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/asciimoth/gobox"
)

type hostProcessBoundary struct{ processGroup int }

func prepareProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attachProcess(command *exec.Cmd) (processBoundary, error) {
	group, err := syscall.Getpgid(command.Process.Pid)
	if err != nil {
		return nil, err
	}
	if group != command.Process.Pid {
		return nil, gobox.ErrLaunch
	}
	return &hostProcessBoundary{processGroup: group}, nil
}

func (boundary *hostProcessBoundary) Signal(signal gobox.Signal) error {
	var native syscall.Signal
	switch signal {
	case gobox.Interrupt:
		native = syscall.SIGINT
	case gobox.Terminate:
		native = syscall.SIGTERM
	case gobox.Kill:
		native = syscall.SIGKILL
	default:
		return gobox.ErrUnsupported
	}
	err := syscall.Kill(-boundary.processGroup, native)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (boundary *hostProcessBoundary) Kill() error  { return boundary.Signal(gobox.Kill) }
func (boundary *hostProcessBoundary) Close() error { return boundary.Kill() }

func processExit(state *os.ProcessState, killed bool) gobox.Exit {
	result := gobox.Exit{Code: state.ExitCode(), Killed: killed}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return result
	}
	switch status.Signal() {
	case syscall.SIGINT:
		result.Signal = gobox.Interrupt
	case syscall.SIGTERM:
		result.Signal = gobox.Terminate
	case syscall.SIGKILL:
		result.Signal = gobox.Kill
	default:
		result.Signal = gobox.Signal(status.Signal().String())
	}
	return result
}
