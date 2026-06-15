package engine

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"sync"

	"github.com/go-tangra/go-tangra-actions/secure"
	"github.com/go-tangra/go-tangra-actions/system"
)

// ScriptRuntime executes scripted (e.g. JavaScript or Lua) action packages. The
// library deliberately ships no script engine: a consumer (e.g. go-tangra-client,
// which already runs JS/Lua via goja/gopher-lua) implements this interface and
// passes it in via Options.ScriptRuntime. The runtime must confine the script to
// the capabilities exposed by the provided ScriptHost — it must NOT grant raw OS
// access — so masking, path confinement and validation continue to apply.
type ScriptRuntime interface {
	// Supports reports whether this runtime can execute an action whose
	// runs.using equals the given language (e.g. "javascript", "node", "lua").
	Supports(using string) bool

	// Run executes the action's entry script and returns the outputs it set.
	Run(ctx context.Context, inv ScriptInvocation) (ScriptResult, error)
}

// ScriptInvocation is everything a runtime needs to execute a scripted action.
type ScriptInvocation struct {
	// Using is the script language (runs.using).
	Using string
	// Main is the entry script path within Files (runs.main).
	Main string
	// Source is the contents of Main, read by the engine for convenience.
	Source string
	// Files is the action package, for resolving additional imports/requires.
	Files fs.FS
	// Inputs are the action's resolved inputs (defaults applied, required
	// enforced), exposed to the script (e.g. as INPUT_* or a getInput API).
	Inputs map[string]string
	// Env is the merged, interpolated environment.
	Env map[string]string
	// Host is the sandboxed capability surface the script may call.
	Host ScriptHost
}

// ScriptResult is what a scripted action returns.
type ScriptResult struct {
	// Outputs are the key/values the script published (e.g. via a setOutput API).
	Outputs map[string]string
}

// ScriptHost is the capability surface a scripted action is allowed to use. Every
// method routes through the engine's system boundary and security controls, so a
// script can do no more than a builtin action: run processes, read/write
// confined files, and log. There is intentionally no raw filesystem or exec
// escape hatch.
type ScriptHost interface {
	// Exec runs a process through the system boundary.
	Exec(ctx context.Context, req system.ExecRequest) (system.ExecResult, error)
	// ReadFile reads a file, subject to path confinement.
	ReadFile(path string) ([]byte, error)
	// WriteFile writes a file, subject to path confinement.
	WriteFile(path string, data []byte, perm uint32) error
	// Log records a line of script output (surfaced as the step's stdout).
	Log(line string)
}

// scriptHost is the engine's ScriptHost implementation. It enforces path
// confinement on file access and accumulates logged output. It is safe for
// concurrent use by a runtime that fans out.
type scriptHost struct {
	sys         system.System
	confineRoot string
	live        io.Writer // optional: receives logged lines as they are produced

	mu  sync.Mutex
	log strings.Builder
}

func newScriptHost(sys system.System, confineRoot string, live io.Writer) *scriptHost {
	return &scriptHost{sys: sys, confineRoot: confineRoot, live: live}
}

func (h *scriptHost) Exec(ctx context.Context, req system.ExecRequest) (system.ExecResult, error) {
	// Confine an explicit working directory the same way file paths are confined.
	if req.Dir != "" {
		dir, err := secure.Confine(h.confineRoot, req.Dir)
		if err != nil {
			return system.ExecResult{}, err
		}
		req.Dir = dir
	}
	return h.sys.Exec(ctx, req)
}

func (h *scriptHost) ReadFile(path string) ([]byte, error) {
	p, err := secure.Confine(h.confineRoot, path)
	if err != nil {
		return nil, err
	}
	return h.sys.ReadFile(p)
}

func (h *scriptHost) WriteFile(path string, data []byte, perm uint32) error {
	p, err := secure.Confine(h.confineRoot, path)
	if err != nil {
		return err
	}
	return h.sys.WriteFile(p, data, perm)
}

func (h *scriptHost) Log(line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.log.WriteString(line)
	if !strings.HasSuffix(line, "\n") {
		h.log.WriteByte('\n')
	}
	// Forward live so a streaming consumer sees script output as it happens.
	if h.live != nil {
		s := line
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		_, _ = io.WriteString(h.live, s)
	}
}

func (h *scriptHost) captured() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.log.String()
}
