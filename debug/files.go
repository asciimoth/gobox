package debug

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/asciimoth/gobox"
)

type files struct {
	instance *sandbox
	root     *os.Root
	rootID   gobox.Root
}

var _ gobox.Files = (*files)(nil)

func (files *files) check(ctx context.Context, name string, allowRoot bool) (string, error) {
	if err := ctxError(ctx); err != nil {
		return "", err
	}
	if err := gobox.ValidatePath(gobox.Path{Root: files.rootID, Rel: name}, allowRoot); err != nil {
		return "", err
	}
	files.instance.mu.Lock()
	closed := files.instance.closed
	files.instance.mu.Unlock()
	if closed {
		return "", gobox.ErrClosed
	}
	if name == "" {
		name = "."
	}
	current := ""
	for _, component := range strings.Split(name, "/") {
		if component == "." {
			continue
		}
		current = path.Join(current, component)
		info, err := files.root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", gobox.ErrDenied
		}
	}
	return name, nil
}

func (files *files) MkdirAll(ctx context.Context, name string, mode fs.FileMode) error {
	if mode & ^fs.FileMode(0o777) != 0 {
		return gobox.ErrInvalid
	}
	name, err := files.check(ctx, name, false)
	if err != nil {
		return err
	}
	return files.root.MkdirAll(name, mode.Perm())
}

func (files *files) TempDir(ctx context.Context, directory, pattern string) (string, error) {
	if len(pattern) > 128 || strings.ContainsAny(pattern, `/\`) || strings.ContainsRune(pattern, 0) {
		return "", gobox.ErrInvalid
	}
	directory, err := files.check(ctx, directory, true)
	if err != nil {
		return "", err
	}
	for range 100 {
		suffix, randomErr := randomKey()
		if randomErr != nil {
			return "", randomErr
		}
		name := path.Join(directory, pattern+suffix[:16])
		if err = files.root.Mkdir(name, 0o700); err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", gobox.ErrQuota
}

func (files *files) Open(ctx context.Context, name string, options gobox.OpenOptions) (gobox.File, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	name, err := files.check(ctx, name, false)
	if err != nil {
		return nil, err
	}
	flags := 0
	switch {
	case options.Read && options.Write:
		flags = os.O_RDWR
	case options.Write:
		flags = os.O_WRONLY
	default:
		flags = os.O_RDONLY
	}
	if options.Create {
		flags |= os.O_CREATE
	}
	if options.Exclusive {
		flags |= os.O_EXCL
	}
	if options.Truncate {
		flags |= os.O_TRUNC
	}
	if options.Append {
		flags |= os.O_APPEND
	}
	opened, err := files.root.OpenFile(name, flags, options.Mode.Perm())
	if err != nil {
		return nil, err
	}
	result := &file{native: opened, instance: files.instance}
	if err = files.instance.track(result); err != nil {
		_ = opened.Close()
		return nil, err
	}
	return result, nil
}

func (files *files) ReadFile(ctx context.Context, name string, maximum int64) ([]byte, error) {
	if maximum < 0 || maximum > 1<<30 {
		return nil, gobox.ErrInvalid
	}
	name, err := files.check(ctx, name, false)
	if err != nil {
		return nil, err
	}
	opened, err := files.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = opened.Close() }()
	data, err := io.ReadAll(io.LimitReader(opened, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, gobox.ErrQuota
	}
	return data, nil
}

func (files *files) WriteFile(ctx context.Context, name string, data []byte, mode fs.FileMode) error {
	if mode & ^fs.FileMode(0o777) != 0 || int64(len(data)) > 1<<30 {
		return gobox.ErrInvalid
	}
	name, err := files.check(ctx, name, false)
	if err != nil {
		return err
	}
	return files.root.WriteFile(name, data, mode.Perm())
}

func (files *files) Rename(ctx context.Context, oldName, newName string) error {
	oldName, err := files.check(ctx, oldName, false)
	if err != nil {
		return err
	}
	newName, err = files.check(ctx, newName, false)
	if err != nil {
		return err
	}
	return files.root.Rename(oldName, newName)
}

func (files *files) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	name, err := files.check(ctx, name, true)
	if err != nil {
		return nil, err
	}
	info, err := files.root.Stat(name)
	if err != nil {
		return nil, err
	}
	return cleanInfo{FileInfo: info}, nil
}

func (files *files) ReadDir(ctx context.Context, name string, count int) ([]fs.DirEntry, error) {
	if count < 0 || count > 1<<20 {
		return nil, gobox.ErrInvalid
	}
	name, err := files.check(ctx, name, true)
	if err != nil {
		return nil, err
	}
	opened, err := files.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = opened.Close() }()
	entries, err := opened.ReadDir(count)
	result := make([]fs.DirEntry, len(entries))
	for index, entry := range entries {
		result[index] = cleanDirEntry{DirEntry: entry}
	}
	return result, err
}

func (files *files) Remove(ctx context.Context, name string) error {
	name, err := files.check(ctx, name, false)
	if err != nil {
		return err
	}
	return files.root.Remove(name)
}

func (files *files) RemoveAll(ctx context.Context, name string) error {
	name, err := files.check(ctx, name, false)
	if err != nil {
		return err
	}
	return files.root.RemoveAll(name)
}

type file struct {
	native   *os.File
	instance *sandbox
	once     sync.Once
	err      error
}

var _ gobox.File = (*file)(nil)

func (file *file) Stat() (fs.FileInfo, error) {
	info, err := file.native.Stat()
	if err != nil {
		return nil, err
	}
	return cleanInfo{FileInfo: info}, nil
}

func (file *file) Read(data []byte) (int, error)  { return file.native.Read(data) }
func (file *file) Write(data []byte) (int, error) { return file.native.Write(data) }
func (file *file) Seek(offset int64, whence int) (int64, error) {
	return file.native.Seek(offset, whence)
}
func (file *file) Close() error {
	file.once.Do(func() {
		file.err = file.native.Close()
		file.instance.untrack(file)
	})
	return file.err
}
func (file *file) Sync() error { return file.native.Sync() }

type cleanInfo struct{ fs.FileInfo }

func (cleanInfo) Sys() any { return nil }

type cleanDirEntry struct{ fs.DirEntry }

func (entry cleanDirEntry) Info() (fs.FileInfo, error) {
	info, err := entry.DirEntry.Info()
	if err != nil {
		return nil, err
	}
	return cleanInfo{FileInfo: info}, nil
}
