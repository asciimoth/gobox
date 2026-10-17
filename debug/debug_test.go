package debug_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/gobox"
	"github.com/asciimoth/gobox/debug"
)

func TestHelperProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		return
	}
	arguments := os.Args[separator+1:]
	if len(arguments) != 2 {
		os.Exit(2)
	}
	switch arguments[0] {
	case "print":
		_, _ = fmt.Fprint(os.Stdout, arguments[1])
	case "env":
		_, _ = fmt.Fprint(os.Stdout, os.Getenv(arguments[1]))
	case "sleep":
		duration, err := time.ParseDuration(arguments[1])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(duration)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func testBackend(t *testing.T) (*debug.Backend, gobox.Owner, gobox.OwnerSandboxes) {
	t.Helper()
	root, err := os.MkdirTemp("", "gb-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	backend, err := debug.New(debug.Config{
		Root: root,
		Programs: map[gobox.ProgramID]debug.Program{
			"helper": {
				Executable: os.Args[0],
				Arguments: func(launch gobox.Launch, resolve debug.ResolveValue) ([]string, error) {
					operation, ok := launch.Inputs["operation"]
					if !ok {
						return nil, errors.New("operation is required")
					}
					value, ok := launch.Inputs["value"]
					if !ok {
						return nil, errors.New("value is required")
					}
					operationText, err := resolve(operation)
					if err != nil {
						return nil, err
					}
					valueText, err := resolve(value)
					if err != nil {
						return nil, err
					}
					return []string{"-test.run=TestHelperProcess", "--", operationText, valueText}, nil
				},
			},
		},
		Templates: map[gobox.TemplateID]debug.Template{"driver": {Programs: []gobox.ProgramID{"helper"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Errorf("close backend: %v", err)
		}
	})
	owner, sandboxes, err := backend.IssueAndBindOwner(context.Background(), gobox.Owner{
		Workspace: "test", Class: "test/Driver", Name: "driver",
	})
	if err != nil {
		t.Fatal(err)
	}
	return backend, owner, sandboxes
}

func acquire(t *testing.T, sandboxes gobox.OwnerSandboxes) gobox.Sandbox {
	t.Helper()
	instance, err := sandboxes.Acquire(context.Background(), "main", "driver")
	if err != nil {
		t.Fatal(err)
	}
	return instance
}

func TestDebugCommandAndStatus(t *testing.T) {
	t.Setenv("GOBOX_DEBUG_TEST_VALUE", "inherited")
	backend, owner, sandboxes := testBackend(t)
	instance := acquire(t, sandboxes)
	status := instance.Status()
	if status.Mode != gobox.Off || status.State != gobox.Ready || status.FullBoundary || status.StrictSatisfied {
		t.Fatalf("unexpected status: %+v", status)
	}
	for _, feature := range status.Features {
		if feature.State != gobox.Disabled {
			t.Fatalf("feature is not disabled: %+v", feature)
		}
	}
	serviceStatus := backend.ServiceStatus()
	if serviceStatus.Mode != gobox.Off || serviceStatus.ActiveOwners != 1 {
		t.Fatalf("unexpected service status: %+v", serviceStatus)
	}

	command, err := instance.NewCommand("helper", gobox.Launch{
		Directory: gobox.Path{Root: gobox.Temporary},
		Inputs: map[string]gobox.Value{
			"operation": {Kind: gobox.TextValue, Text: "print"},
			"value":     {Kind: gobox.TextValue, Text: "hello from debug"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := gobox.Output(context.Background(), command, 1024)
	if err != nil || string(output) != "hello from debug" {
		t.Fatalf("output %q: %v", output, err)
	}
	command, err = instance.NewCommand("helper", gobox.Launch{
		Directory: gobox.Path{Root: gobox.Temporary},
		Inputs: map[string]gobox.Value{
			"operation": {Kind: gobox.TextValue, Text: "env"},
			"value":     {Kind: gobox.TextValue, Text: "GOBOX_DEBUG_TEST_VALUE"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err = gobox.Output(context.Background(), command, 1024)
	if err != nil || string(output) != "inherited" {
		t.Fatalf("inherited environment %q: %v", output, err)
	}

	updated, err := backend.RebindOwner(context.Background(), gobox.OwnerRename{
		Owner: owner, NewName: "renamed", OperationID: "rename-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.NameRevision != owner.NameRevision+1 || instance.Status().Owner != updated {
		t.Fatalf("rename was not propagated: %+v", updated)
	}
	repeated, err := backend.RebindOwner(context.Background(), gobox.OwnerRename{
		Owner: owner, NewName: "renamed", OperationID: "rename-1",
	})
	if err != nil || repeated != updated {
		t.Fatalf("idempotent rename %+v: %v", repeated, err)
	}
}

func TestDebugStorageLifecycle(t *testing.T) {
	_, _, sandboxes := testBackend(t)
	instance := acquire(t, sandboxes)
	persistent, err := instance.FS(gobox.Persistent)
	if err != nil {
		t.Fatal(err)
	}
	if err = persistent.WriteFile(context.Background(), "state.txt", []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	temporaryPath, err := instance.ChildPath(gobox.Path{Root: gobox.Temporary})
	if err != nil {
		t.Fatal(err)
	}
	if err = sandboxes.Remove(context.Background(), "main"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary directory still exists: %v", err)
	}

	instance = acquire(t, sandboxes)
	persistent, err = instance.FS(gobox.Persistent)
	if err != nil {
		t.Fatal(err)
	}
	data, err := persistent.ReadFile(context.Background(), "state.txt", 32)
	if err != nil || string(data) != "kept" {
		t.Fatalf("persistent data %q: %v", data, err)
	}
	opened, err := persistent.Open(context.Background(), "state.txt", gobox.OpenOptions{Read: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, exposesHandle := opened.(interface{ Fd() uintptr }); exposesHandle {
		t.Fatal("debug file exposes a native descriptor")
	}
	info, err := opened.Stat()
	if err != nil || info.Sys() != nil {
		t.Fatalf("file metadata exposes native data: %v %+v", err, info)
	}
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDebugLoopbackSockets(t *testing.T) {
	_, _, sandboxes := testBackend(t)
	instance := acquire(t, sandboxes)
	listener, err := instance.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	received := make(chan string, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			received <- acceptErr.Error()
			return
		}
		defer func() { _ = connection.Close() }()
		data, readErr := io.ReadAll(io.LimitReader(connection, 16))
		if readErr != nil {
			received <- readErr.Error()
			return
		}
		received <- string(data)
	}()
	connection, err := instance.DialContext(context.Background(), "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Write([]byte("loopback")); err != nil {
		t.Fatal(err)
	}
	if err = connection.Close(); err != nil {
		t.Fatal(err)
	}
	if value := <-received; value != "loopback" {
		t.Fatalf("received %q", value)
	}
	if _, err = instance.DialContext(context.Background(), "tcp", "192.0.2.1:80"); !errors.Is(err, gobox.ErrInvalid) {
		t.Fatalf("non-loopback dial error: %v", err)
	}
}

func TestDebugUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are not portable on Windows")
	}
	_, _, sandboxes := testBackend(t)
	instance := acquire(t, sandboxes)
	socketPath := gobox.Path{Root: gobox.Temporary, Rel: "driver.sock"}
	listener, err := instance.ListenUnix(context.Background(), socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()
	client, err := instance.DialUnix(context.Background(), socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	_ = client.Close()
	_ = server.Close()
}

func TestDebugCancellationKillsCommand(t *testing.T) {
	_, _, sandboxes := testBackend(t)
	instance := acquire(t, sandboxes)
	command, err := instance.NewCommand("helper", gobox.Launch{
		Directory: gobox.Path{Root: gobox.Temporary},
		Inputs: map[string]gobox.Value{
			"operation": {Kind: gobox.TextValue, Text: "sleep"},
			"value":     {Kind: gobox.TextValue, Text: "1m"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = command.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-command.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("command was not killed")
	}
	exit, err := command.Wait()
	var exitError *gobox.ExitError
	if !errors.As(err, &exitError) || !exit.Killed {
		t.Fatalf("exit=%+v error=%v", exit, err)
	}
}

func TestDebugPersistentOwnerRecord(t *testing.T) {
	root := t.TempDir()
	configuration := debug.Config{
		Root:      root,
		Programs:  map[gobox.ProgramID]debug.Program{"helper": {Executable: os.Args[0]}},
		Templates: map[gobox.TemplateID]debug.Template{"driver": {}},
	}
	first, err := debug.New(configuration)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := first.IssueOwner(context.Background(), gobox.Owner{Workspace: "test", Class: "driver"})
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := debug.New(configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	restored, err := second.IssueOwner(context.Background(), owner)
	if err != nil || restored != owner {
		t.Fatalf("restore %+v: %v", restored, err)
	}
	if _, err = os.Stat(filepath.Join(root, "owners", owner.Key+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestDebugRejectsUnknownProgram(t *testing.T) {
	_, _, sandboxes := testBackend(t)
	instance := acquire(t, sandboxes)
	_, err := instance.NewCommand("unknown", gobox.Launch{Directory: gobox.Path{Root: gobox.Temporary}})
	if !errors.Is(err, gobox.ErrDenied) || !strings.Contains(err.Error(), gobox.ErrDenied.Error()) {
		t.Fatalf("unexpected error: %v", err)
	}
}
