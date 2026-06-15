package system

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
)

// Real is the production System backed by the host OS. It is the only type in
// the library that touches os/exec and the filesystem.
type Real struct {
	// BaseEnv, when non-nil, replaces os.Environ() as the starting environment
	// for executed processes. Nil means inherit the current process env.
	BaseEnv []string
}

// NewReal returns a Real that inherits the current process environment.
func NewReal() *Real { return &Real{} }

func (r *Real) baseEnv() []string {
	if r.BaseEnv != nil {
		return r.BaseEnv
	}
	return os.Environ()
}

// Exec runs the process described by req. Cancellation/deadline on ctx kills the
// process; that is reported as an error. A non-zero exit code is returned in the
// result, not as an error.
func (r *Real) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	var cmd *exec.Cmd
	if req.Shell != "" {
		cmd = exec.CommandContext(ctx, req.Shell, "-c", req.Name)
	} else {
		cmd = exec.CommandContext(ctx, req.Name, req.Args...)
	}

	cmd.Env = append(append([]string{}, r.baseEnv()...), req.Env...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	if req.Input != "" {
		cmd.Stdin = strings.NewReader(req.Input)
	}

	// Always buffer (for ExecResult); additionally tee to the caller's live
	// writers when provided so output can be streamed as it is produced.
	var stdout, stderr bytes.Buffer
	var outW io.Writer = &stdout
	if req.StdoutWriter != nil {
		outW = io.MultiWriter(&stdout, req.StdoutWriter)
	}
	cmd.Stdout = outW
	if req.Combine {
		cmd.Stderr = outW
	} else {
		var errW io.Writer = &stderr
		if req.StderrWriter != nil {
			errW = io.MultiWriter(&stderr, req.StderrWriter)
		}
		cmd.Stderr = errW
	}

	err := cmd.Run()

	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.ExitCode = 0
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
		// If the context ended, surface that as a real error so callers can
		// distinguish a kill from an ordinary non-zero exit.
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
	default:
		// Failed to start (binary not found, permission, etc.) — a real error.
		return res, err
	}
	return res, nil
}

func (r *Real) LookPath(file string) (string, bool) {
	p, err := exec.LookPath(file)
	if err != nil {
		return "", false
	}
	return p, true
}

func (r *Real) Stat(path string) (FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return FileInfo{Exists: false}, nil
		}
		return FileInfo{}, err
	}
	return FileInfo{
		Exists: true,
		IsDir:  fi.IsDir(),
		Mode:   uint32(fi.Mode().Perm()),
		Size:   fi.Size(),
	}, nil
}

func (r *Real) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (r *Real) WriteFile(path string, data []byte, perm uint32) error {
	return os.WriteFile(path, data, fs.FileMode(perm))
}

func (r *Real) Mkdir(path string, perm uint32) error {
	return os.MkdirAll(path, fs.FileMode(perm))
}

func (r *Real) Remove(path string) error {
	err := os.RemoveAll(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (r *Real) Chmod(path string, perm uint32) error {
	return os.Chmod(path, fs.FileMode(perm))
}

func (r *Real) Host() HostInfo { return CurrentHost() }
