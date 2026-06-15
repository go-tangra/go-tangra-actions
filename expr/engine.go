// Package expr evaluates workflow conditions and interpolates `${{ }}`
// expressions using CEL (the same engine the rest of go-tangra uses), exposing
// GitHub-Actions-style contexts (env, inputs, steps, needs, job, runner,
// matrix) and status functions (success, failure, always, cancelled).
//
// Security model: CEL is a sandboxed, side-effect-free language. The user's
// condition text is *never* concatenated into evaluable source — it is compiled
// as CEL and run against typed, read-only contexts. There is no host, network
// or filesystem access from an expression.
package expr

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/ext"
)

// Engine compiles and evaluates CEL conditions. One Engine should be used per
// workflow run: it carries the per-evaluation status that backs success()/
// failure()/cancelled(), and serialises evaluations with a mutex, so it is not
// reentrant but is safe to call from one run's sequential step loop.
type Engine struct {
	env *cel.Env

	mu        sync.Mutex
	cur       evalState // status visible to the status functions, set per Eval
	progCache map[string]cel.Program
}

type evalState struct {
	jobStatus string
	cancelled bool
}

// NewEngine builds the CEL environment with the action contexts and status
// functions. The status functions close over the engine so they observe the
// status of whichever evaluation is currently in flight.
func NewEngine() (*Engine, error) {
	e := &Engine{progCache: map[string]cel.Program{}}

	statusFn := func(name string, fn func(evalState) bool) cel.EnvOption {
		return cel.Function(name,
			cel.Overload(name+"_bool", []*cel.Type{}, cel.BoolType,
				cel.FunctionBinding(func(...ref.Val) ref.Val {
					return types.Bool(fn(e.cur))
				}),
			),
		)
	}

	env, err := cel.NewEnv(
		ext.Strings(),
		cel.Variable("env", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("inputs", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("steps", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("needs", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("matrix", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("job", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("runner", cel.MapType(cel.StringType, cel.StringType)),
		statusFn("success", func(s evalState) bool {
			return !s.cancelled && (s.jobStatus == StatusSuccess || s.jobStatus == "")
		}),
		statusFn("failure", func(s evalState) bool {
			return s.jobStatus == StatusFailure
		}),
		statusFn("cancelled", func(s evalState) bool {
			return s.cancelled
		}),
		statusFn("always", func(evalState) bool {
			return true
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("build CEL env: %w", err)
	}
	e.env = env
	return e, nil
}

// maxProgramCache bounds the compiled-program cache so a workflow with a huge
// number of distinct conditions cannot exhaust memory. On overflow the cache is
// flushed (compilation simply repeats), which is a cheap, safe degradation.
const maxProgramCache = 1024

// Validate compiles expr (without evaluating) and reports a syntax/type error.
// An empty expression is valid. It is serialised with Eval/Interpolate so the
// engine has a single, consistent locking contract.
func (e *Engine) Validate(expr string) error {
	if expr == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, iss := e.env.Compile(expr); iss != nil && iss.Err() != nil {
		return iss.Err()
	}
	return nil
}

// Eval evaluates a boolean condition. An empty expression defaults to true (the
// engine applies the success() default before calling Eval, so "" here means
// "no extra gate"). A non-boolean result is an error.
func (e *Engine) Eval(expr string, ctx Context) (bool, error) {
	if expr == "" {
		return true, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	out, err := e.evalLocked(expr, ctx)
	if err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("condition %q did not evaluate to a boolean (got %T)", expr, out.Value())
	}
	return b, nil
}

// evalLocked compiles (with caching) and evaluates expr against ctx. The caller
// must hold e.mu; the status functions read e.cur, which is set here.
func (e *Engine) evalLocked(expr string, ctx Context) (ref.Val, error) {
	prg, err := e.programLocked(expr)
	if err != nil {
		return nil, err
	}
	e.cur = evalState{jobStatus: orDefault(ctx.JobStatus, StatusSuccess), cancelled: ctx.Cancelled}
	out, _, err := prg.Eval(ctx.activation())
	if err != nil {
		return nil, fmt.Errorf("evaluate %q: %w", expr, err)
	}
	return out, nil
}

func (e *Engine) programLocked(expr string) (cel.Program, error) {
	if prg, ok := e.progCache[expr]; ok {
		return prg, nil
	}
	if len(e.progCache) >= maxProgramCache {
		e.progCache = map[string]cel.Program{}
	}
	ast, iss := e.env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("compile %q: %w", expr, iss.Err())
	}
	prg, err := e.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("program %q: %w", expr, err)
	}
	e.progCache[expr] = prg
	return prg, nil
}
