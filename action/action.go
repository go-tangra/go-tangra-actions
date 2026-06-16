// Package action defines the Action contract, a registry of named actions, and
// the builtin actions go-tangra-actions ships with: run, package, file,
// file_line, service, service_status, log, hostname and timezone. Actions perform
// all side effects through the injected system.System
// boundary, so they are fully testable against an in-memory fake and never
// touch the host directly.
package action

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/go-tangra/go-tangra-actions/system"
)

// Result is the outcome of running an action. Outputs become addressable from
// later conditions as steps.<id>.outputs.<key>.
type Result struct {
	// Changed reports whether the action modified host state (best effort).
	Changed bool
	// Outputs are string key/values published to subsequent steps.
	Outputs map[string]string
	Stdout  string
	Stderr  string
}

// Input is everything an action needs to run. With holds the action's
// parameters, already `${{ }}`-interpolated by the engine. Env is the merged,
// interpolated environment as KEY=VALUE entries. ConfineRoot, when non-empty,
// restricts filesystem-touching actions to that subtree.
type Input struct {
	With        map[string]string
	Env         []string
	System      system.System
	ConfineRoot string
	// Stdout, when non-nil, receives the action's output as it is produced, so it
	// can stream live (in addition to the buffered Result.Stdout). The engine
	// wires it to the live output sink per step; it is nil when no sink is set.
	// Actions that shell out should tee via system.ExecRequest writers instead;
	// this is for actions that emit output without exec (e.g. log).
	Stdout io.Writer
}

// Action is a single named unit of work.
type Action interface {
	// Name is the identifier referenced by a step's `uses`.
	Name() string
	// Run performs the work. A non-nil error marks the step as failed; the
	// Result is still returned where possible for diagnostics.
	Run(ctx context.Context, in Input) (Result, error)
}

// Registry maps action names to implementations.
type Registry struct {
	actions map[string]Action
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{actions: map[string]Action{}}
}

// Register adds a (or replaces) an action by its Name.
func (r *Registry) Register(a Action) {
	r.actions[a.Name()] = a
}

// Get returns the action registered under name.
func (r *Registry) Get(name string) (Action, bool) {
	a, ok := r.actions[name]
	return a, ok
}

// Names returns the registered action names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.actions))
	for n := range r.actions {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DefaultRegistry returns a registry pre-loaded with the builtin actions.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(&Run{})
	r.Register(&Package{})
	r.Register(&File{})
	r.Register(&FileLine{})
	r.Register(&Service{})
	r.Register(&ServiceStatus{})
	r.Register(&Log{})
	r.Register(&Hostname{})
	r.Register(&Timezone{})
	return r
}

// args is a thin typed view over a step's `with` inputs.
type args map[string]string

func (a args) str(key string) string { return strings.TrimSpace(a[key]) }

func (a args) required(key string) (string, error) {
	v := a.str(key)
	if v == "" {
		return "", fmt.Errorf("missing required input %q", key)
	}
	return v, nil
}

func (a args) withDefault(key, def string) string {
	if v := a.str(key); v != "" {
		return v
	}
	return def
}

// boolValue parses a permissive boolean (true/false/yes/no/1/0). An empty value
// returns def.
func (a args) boolValue(key string, def bool) (bool, error) {
	v := a.str(key)
	if v == "" {
		return def, nil
	}
	switch strings.ToLower(v) {
	case "true", "yes", "1", "on":
		return true, nil
	case "false", "no", "0", "off":
		return false, nil
	default:
		return false, fmt.Errorf("input %q: %q is not a boolean", key, v)
	}
}

// execOK runs a fixed binary with already-validated args (no shell) and turns a
// non-zero exit into an error. Shared by actions that shell out to a single
// command (hostname, timezone).
func execOK(ctx context.Context, in Input, bin string, cmdArgs ...string) error {
	res, err := in.System.Exec(ctx, system.ExecRequest{Name: bin, Args: cmdArgs, Env: in.Env})
	if err != nil {
		return fmt.Errorf("exec %s: %w", bin, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s exited with code %d: %s", bin, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// splitList splits a comma- or newline-separated input into trimmed,
// non-empty tokens (used for package name lists).
func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseMode parses an octal file mode string (e.g. "0644", "755"). Empty
// returns 0 and ok=false so callers can fall back to a default.
func parseMode(s string) (uint32, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, false, fmt.Errorf("invalid mode %q (want octal like 0644): %w", s, err)
	}
	return uint32(v), true, nil
}
