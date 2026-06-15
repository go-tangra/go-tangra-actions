package system

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

// Fake is an in-memory System for tests. It records every Exec call, serves
// process results from a programmable handler, and keeps an in-memory
// filesystem. It is safe for concurrent use so it can run under -race.
type Fake struct {
	mu sync.Mutex

	// ExecFunc handles each Exec call. If nil, Exec records the call and returns
	// a zero-exit empty result.
	ExecFunc func(ctx context.Context, req ExecRequest) (ExecResult, error)

	// paths maps an executable name to its resolved path for LookPath. A name
	// absent from the map is reported as not found.
	paths map[string]string

	// files is the in-memory filesystem keyed by cleaned path.
	files map[string]*fakeFile

	host  HostInfo
	calls []ExecRequest
}

type fakeFile struct {
	data  []byte
	mode  uint32
	isDir bool
}

// NewFake returns an empty Fake with a Linux/amd64 host.
func NewFake() *Fake {
	return &Fake{
		paths: map[string]string{},
		files: map[string]*fakeFile{},
		host:  HostInfo{OS: "linux", Arch: "amd64"},
	}
}

// SetHost overrides the reported host facts.
func (f *Fake) SetHost(h HostInfo) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.host = h
	return f
}

// AddPath registers an executable so LookPath finds it.
func (f *Fake) AddPath(file string) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths[file] = "/usr/bin/" + file
	return f
}

// Calls returns a copy of the recorded Exec requests in order.
func (f *Fake) Calls() []ExecRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ExecRequest{}, f.calls...)
}

func (f *Fake) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecResult{}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, req)
	fn := f.ExecFunc
	f.mu.Unlock()

	if fn == nil {
		return ExecResult{}, nil
	}
	return fn(ctx, req)
}

func (f *Fake) LookPath(file string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.paths[file]
	return p, ok
}

func (f *Fake) Stat(p string) (FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[path.Clean(p)]
	if !ok {
		return FileInfo{Exists: false}, nil
	}
	return FileInfo{Exists: true, IsDir: ff.isDir, Mode: ff.mode, Size: int64(len(ff.data))}, nil
}

func (f *Fake) ReadFile(p string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[path.Clean(p)]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	if ff.isDir {
		return nil, &fs.PathError{Op: "read", Path: p, Err: fmt.Errorf("is a directory")}
	}
	return append([]byte{}, ff.data...), nil
}

func (f *Fake) WriteFile(p string, data []byte, perm uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := path.Clean(p)
	if existing, ok := f.files[cp]; ok && existing.isDir {
		return &fs.PathError{Op: "write", Path: p, Err: fmt.Errorf("is a directory")}
	}
	f.files[cp] = &fakeFile{data: append([]byte{}, data...), mode: perm}
	return nil
}

func (f *Fake) Mkdir(p string, perm uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := path.Clean(p)
	for _, dir := range ancestors(cp) {
		if existing, ok := f.files[dir]; ok {
			if !existing.isDir {
				return &fs.PathError{Op: "mkdir", Path: dir, Err: fmt.Errorf("not a directory")}
			}
			continue
		}
		f.files[dir] = &fakeFile{isDir: true, mode: perm}
	}
	return nil
}

func (f *Fake) Remove(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := path.Clean(p)
	delete(f.files, cp)
	// Remove children of a directory.
	prefix := cp + "/"
	for k := range f.files {
		if strings.HasPrefix(k, prefix) {
			delete(f.files, k)
		}
	}
	return nil
}

func (f *Fake) Chmod(p string, perm uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[path.Clean(p)]
	if !ok {
		return &fs.PathError{Op: "chmod", Path: p, Err: fs.ErrNotExist}
	}
	ff.mode = perm
	return nil
}

func (f *Fake) Host() HostInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.host
}

// Filenames returns the cleaned paths currently present, sorted — a test helper.
func (f *Fake) Filenames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.files))
	for k := range f.files {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// ancestors returns cp and each of its parent directories up to root, parents
// first, so Mkdir can create the chain in order.
func ancestors(cp string) []string {
	if cp == "/" || cp == "." {
		return []string{cp}
	}
	var dirs []string
	for p := cp; p != "/" && p != "." && p != ""; p = path.Dir(p) {
		dirs = append([]string{p}, dirs...)
	}
	return dirs
}
