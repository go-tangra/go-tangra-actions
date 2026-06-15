// Package engine executes a workflow: it orders jobs by their dependencies,
// evaluates per-job and per-step conditions, runs the referenced actions
// through the system boundary, and tracks status so success()/failure()/
// always() behave the way they do in GitHub Actions.
//
// Each Run uses its own expr.Engine (which carries the per-run status backing
// the status functions) and applies the configured secret Masker to everything
// it reports.
package engine

import (
	"context"
	"fmt"
	"maps"

	"github.com/go-tangra/go-tangra-actions/action"
	"github.com/go-tangra/go-tangra-actions/expr"
	"github.com/go-tangra/go-tangra-actions/secure"
	"github.com/go-tangra/go-tangra-actions/system"
	"github.com/go-tangra/go-tangra-actions/workflow"
)

// Options configures a Runner. The zero value is usable: it executes against
// the real host with the builtin actions and no confinement.
type Options struct {
	// System is the OS boundary. Defaults to system.NewReal().
	System system.System
	// Registry holds the available actions. Defaults to action.DefaultRegistry().
	Registry *action.Registry
	// Secrets are values masked in all reported output and logs.
	Secrets []string
	// ConfineRoot, when set, restricts filesystem-touching actions to that
	// subtree.
	ConfineRoot string
	// Resolver, when set, resolves a step's `uses` reference to an external
	// action (composite or scripted) when it is not a registered native action.
	// Nil disables external actions (an unknown `uses` fails the step).
	Resolver Resolver
	// ScriptRuntime, when set, executes scripted actions (runs.using other than
	// "composite", e.g. "javascript"/"lua"). Nil means scripted actions are not
	// runnable. The library ships no script engine; the consumer supplies one.
	ScriptRuntime ScriptRuntime
	// MaxActionDepth caps composite-action nesting (an action using an action
	// using …). Zero selects defaultMaxActionDepth.
	MaxActionDepth int
}

// defaultMaxActionDepth bounds composite nesting as a backstop against runaway
// or maliciously deep action graphs.
const defaultMaxActionDepth = 16

// Runner executes workflows with a fixed configuration.
type Runner struct {
	sys           system.System
	reg           *action.Registry
	masker        *secure.Masker
	confineRoot   string
	resolver      Resolver
	scriptRuntime ScriptRuntime
	maxDepth      int
}

// New builds a Runner from Options, filling in defaults.
func New(opts Options) *Runner {
	sys := opts.System
	if sys == nil {
		sys = system.NewReal()
	}
	reg := opts.Registry
	if reg == nil {
		reg = action.DefaultRegistry()
	}
	depth := opts.MaxActionDepth
	if depth <= 0 {
		depth = defaultMaxActionDepth
	}
	return &Runner{
		sys:           sys,
		reg:           reg,
		masker:        secure.NewMasker(opts.Secrets...),
		confineRoot:   opts.ConfineRoot,
		resolver:      opts.Resolver,
		scriptRuntime: opts.ScriptRuntime,
		maxDepth:      depth,
	}
}

// Run executes wf with the given inputs. It returns a RunResult describing every
// job and step. A returned error indicates the run could not proceed at all
// (e.g. a missing required input or an internal ordering failure); ordinary
// step/job failures are reported in the RunResult with Success == false, not as
// an error.
func (r *Runner) Run(ctx context.Context, wf *workflow.Workflow, inputs map[string]string) (*RunResult, error) {
	eng, err := expr.NewEngine()
	if err != nil {
		return nil, fmt.Errorf("init expression engine: %w", err)
	}

	resolvedInputs, err := resolveInputs(wf, inputs)
	if err != nil {
		return nil, err
	}

	order, err := wf.TopoOrder()
	if err != nil {
		return nil, err
	}

	host := r.sys.Host()
	result := &RunResult{
		Jobs:     make(map[string]*JobReport, len(wf.Jobs)),
		JobOrder: order,
		Success:  true,
	}
	needs := map[string]expr.NeedResult{}

	for _, jobID := range order {
		job := wf.Jobs[jobID]
		jr := r.runJob(ctx, eng, jobCtx{
			id:         jobID,
			job:        job,
			wfEnv:      wf.Env,
			inputs:     resolvedInputs,
			needs:      subsetNeeds(needs, job.Needs),
			runnerOS:   host.OS,
			runnerArch: host.Arch,
		})
		result.Jobs[jobID] = jr
		if jr.Result == StatusFailure {
			result.Success = false
		}
		needs[jobID] = expr.NeedResult{Result: jr.Result, Outputs: jr.Outputs}
	}

	return result, nil
}

// jobCtx bundles everything runJob needs.
type jobCtx struct {
	id         string
	job        workflow.Job
	wfEnv      map[string]string
	inputs     map[string]string
	needs      map[string]expr.NeedResult
	runnerOS   string
	runnerArch string
}

func (r *Runner) runJob(ctx context.Context, eng *expr.Engine, jc jobCtx) *JobReport {
	jr := &JobReport{ID: jc.id, Outputs: map[string]string{}}
	jobEnv := mergeEnv(jc.wfEnv, jc.job.Env)
	cancelled := ctx.Err() != nil

	allSucceeded, anyFailed := summariseNeeds(jc.needs)
	condCtx := expr.Context{
		Env:        jobEnv,
		Inputs:     jc.inputs,
		Needs:      jc.needs,
		RunnerOS:   jc.runnerOS,
		RunnerArch: jc.runnerArch,
		Cancelled:  cancelled,
		// JobStatus drives success()/failure() for the job condition: failure
		// only when a dependency actually failed; "skipped" (not success) when a
		// dependency was merely skipped, so failure() does not fire spuriously.
		JobStatus: needsStatus(allSucceeded, anyFailed),
	}

	run, err := r.shouldRunJob(eng, jc.job.If, condCtx, allSucceeded, cancelled)
	if err != nil {
		// A malformed job condition fails the job rather than the whole run.
		jr.Result = StatusFailure
		jr.Steps = append(jr.Steps, StepReport{
			Name: "<job if>", Outcome: StatusFailure, Conclusion: StatusFailure,
			Err: r.masker.Mask(err.Error()),
		})
		return jr
	}
	if !run {
		jr.Result = StatusSkipped
		return jr
	}

	jr.Result = r.runSteps(ctx, eng, jc, jobEnv, jr)
	return jr
}

// shouldRunJob decides whether a job runs. With no condition the job runs only
// when every dependency succeeded and the run is not cancelled. As with steps, a
// custom condition without a status function is implicitly guarded by success()
// over the dependencies; one that calls a status function takes explicit control.
func (r *Runner) shouldRunJob(eng *expr.Engine, cond string, condCtx expr.Context, allDepsSucceeded, cancelled bool) (bool, error) {
	if cond == "" {
		return allDepsSucceeded && !cancelled, nil
	}
	if !referencesStatusFunc(cond) && (!allDepsSucceeded || cancelled) {
		return false, nil
	}
	return eng.Eval(cond, condCtx)
}

// runSteps runs a job's steps in order, threading step results into the context
// and tracking the cumulative job status. It returns the final job result.
func (r *Runner) runSteps(ctx context.Context, eng *expr.Engine, jc jobCtx, jobEnv map[string]string, jr *JobReport) string {
	// Seed every id'd step so forward references read as empty rather than
	// raising a "no such key" error (matching GitHub Actions leniency).
	steps := map[string]expr.StepResult{}
	for _, s := range jc.job.Steps {
		if s.ID != "" {
			steps[s.ID] = expr.StepResult{}
		}
	}

	jobStatus := StatusSuccess
	for _, step := range jc.job.Steps {
		sr := r.runStep(ctx, eng, step, stepCtx{
			env:        mergeEnv(jobEnv, step.Env),
			inputs:     jc.inputs,
			steps:      steps,
			needs:      jc.needs,
			jobStatus:  jobStatus,
			runnerOS:   jc.runnerOS,
			runnerArch: jc.runnerArch,
		}, nil)
		jr.Steps = append(jr.Steps, sr)

		if step.ID != "" {
			steps[step.ID] = expr.StepResult{
				Outcome:    sr.Outcome,
				Conclusion: sr.Conclusion,
				Outputs:    sr.Outputs,
			}
		}
		maps.Copy(jr.Outputs, sr.Outputs)
		// A failed conclusion flips the job to failure for the remaining steps'
		// success()/failure() evaluation.
		if sr.Conclusion == StatusFailure {
			jobStatus = StatusFailure
		}
	}
	return jobStatus
}

// resolveInputs applies provided values over declared defaults and enforces
// required inputs. Only declared inputs are returned; undeclared provided values
// are ignored.
func resolveInputs(wf *workflow.Workflow, provided map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(wf.Inputs))
	for name, spec := range wf.Inputs {
		v, ok := provided[name]
		switch {
		case ok:
			// An explicitly provided value wins, even if it is empty.
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

// subsetNeeds restricts the accumulated needs map to the ids a job declares, so
// a job's condition can only see its own dependencies.
func subsetNeeds(all map[string]expr.NeedResult, declared []string) map[string]expr.NeedResult {
	out := make(map[string]expr.NeedResult, len(declared))
	for _, id := range declared {
		if r, ok := all[id]; ok {
			out[id] = r
		}
	}
	return out
}

// summariseNeeds reports whether every dependency succeeded and whether any
// dependency failed. With no dependencies, allSucceeded is true (vacuously) and
// anyFailed is false. A skipped dependency makes allSucceeded false without
// making anyFailed true — the distinction GitHub Actions draws between a
// dependency that failed and one that was merely skipped.
func summariseNeeds(needs map[string]expr.NeedResult) (allSucceeded, anyFailed bool) {
	allSucceeded = true
	for _, n := range needs {
		switch n.Result {
		case StatusSuccess:
		case StatusFailure:
			allSucceeded = false
			anyFailed = true
		default: // skipped
			allSucceeded = false
		}
	}
	return allSucceeded, anyFailed
}

// needsStatus maps the dependency summary to the JobStatus used by the job's
// condition: failure if any dep failed, success if all succeeded, else skipped.
func needsStatus(allSucceeded, anyFailed bool) string {
	switch {
	case anyFailed:
		return StatusFailure
	case allSucceeded:
		return StatusSuccess
	default:
		return StatusSkipped
	}
}
