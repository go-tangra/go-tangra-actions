// Package jsruntime is a JavaScript engine.ScriptRuntime backed by goja. It is a
// SEPARATE package from the core library on purpose: importing it pulls in goja,
// whereas the engine/workflow/action packages stay dependency-light. A consumer
// that wants to run scripted JS actions (the tangra-actions CLI, or a host like
// go-tangra-client) wires this in via engine.Options.ScriptRuntime.
//
// Scripts run against a sandboxed `tangra` global that maps onto the
// engine.ScriptHost — there is no Node API and no raw OS access, so masking and
// path confinement still apply:
//
//	tangra.getInput(name)               -> string
//	tangra.setOutput(name, value)       -> void
//	tangra.exec(name, [args])           -> { stdout, stderr, code }
//	tangra.readFile(path)               -> string
//	tangra.writeFile(path, data, mode?) -> void   (mode octal string, default 0644)
//	tangra.log(line)                    -> void
//	tangra.env                          -> { KEY: value }
package jsruntime

import (
	"context"
	"fmt"
	"strconv"

	"github.com/dop251/goja"

	"github.com/go-tangra/go-tangra-actions/engine"
	"github.com/go-tangra/go-tangra-actions/system"
)

// Runtime executes JavaScript action scripts with goja.
type Runtime struct{}

// New returns a ready Runtime.
func New() *Runtime { return &Runtime{} }

// supported lists the runs.using values this runtime claims, mirroring the
// GitHub node aliases plus a plain "javascript"/"js".
var supported = map[string]bool{
	"javascript": true, "js": true, "node": true,
	"node12": true, "node16": true, "node20": true, "node24": true,
}

// Supports reports whether using names a JavaScript runner.
func (*Runtime) Supports(using string) bool { return supported[using] }

// Run executes the action's entry script and returns the outputs it set via
// tangra.setOutput. The script is interrupted if ctx is cancelled.
func (*Runtime) Run(ctx context.Context, inv engine.ScriptInvocation) (engine.ScriptResult, error) {
	vm := goja.New()
	outputs := map[string]string{}

	// Interrupt the VM if the run is cancelled, so a long/looping script does
	// not outlive the context.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-done:
		}
	}()

	// throw converts a Go error into a thrown JS exception so it propagates out
	// of RunProgram as a Go error.
	throw := func(err error) { panic(vm.ToValue(err.Error())) }

	tangra := map[string]any{
		"env": inv.Env,
		"getInput": func(name string) string {
			return inv.Inputs[name]
		},
		"setOutput": func(name, value string) {
			outputs[name] = value
		},
		"log": func(line string) {
			inv.Host.Log(line)
		},
		"exec": func(call goja.FunctionCall) goja.Value {
			name := call.Argument(0).String()
			var args []string
			if a := call.Argument(1); !goja.IsUndefined(a) && !goja.IsNull(a) {
				if err := vm.ExportTo(a, &args); err != nil {
					throw(fmt.Errorf("exec: args must be an array of strings: %w", err))
				}
			}
			res, err := inv.Host.Exec(ctx, system.ExecRequest{Name: name, Args: args})
			if err != nil {
				throw(fmt.Errorf("exec %s: %w", name, err))
			}
			return vm.ToValue(map[string]any{
				"stdout": res.Stdout,
				"stderr": res.Stderr,
				"code":   res.ExitCode,
			})
		},
		"readFile": func(call goja.FunctionCall) goja.Value {
			data, err := inv.Host.ReadFile(call.Argument(0).String())
			if err != nil {
				throw(err)
			}
			return vm.ToValue(string(data))
		},
		"writeFile": func(call goja.FunctionCall) goja.Value {
			path := call.Argument(0).String()
			data := call.Argument(1).String()
			perm := uint32(0o644)
			if m := call.Argument(2); !goja.IsUndefined(m) && !goja.IsNull(m) && m.String() != "" {
				v, err := strconv.ParseUint(m.String(), 8, 32)
				if err != nil {
					throw(fmt.Errorf("writeFile: invalid mode %q: %w", m.String(), err))
				}
				perm = uint32(v)
			}
			if err := inv.Host.WriteFile(path, []byte(data), perm); err != nil {
				throw(err)
			}
			return goja.Undefined()
		},
	}
	if err := vm.Set("tangra", tangra); err != nil {
		return engine.ScriptResult{}, fmt.Errorf("bind tangra: %w", err)
	}

	name := inv.Main
	if name == "" {
		name = "action.js"
	}
	prog, err := goja.Compile(name, inv.Source, false)
	if err != nil {
		return engine.ScriptResult{}, fmt.Errorf("compile %s: %w", name, err)
	}
	if _, err := vm.RunProgram(prog); err != nil {
		return engine.ScriptResult{Outputs: outputs}, fmt.Errorf("run %s: %w", name, err)
	}
	return engine.ScriptResult{Outputs: outputs}, nil
}
