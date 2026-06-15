// Package system is the single boundary between go-tangra-actions and the
// operating system. Every side effect an action performs — running a process,
// reading or writing a file, querying host facts — goes through the System
// interface. This keeps the rest of the library pure and lets tests drive
// actions against an in-memory Fake instead of mutating the real host.
//
// Only the real implementation (real.go) is permitted to import os/exec or
// touch the filesystem. Actions depend on the interface, never the concrete
// type.
package system

import (
	"context"
	"io"
	"runtime"
)

// ExecRequest describes a process to run. When Shell is empty the binary in
// Name is executed directly with Args (no shell, so no metacharacter
// interpretation) — this is the safe default. When Shell is set (e.g. "bash",
// "sh") the request is run as `<shell> -c <Name>` and Args is ignored; this is
// reserved for the explicit `run` action.
type ExecRequest struct {
	Name    string
	Args    []string
	Shell   string
	Input   string   // written to the process's stdin
	Env     []string // additional KEY=VALUE entries, appended to the base env
	Dir     string   // working directory; empty means the process default
	Combine bool     // if true, Stderr is folded into Stdout

	// StdoutWriter/StderrWriter, when non-nil, receive process output live as it
	// is produced (in addition to the buffered ExecResult). This is how a caller
	// streams logs in real time. When Combine is set, both stdout and stderr are
	// also written to StdoutWriter.
	StdoutWriter io.Writer
	StderrWriter io.Writer
}

// ExecResult is the outcome of a finished process. A non-zero ExitCode is not
// an error from Exec's perspective — only failure to start/run the process is.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// FileInfo is the subset of file metadata actions need.
type FileInfo struct {
	Exists bool
	IsDir  bool
	Mode   uint32
	Size   int64
}

// HostInfo reports facts about the machine, surfaced to conditions as the
// `runner` context.
type HostInfo struct {
	OS   string
	Arch string
}

// System is the complete OS boundary. Implementations must be safe for use by a
// single run (the engine executes steps sequentially within a run).
type System interface {
	// Exec runs a process to completion, honouring ctx cancellation and any
	// deadline already on ctx. It returns an error only when the process cannot
	// be started or is killed by the context; a non-zero exit is reported via
	// ExecResult.ExitCode.
	Exec(ctx context.Context, req ExecRequest) (ExecResult, error)

	// LookPath reports whether an executable is resolvable on PATH, used for
	// capability detection (e.g. which package manager exists).
	LookPath(file string) (string, bool)

	// Stat returns metadata for path. A missing file is not an error: it returns
	// FileInfo{Exists: false}, nil.
	Stat(path string) (FileInfo, error)
	ReadFile(path string) ([]byte, error)
	// WriteFile writes data to path with the given permission bits, creating it
	// if necessary.
	WriteFile(path string, data []byte, perm uint32) error
	// Mkdir creates path and any missing parents.
	Mkdir(path string, perm uint32) error
	// Remove deletes path (and its contents if it is a directory). Removing a
	// path that does not exist is not an error.
	Remove(path string) error
	// Chmod sets the permission bits of an existing path.
	Chmod(path string, perm uint32) error

	// Host returns static facts about the machine.
	Host() HostInfo
}

// CurrentHost reports the OS/arch of the running process, the value real
// implementations return from Host.
func CurrentHost() HostInfo {
	return HostInfo{OS: runtime.GOOS, Arch: runtime.GOARCH}
}
