package gobox

import (
	"errors"
	"testing"
)

func TestPortablePaths(t *testing.T) {
	invalid := []string{"../x", "a/../b", "/x", "C:/x", `a\b`, "a//b", "a/./b", "a/", "a:", "NUL.txt", "com1", "COM¹", "lpt9.ext", "foo.", "foo ", "a\x00b", "a\nb", "CONIN$"}
	for _, name := range invalid {
		if err := ValidatePath(Path{Rel: name}, true); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %q: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "a", "a/b.txt", "commander", "snow/a", "two words"} {
		if err := ValidatePath(Path{Rel: name}, true); err != nil {
			t.Errorf("rejected %q: %v", name, err)
		}
	}
	if ValidatePath(Path{}, false) == nil || ValidatePath(Path{Root: 9, Rel: "a"}, true) == nil {
		t.Fatal("accepted root mutation or unknown root")
	}
}

func TestLoopbackEndpoints(t *testing.T) {
	invalid := []string{"localhost:80", "0.0.0.0:80", "[::]:80", "[::ffff:127.0.0.1]:80", "[::1%eth0]:80", "192.0.2.1:80", "127.0.0.1:0", "127.1:80"}
	for _, address := range invalid {
		if err := ValidateEndpoint("tcp", address, false); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:80", "[::1]:80"} {
		if err := ValidateEndpoint("tcp", address, false); err != nil {
			t.Error(err)
		}
	}
	if ValidateEndpoint("udp4", "127.0.0.1:0", true) != nil ||
		ValidateEndpoint("tcp4", "[::1]:80", true) == nil ||
		ValidateEndpoint("unix", "127.0.0.1:80", true) == nil {
		t.Fatal("incorrect protocol validation")
	}
}

func TestTypedInputs(t *testing.T) {
	valid := []Value{
		{Kind: TextValue, Text: "hello"},
		{Kind: IntegerValue, Int: -1},
		{Kind: BooleanValue, Bool: true},
		{Kind: PathValue, Path: Path{Persistent, "state"}},
		{Kind: ProgramValue, Program: "tor"},
		{Kind: EndpointValue, Endpoint: "127.0.0.1:9050"},
	}
	for _, value := range valid {
		if err := value.Validate(); err != nil {
			t.Errorf("%+v: %v", value, err)
		}
	}
	invalid := []Value{
		{},
		{Kind: TextValue, Int: 1},
		{Kind: IntegerValue, Bool: true},
		{Kind: ProgramValue, Program: "/bin/sh"},
		{Kind: PathValue, Path: Path{Rel: "../x"}},
		{Kind: TextValue, Text: "a\x00b"},
	}
	for _, value := range invalid {
		if value.Validate() == nil {
			t.Errorf("accepted %+v", value)
		}
	}
}

func TestOpenOptions(t *testing.T) {
	invalid := []OpenOptions{
		{},
		{Read: true, Create: true},
		{Write: true, Exclusive: true},
		{Write: true, Append: true, Truncate: true},
		{Write: true, Mode: 0o4000},
	}
	for _, options := range invalid {
		if options.Validate() == nil {
			t.Errorf("accepted %+v", options)
		}
	}
	valid := OpenOptions{Read: true, Write: true, Create: true, Exclusive: true, Mode: 0o600}
	if valid.Validate() != nil {
		t.Fatal("valid options rejected")
	}
}

func FuzzPortablePaths(f *testing.F) {
	for _, value := range []string{"a/b", "../x", "NUL", "[::1]:80", "a\x00b"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		_ = ValidatePath(Path{Rel: value}, true)
		_ = ValidateEndpoint("tcp", value, false)
		_ = ValidateName(value)
	})
}

func FuzzTypedInputs(f *testing.F) {
	f.Add(uint8(0), "hello", int64(0), false, uint8(0), "", "", "")
	f.Add(uint8(3), "", int64(0), false, uint8(Persistent), "state", "", "")
	f.Add(uint8(5), "", int64(0), false, uint8(0), "", "", "127.0.0.1:9050")
	f.Fuzz(func(t *testing.T, kind uint8, text string, integer int64, boolean bool, root uint8, rel, program, endpoint string) {
		kinds := [...]ValueKind{TextValue, IntegerValue, BooleanValue, PathValue, ProgramValue, EndpointValue, ValueKind("unknown")}
		value := Value{
			Kind: kinds[int(kind)%len(kinds)], Text: text, Int: integer, Bool: boolean,
			Path: Path{Root: Root(root), Rel: rel}, Program: ProgramID(program), Endpoint: endpoint,
		}
		_ = value.Validate()
	})
}
