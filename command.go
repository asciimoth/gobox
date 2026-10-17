package gobox

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

// Run starts a command and waits for its stable exit result.
func Run(ctx context.Context, command Command) (Exit, error) {
	if err := command.Start(ctx); err != nil {
		return Exit{}, err
	}
	return command.Wait()
}

// Output captures stdout up to limit bytes. On overflow, it kills the command
// tree and drains the pipe.
func Output(ctx context.Context, command Command, limit int64) ([]byte, error) {
	return capture(ctx, command, limit, false)
}

// CombinedOutput captures both output streams in arrival order, up to limit
// bytes. On overflow, it kills the command tree and drains both pipes.
func CombinedOutput(ctx context.Context, command Command, limit int64) ([]byte, error) {
	return capture(ctx, command, limit, true)
}

type captureBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int64
}

func (buffer *captureBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	length := int64(len(data))
	left := buffer.limit - int64(len(buffer.data))
	if length > left {
		buffer.data = append(buffer.data, data[:int(left)]...)
		return int(left), ErrQuota
	}
	buffer.data = append(buffer.data, data...)
	return len(data), nil
}

func capture(ctx context.Context, command Command, limit int64, combined bool) ([]byte, error) {
	if limit < 1 || limit > 64<<20 {
		return nil, ErrInvalid
	}
	if bounded, ok := command.(interface{ OutputLimit() int64 }); ok && limit > bounded.OutputLimit() {
		return nil, ErrQuota
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = stdout.Close() }()
	streams := []io.ReadCloser{stdout}
	if combined {
		stderr, stderrErr := command.StderrPipe()
		if stderrErr != nil {
			return nil, stderrErr
		}
		defer func() { _ = stderr.Close() }()
		streams = append(streams, stderr)
	}
	if err = command.Start(ctx); err != nil {
		return nil, err
	}
	buffer := &captureBuffer{limit: limit}
	var group sync.WaitGroup
	copyErrors := make(chan error, len(streams))
	for _, stream := range streams {
		group.Go(func() {
			_, copyErr := io.Copy(buffer, stream)
			// Wait can close an os/exec pipe after process exit while this
			// goroutine consumes the final bytes.
			if errors.Is(copyErr, os.ErrClosed) {
				return
			}
			if copyErr != nil {
				killErr := command.Kill()
				_, drainErr := io.Copy(io.Discard, stream)
				copyErrors <- errors.Join(copyErr, killErr, drainErr)
			}
		})
	}
	_, err = command.Wait()
	group.Wait()
	close(copyErrors)
	for copyErr := range copyErrors {
		err = errors.Join(err, copyErr)
	}
	return buffer.data, err
}
