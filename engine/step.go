package engine

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"time"

	"github.com/go-tangra/go-tangra-actions/action"
	"github.com/go-tangra/go-tangra-actions/expr"
	"github.com/go-tangra/go-tangra-actions/system"
	"github.com/go-tangra/go-tangra-actions/workflow"
)

// stepCtx is the per-step evaluation environment.
type stepCtx struct {
	jobID      string
	env        map[string]string
	inputs     map[string]string
	steps      map[string]expr.StepResult
	needs      map[string]expr.NeedResult
	jobStatus  string
	runnerOS   string
	runnerArch string
}

func (sc stepCtx) condContext(cancelled bool) expr.Context {
	return expr.Context{
		Env:        sc.env,
		Inputs:     sc.inputs,
		Steps:      sc.steps,
		Needs:      sc.needs,
		JobStatus:  sc.jobStatus,
		Cancelled:  cancelled,
		RunnerOS:   sc.runnerOS,
		RunnerArch: sc.runnerArch,
	}
}

// runStep evaluates a step's condition and, if it passes, interpolates the
// step's inputs/env and runs the referenced action (native or composite). The
// returned StepReport has all secret-bearing fields masked. stack carries the
// chain of composite action references currently being expanded, for depth and
// cycle limiting.
// stepLabel is the identifier used to tag a step's output: its id, or its
// display name when it has no id.
func stepLabel(step workflow.Step) string {
	if step.ID != "" {
		return step.ID
	}
	return step.Name
}

// runStep wraps the step execution with live step-boundary events (started /
// finished+outcome) so a consumer can render GitHub-Actions-style grouped logs.
func (r *Runner) runStep(ctx context.Context, eng *expr.Engine, step workflow.Step, sc stepCtx, stack []string) StepReport {
	if r.output != nil {
		r.output(OutputEvent{
			Kind: KindStepStarted,
			Job:  sc.jobID,
			Step: stepLabel(step),
			Name: step.Name,
			Uses: step.Uses,
		})
	}
	sr := r.runStepImpl(ctx, eng, step, sc, stack)
	if r.output != nil {
		r.output(OutputEvent{
			Kind:    KindStepFinished,
			Job:     sc.jobID,
			Step:    stepLabel(step),
			Name:    step.Name,
			Outcome: sr.Outcome,
		})
	}
	return sr
}

func (r *Runner) runStepImpl(ctx context.Context, eng *expr.Engine, step workflow.Step, sc stepCtx, stack []string) StepReport {
	sr := StepReport{ID: step.ID, Name: step.Name, Uses: step.Uses, Outputs: map[string]string{}}
	cancelled := ctx.Err() != nil
	condCtx := sc.condContext(cancelled)

	shouldRun, err := r.evalStepCondition(eng, step.If, condCtx)
	if err != nil {
		return r.failStep(sr, fmt.Errorf("condition %q: %w", step.If, err))
	}
	if !shouldRun {
		sr.Outcome = StatusSkipped
		sr.Conclusion = StatusSkipped
		return sr
	}

	actionName, withRaw := resolveAction(step)

	// Interpolate env values and action inputs against the step context.
	env, err := interpolateMap(eng, sc.env, condCtx)
	if err != nil {
		return r.failStep(sr, fmt.Errorf("interpolate env: %w", err))
	}
	withVals, err := interpolateMap(eng, withRaw, condCtx)
	if err != nil {
		return r.failStep(sr, fmt.Errorf("interpolate inputs: %w", err))
	}

	runCtx, cancel := stepContext(ctx, step.TimeoutSeconds)
	defer cancel()

	// When live output is enabled, run this step's actions through a System that
	// tees process output to the sink, and give scripted actions a live writer.
	sys := r.sys
	var live io.Writer
	if r.output != nil {
		label := stepLabel(step)
		stdoutW := sinkWriter{sink: r.output, job: sc.jobID, step: label, stream: StreamStdout}
		stderrW := sinkWriter{sink: r.output, job: sc.jobID, step: label, stream: StreamStderr}
		sys = outputSystem{System: r.sys, stdout: stdoutW, stderr: stderrW}
		live = stdoutW
	}

	res, nested, runErr := r.dispatch(runCtx, eng, sc, sys, live, actionName, withVals, env, stack)

	sr.Steps = nested
	sr.Stdout = r.masker.Mask(res.Stdout)
	sr.Stderr = r.masker.Mask(res.Stderr)
	sr.Outputs = r.maskMap(res.Outputs)

	if runErr != nil {
		sr.Outcome = StatusFailure
		sr.Err = r.masker.Mask(runErr.Error())
		// continue-on-error keeps the job green: the outcome is failure but the
		// conclusion (what success()/failure() and the job result key off) is
		// success.
		if step.ContinueOnError {
			sr.Conclusion = StatusSuccess
		} else {
			sr.Conclusion = StatusFailure
		}
		return sr
	}

	sr.Outcome = StatusSuccess
	sr.Conclusion = StatusSuccess
	return sr
}

// dispatch runs the named action: a registered native action, or — failing
// that — a composite action obtained from the Resolver. It returns the action
// result, any nested (composite) step reports, and an execution error.
func (r *Runner) dispatch(ctx context.Context, eng *expr.Engine, sc stepCtx, sys system.System, live io.Writer, name string, with, env map[string]string, stack []string) (action.Result, []StepReport, error) {
	if act, ok := r.reg.Get(name); ok {
		res, err := act.Run(ctx, action.Input{
			With:        with,
			Env:         envSlice(env),
			System:      sys,
			ConfineRoot: r.confineRoot,
		})
		return res, nil, err
	}

	if r.resolver != nil {
		resolved, err := r.resolver.Resolve(ctx, name)
		if err != nil {
			return action.Result{}, nil, fmt.Errorf("resolve action %q: %w", name, err)
		}
		if resolved.Def.Runs.IsComposite() {
			return r.runComposite(ctx, eng, resolved.Def, name, with, env, stack, sc)
		}
		res, err := r.runScript(ctx, resolved, name, with, env, sys, live)
		return res, nil, err
	}

	return action.Result{}, nil, fmt.Errorf("unknown action %q: not a builtin, and no resolver is configured for external actions", name)
}

// runScript executes a scripted action through the configured ScriptRuntime.
// The script receives the action's resolved inputs and a sandboxed ScriptHost;
// its declared setOutput values come back as the action's outputs, and anything
// it logs becomes the step's stdout.
func (r *Runner) runScript(ctx context.Context, resolved *ResolvedAction, ref string, with, env map[string]string, sys system.System, live io.Writer) (action.Result, error) {
	def := resolved.Def
	using := def.Runs.Using

	if r.scriptRuntime == nil || !r.scriptRuntime.Supports(using) {
		return action.Result{}, fmt.Errorf("action %q: no script runtime for using=%q", ref, using)
	}
	if resolved.Files == nil {
		return action.Result{}, fmt.Errorf("action %q: no package files to load %q from", ref, def.Runs.Main)
	}

	src, err := fs.ReadFile(resolved.Files, def.Runs.Main)
	if err != nil {
		return action.Result{}, fmt.Errorf("action %q: read main %q: %w", ref, def.Runs.Main, err)
	}

	inputs, err := resolveActionInputs(def, with)
	if err != nil {
		return action.Result{}, fmt.Errorf("action %q: %w", ref, err)
	}

	host := newScriptHost(sys, r.confineRoot, live)
	out, err := r.scriptRuntime.Run(ctx, ScriptInvocation{
		Using:  using,
		Main:   def.Runs.Main,
		Source: string(src),
		Files:  resolved.Files,
		Inputs: inputs,
		Env:    env,
		Host:   host,
	})

	result := action.Result{Outputs: out.Outputs, Stdout: host.captured()}
	if err != nil {
		return result, fmt.Errorf("action %q: %w", ref, err)
	}
	result.Changed = true
	return result, nil
}

// runComposite expands and runs a composite action: it scopes the action's
// declared inputs from the caller's `with`, runs the action's steps in their own
// context, then computes the action's declared outputs. Nesting is bounded by
// maxDepth and a reference can not appear twice on the expansion stack (cycle
// guard).
func (r *Runner) runComposite(ctx context.Context, eng *expr.Engine, def *workflow.ActionDef, ref string, with, env map[string]string, stack []string, sc stepCtx) (action.Result, []StepReport, error) {
	if len(stack) >= r.maxDepth {
		return action.Result{}, nil, fmt.Errorf("composite action %q exceeds max nesting depth %d", ref, r.maxDepth)
	}
	if slices.Contains(stack, ref) {
		return action.Result{}, nil, fmt.Errorf("composite action cycle: %v -> %s", stack, ref)
	}
	nextStack := append(append([]string{}, stack...), ref)

	inputs, err := resolveActionInputs(def, with)
	if err != nil {
		return action.Result{}, nil, fmt.Errorf("action %q: %w", ref, err)
	}

	// Internal step context: the action sees its own inputs, a fresh steps map,
	// the inherited env and runner facts, and no job `needs`.
	steps := map[string]expr.StepResult{}
	for _, s := range def.Runs.Steps {
		if s.ID != "" {
			steps[s.ID] = expr.StepResult{}
		}
	}

	jobStatus := StatusSuccess
	reports := make([]StepReport, 0, len(def.Runs.Steps))
	for _, cstep := range def.Runs.Steps {
		csc := stepCtx{
			jobID:      sc.jobID,
			env:        mergeEnv(env, cstep.Env),
			inputs:     inputs,
			steps:      steps,
			needs:      map[string]expr.NeedResult{},
			jobStatus:  jobStatus,
			runnerOS:   sc.runnerOS,
			runnerArch: sc.runnerArch,
		}
		csr := r.runStep(ctx, eng, cstep, csc, nextStack)
		reports = append(reports, csr)
		if cstep.ID != "" {
			steps[cstep.ID] = expr.StepResult{
				Outcome:    csr.Outcome,
				Conclusion: csr.Conclusion,
				Outputs:    csr.Outputs,
			}
		}
		if csr.Conclusion == StatusFailure {
			jobStatus = StatusFailure
		}
	}

	// Compute declared outputs against the action's final internal context.
	outCtx := expr.Context{
		Env:        env,
		Inputs:     inputs,
		Steps:      steps,
		JobStatus:  jobStatus,
		RunnerOS:   sc.runnerOS,
		RunnerArch: sc.runnerArch,
	}
	outputs := make(map[string]string, len(def.Outputs))
	for name, out := range def.Outputs {
		v, err := eng.Interpolate(out.Value, outCtx)
		if err != nil {
			return action.Result{Outputs: outputs}, reports, fmt.Errorf("action %q output %q: %w", ref, name, err)
		}
		outputs[name] = v
	}

	result := action.Result{Outputs: outputs, Changed: jobStatus == StatusSuccess}
	if jobStatus == StatusFailure {
		return result, reports, fmt.Errorf("composite action %q failed", ref)
	}
	return result, reports, nil
}

// resolveActionInputs scopes a composite action's declared inputs from the
// caller-supplied (already interpolated) with-values, applying defaults and
// enforcing required inputs.
func resolveActionInputs(def *workflow.ActionDef, with map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(def.Inputs))
	for name, spec := range def.Inputs {
		v, ok := with[name]
		switch {
		case ok:
			out[name] = v
		case spec.Default != "":
			out[name] = spec.Default
		case spec.Required:
			return nil, fmt.Errorf("missing required input %q", name)
		default:
			out[name] = ""
		}
	}
	return out, nil
}

// evalStepCondition decides whether a step runs. With no condition the step runs
// only while the job is still succeeding and not cancelled. A custom condition
// that does NOT call a status function is implicitly guarded by success(): once
// the job has failed or been cancelled it is skipped without being evaluated
// (so it never runs against a failed step's results) — matching GitHub Actions.
// A condition that calls success()/failure()/always()/cancelled() takes explicit
// control and is always evaluated.
func (r *Runner) evalStepCondition(eng *expr.Engine, cond string, condCtx expr.Context) (bool, error) {
	if cond == "" {
		return condCtx.JobStatus == StatusSuccess && !condCtx.Cancelled, nil
	}
	if !referencesStatusFunc(cond) && (condCtx.JobStatus != StatusSuccess || condCtx.Cancelled) {
		return false, nil
	}
	return eng.Eval(cond, condCtx)
}

// failStep marks a report as a hard failure with a masked message.
func (r *Runner) failStep(sr StepReport, err error) StepReport {
	sr.Outcome = StatusFailure
	sr.Conclusion = StatusFailure
	sr.Err = r.masker.Mask(err.Error())
	return sr
}

func (r *Runner) maskMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = r.masker.Mask(v)
	}
	return out
}

// resolveAction maps a step to an action name and its raw input map. A `run:`
// step becomes the builtin "run" action with command/shell inputs.
func resolveAction(step workflow.Step) (string, map[string]string) {
	if step.IsRun() {
		with := map[string]string{"command": step.Run}
		if step.Shell != "" {
			with["shell"] = step.Shell
		}
		return "run", with
	}
	return step.Uses, step.With
}

// interpolateMap returns a new map with every value `${{ }}`-interpolated.
func interpolateMap(eng *expr.Engine, in map[string]string, condCtx expr.Context) (map[string]string, error) {
	out := make(map[string]string, len(in))
	for k, v := range in {
		iv, err := eng.Interpolate(v, condCtx)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		out[k] = iv
	}
	return out, nil
}

// stepContext applies a per-step timeout when one is configured, else returns
// ctx with a no-op cancel.
func stepContext(ctx context.Context, timeoutSeconds int) (context.Context, context.CancelFunc) {
	if timeoutSeconds > 0 {
		return context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	}
	return ctx, func() {}
}
