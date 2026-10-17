// Package debug provides an unsafe host-process implementation of gobox.
//
// Use this package to develop and test program drivers when a production
// sandbox backend is not available. It does not create a security boundary.
// Child programs inherit the parent environment and identity, use host
// loopback, and can access everything that the parent process can access.
package debug

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/asciimoth/gobox"
)

// ResolveValue converts one typed launch value to the value visible to a host
// process. In particular, it converts gobox paths and program IDs to host
// paths. An Arguments function must use this function for those value kinds.
type ResolveValue func(gobox.Value) (string, error)

// Program defines one executable that a debug sandbox can start.
//
// Arguments converts typed launch inputs to an argument list. It is trusted
// host configuration, not driver code. If Arguments is nil, the launch must
// contain no inputs and the program receives no arguments.
type Program struct {
	Executable string
	Arguments  func(gobox.Launch, ResolveValue) ([]string, error)
}

// Template lists the programs available in a sandbox template. An empty list
// permits every program in Config.Programs.
type Template struct {
	Programs []gobox.ProgramID
}

// Config defines a debug service. Root is optional. If Root is empty, New
// creates an ephemeral root and Close removes it. If Root is set, persistent
// storage and owner records remain below it after Close.
type Config struct {
	Root      string
	Programs  map[gobox.ProgramID]Program
	Templates map[gobox.TemplateID]Template
}

type normalizedConfig struct {
	root          string
	ephemeralRoot bool
	programs      map[gobox.ProgramID]Program
	templates     map[gobox.TemplateID]Template
}

func normalize(input Config) (normalizedConfig, error) {
	if len(input.Programs) == 0 || len(input.Templates) == 0 {
		return normalizedConfig{}, fmt.Errorf("%w: debug programs and templates are required", gobox.ErrInvalid)
	}
	result := normalizedConfig{
		programs:  make(map[gobox.ProgramID]Program, len(input.Programs)),
		templates: make(map[gobox.TemplateID]Template, len(input.Templates)),
	}
	for id, program := range input.Programs {
		if err := gobox.ValidateName(string(id)); err != nil || program.Executable == "" {
			return normalizedConfig{}, fmt.Errorf("%w: debug program %q", gobox.ErrInvalid, id)
		}
		executable, err := exec.LookPath(program.Executable)
		if err != nil {
			return normalizedConfig{}, fmt.Errorf("%w: debug program %q: %v", gobox.ErrUnavailable, id, err)
		}
		executable, err = filepath.Abs(executable)
		if err != nil {
			return normalizedConfig{}, fmt.Errorf("%w: debug program %q: %v", gobox.ErrInvalid, id, err)
		}
		program.Executable = executable
		result.programs[id] = program
	}
	for id, template := range input.Templates {
		if err := gobox.ValidateName(string(id)); err != nil {
			return normalizedConfig{}, fmt.Errorf("%w: debug template %q", gobox.ErrInvalid, id)
		}
		seen := make(map[gobox.ProgramID]bool, len(template.Programs))
		for _, program := range template.Programs {
			if _, ok := result.programs[program]; !ok || seen[program] {
				return normalizedConfig{}, fmt.Errorf("%w: debug template %q program %q", gobox.ErrInvalid, id, program)
			}
			seen[program] = true
		}
		template.Programs = slices.Clone(template.Programs)
		result.templates[id] = template
	}
	if input.Root == "" {
		root, err := os.MkdirTemp("", "gobox-debug-")
		if err != nil {
			return normalizedConfig{}, err
		}
		result.root = root
		result.ephemeralRoot = true
	} else {
		root, err := filepath.Abs(input.Root)
		if err != nil {
			return normalizedConfig{}, fmt.Errorf("%w: debug root: %v", gobox.ErrInvalid, err)
		}
		if err = os.MkdirAll(root, 0o700); err != nil {
			return normalizedConfig{}, err
		}
		result.root = root
	}
	for _, directory := range []string{"owners", "persistent", "temporary"} {
		if err := os.MkdirAll(filepath.Join(result.root, directory), 0o700); err != nil {
			if result.ephemeralRoot {
				_ = os.RemoveAll(result.root)
			}
			return normalizedConfig{}, err
		}
	}
	return result, nil
}

func disabledFeatures() []gobox.FeatureResult {
	features := []gobox.Feature{
		gobox.PrivilegeRemoval, gobox.Environment, gobox.HandleInheritance,
		gobox.WritableStorage, gobox.FilesystemAccess, gobox.ExactFilesystemView,
		gobox.PrivateNetwork, gobox.ProcessIsolation, gobox.ProcessTree,
		gobox.ResourceLimits,
	}
	result := make([]gobox.FeatureResult, 0, len(features))
	for _, feature := range features {
		result = append(result, gobox.FeatureResult{Feature: feature, State: gobox.Disabled, Reason: "debug-backend"})
	}
	return result
}

func debugStatus(owner gobox.Owner, template gobox.TemplateID, generation uint64) gobox.Status {
	return gobox.Status{
		Owner:             owner,
		Template:          template,
		Generation:        generation,
		Mode:              gobox.Off,
		Profile:           "debug-host-v1",
		EffectiveIdentity: currentIdentity(),
		Features:          disabledFeatures(),
		Warnings:          []string{"debug backend: child processes are not sandboxed"},
		State:             gobox.Ready,
		Cleanup:           "best-effort host process cleanup",
	}
}

func currentIdentity() string {
	return fmt.Sprintf("%s/%s pid=%d", runtime.GOOS, runtime.GOARCH, os.Getpid())
}
