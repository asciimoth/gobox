package gobox

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
)

type outputCommand struct {
	Command
	output           []byte
	stdoutR, stderrR *io.PipeReader
	stdoutW, stderrW *io.PipeWriter
	done             chan struct{}
	once             sync.Once
	killed           bool
	mu               sync.Mutex
}

func (command *outputCommand) StdoutPipe() (io.ReadCloser, error) {
	command.stdoutR, command.stdoutW = io.Pipe()
	return command.stdoutR, nil
}

func (command *outputCommand) StderrPipe() (io.ReadCloser, error) {
	command.stderrR, command.stderrW = io.Pipe()
	return command.stderrR, nil
}

func (command *outputCommand) Start(context.Context) error {
	command.done = make(chan struct{})
	go func() {
		var group sync.WaitGroup
		for _, writer := range []*io.PipeWriter{command.stdoutW, command.stderrW} {
			if writer != nil {
				group.Go(func() {
					_, _ = writer.Write(command.output)
					_ = writer.Close()
				})
			}
		}
		group.Wait()
		close(command.done)
	}()
	return nil
}

func (command *outputCommand) Wait() (Exit, error) {
	<-command.done
	command.mu.Lock()
	defer command.mu.Unlock()
	return Exit{Killed: command.killed}, nil
}

func (command *outputCommand) Kill() error {
	command.mu.Lock()
	command.killed = true
	command.mu.Unlock()
	command.once.Do(func() {
		for _, writer := range []*io.PipeWriter{command.stdoutW, command.stderrW} {
			if writer != nil {
				_ = writer.Close()
			}
		}
	})
	return nil
}

func TestBoundedOutput(t *testing.T) {
	for _, combined := range []bool{false, true} {
		command := &outputCommand{output: []byte("hello")}
		var data []byte
		var err error
		if combined {
			data, err = CombinedOutput(context.Background(), command, 100)
		} else {
			data, err = Output(context.Background(), command, 100)
		}
		expected := "hello"
		if combined {
			expected += "hello"
		}
		if err != nil || string(data) != expected {
			t.Fatalf("%q %v", data, err)
		}

		command = &outputCommand{output: make([]byte, 10000)}
		if combined {
			data, err = CombinedOutput(context.Background(), command, 5)
		} else {
			data, err = Output(context.Background(), command, 5)
		}
		if len(data) != 5 || !errors.Is(err, ErrQuota) || !command.killed {
			t.Fatalf("length=%d killed=%t error=%v", len(data), command.killed, err)
		}
	}
}
