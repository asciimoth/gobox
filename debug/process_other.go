//go:build !unix

package debug

import (
	"os"
	"os/exec"

	"github.com/asciimoth/gobox"
)

type hostProcessBoundary struct{ process *os.Process }

func prepareProcess(*exec.Cmd) {}

func attachProcess(command *exec.Cmd) (processBoundary, error) {
	return &hostProcessBoundary{process: command.Process}, nil
}

func (boundary *hostProcessBoundary) Signal(signal gobox.Signal) error {
	switch signal {
	case gobox.Kill:
		return boundary.Kill()
	case gobox.Interrupt:
		return boundary.process.Signal(os.Interrupt)
	default:
		return gobox.ErrUnsupported
	}
}

func (boundary *hostProcessBoundary) Kill() error  { return boundary.process.Kill() }
func (boundary *hostProcessBoundary) Close() error { return boundary.Kill() }

func processExit(state *os.ProcessState, killed bool) gobox.Exit {
	return gobox.Exit{Code: state.ExitCode(), Killed: killed}
}
