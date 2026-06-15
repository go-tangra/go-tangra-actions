package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/go-tangra/go-tangra-actions/action"
	"github.com/go-tangra/go-tangra-actions/system"
	"github.com/go-tangra/go-tangra-actions/workflow"
)

// recordAction is a controllable test action. It records each invocation and
// can be told (via with[fail]=true) to fail; with[out_*] inputs become outputs.
type recordAction struct {
	mu    sync.Mutex
	calls []action.Input
}

func (a *recordAction) Name() string { return "test" }

func (a *recordAction) Run(_ context.Context, in action.Input) (action.Result, error) {
	a.mu.Lock()
	a.calls = append(a.calls, in)
	a.mu.Unlock()

	outputs := map[string]string{}
	for k, v := range in.With {
		if name, ok := strings.CutPrefix(k, "out_"); ok {
			outputs[name] = v
		}
	}
	res := action.Result{Outputs: outputs, Stdout: in.With["stdout"]}
	if in.With["fail"] == "true" {
		return res, fmt.Errorf("forced failure: %s", in.With["msg"])
	}
	res.Changed = true
	return res, nil
}

func (a *recordAction) names() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var n []string
	for _, c := range a.calls {
		n = append(n, c.With["tag"])
	}
	return n
}

func newTestRunner(t *testing.T, sys system.System, secrets ...string) (*Runner, *recordAction) {
	t.Helper()
	rec := &recordAction{}
	reg := action.DefaultRegistry()
	reg.Register(rec)
	r := New(Options{System: sys, Registry: reg, Secrets: secrets})
	return r, rec
}

func mustParse(t *testing.T, src string) *workflow.Workflow {
	t.Helper()
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	return wf
}

func TestRun_HappyPath(t *testing.T) {
	src := `
jobs:
  main:
    steps:
      - id: a
        uses: test
        with: { tag: a, out_sha: deadbeef }
      - id: b
        if: steps.a.outcome == 'success'
        uses: test
        with: { tag: b }
`
	r, rec := newTestRunner(t, system.NewFake())
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Errorf("expected success")
	}
	if got := rec.names(); strings.Join(got, ",") != "a,b" {
		t.Errorf("executed steps = %v, want [a b]", got)
	}
	job := res.Jobs["main"]
	if job.Result != StatusSuccess {
		t.Errorf("job result = %q", job.Result)
	}
	if job.Steps[0].Outputs["sha"] != "deadbeef" {
		t.Errorf("step a outputs = %v", job.Steps[0].Outputs)
	}
}

func TestRun_StepSkippedByCondition(t *testing.T) {
	src := `
jobs:
  main:
    steps:
      - id: a
        uses: test
        with: { tag: a }
      - id: b
        if: steps.a.outcome == 'failure'
        uses: test
        with: { tag: b }
`
	r, rec := newTestRunner(t, system.NewFake())
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if got := rec.names(); strings.Join(got, ",") != "a" {
		t.Errorf("executed = %v, want only [a]", got)
	}
	if res.Jobs["main"].Steps[1].Outcome != StatusSkipped {
		t.Errorf("step b should be skipped, got %q", res.Jobs["main"].Steps[1].Outcome)
	}
}

func TestRun_FailurePropagation(t *testing.T) {
	src := `
jobs:
  main:
    steps:
      - { id: a, uses: test, with: { tag: a } }
      - { id: b, uses: test, with: { tag: b, fail: "true", msg: boom } }
      - { id: c, uses: test, with: { tag: c } }                # default success() -> skipped
      - { id: d, if: failure(), uses: test, with: { tag: d } } # runs because job failed
      - { id: e, if: always(), uses: test, with: { tag: e } }  # always runs
`
	r, rec := newTestRunner(t, system.NewFake())
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)

	if res.Success {
		t.Error("run should not be success after a failing step")
	}
	got := strings.Join(rec.names(), ",")
	if got != "a,b,d,e" {
		t.Errorf("executed = %q, want a,b,d,e (c skipped)", got)
	}
	steps := res.Jobs["main"].Steps
	if steps[1].Outcome != StatusFailure || steps[1].Err == "" {
		t.Errorf("step b = %+v, want failure with error", steps[1])
	}
	if steps[2].Outcome != StatusSkipped {
		t.Errorf("step c should be skipped, got %q", steps[2].Outcome)
	}
	if res.Jobs["main"].Result != StatusFailure {
		t.Errorf("job result = %q, want failure", res.Jobs["main"].Result)
	}
}

func TestRun_CustomIfImplicitlyGuardedBySuccess(t *testing.T) {
	// After a failure, a custom `if` WITHOUT a status function must be skipped
	// (not evaluated), even if it references the failed step's missing outputs —
	// matching GitHub. A custom `if` WITH a status function is still evaluated.
	src := `
jobs:
  main:
    steps:
      - { id: a, uses: test, with: { tag: a, fail: "true", msg: boom } }
      - { id: guarded, if: "steps.a.outputs.missing == 'x'", uses: test, with: { tag: guarded } }
      - { id: cleanup, if: "always() && steps.a.outputs.missing == ''", uses: test, with: { tag: cleanup } }
`
	r, rec := newTestRunner(t, system.NewFake())
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	steps := res.Jobs["main"].Steps
	// guarded: skipped without error (implicit success() guard short-circuits).
	if steps[1].Outcome != StatusSkipped || steps[1].Err != "" {
		t.Errorf("guarded step = %+v, want skipped with no error", steps[1])
	}
	// cleanup: runs (always()) and reads the missing output as empty.
	if steps[2].Outcome != StatusSuccess {
		t.Errorf("cleanup step = %+v, want success (always + lenient outputs)", steps[2])
	}
	if got := strings.Join(rec.names(), ","); got != "a,cleanup" {
		t.Errorf("executed = %q, want a,cleanup", got)
	}
}

func TestRun_LenientStepOutputs(t *testing.T) {
	// Referencing an output a step never set reads as "" rather than erroring.
	src := `
jobs:
  main:
    steps:
      - { id: a, uses: test, with: { tag: a } }
      - { id: b, uses: test, with: { tag: b, saw: "[${{ steps.a.outputs.never_set }}]" } }
`
	r, rec := newTestRunner(t, system.NewFake())
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success: %s", collectErrs(res.Jobs["main"].Steps))
	}
	var saw string
	for _, c := range rec.calls {
		if c.With["tag"] == "b" {
			saw = c.With["saw"]
		}
	}
	if saw != "[]" {
		t.Errorf("missing output interpolated to %q, want []", saw)
	}
}

func TestRun_ContinueOnError(t *testing.T) {
	src := `
jobs:
  main:
    steps:
      - { id: a, continue-on-error: true, uses: test, with: { tag: a, fail: "true", msg: ignored } }
      - { id: b, uses: test, with: { tag: b } }   # default success(): still runs because a's conclusion is success
`
	r, rec := newTestRunner(t, system.NewFake())
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)

	if !res.Success {
		t.Error("continue-on-error should keep the run successful")
	}
	if got := strings.Join(rec.names(), ","); got != "a,b" {
		t.Errorf("executed = %q, want a,b", got)
	}
	a := res.Jobs["main"].Steps[0]
	if a.Outcome != StatusFailure || a.Conclusion != StatusSuccess {
		t.Errorf("step a outcome/conclusion = %q/%q, want failure/success", a.Outcome, a.Conclusion)
	}
}

func TestRun_JobNeedsSkippedOnUpstreamFailure(t *testing.T) {
	src := `
jobs:
  build:
    steps:
      - { uses: test, with: { tag: build, fail: "true", msg: x } }
  deploy:
    needs: [build]
    steps:
      - { uses: test, with: { tag: deploy } }
  notify:
    needs: [build]
    if: always()
    steps:
      - { uses: test, with: { tag: notify } }
`
	r, rec := newTestRunner(t, system.NewFake())
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)

	if res.Jobs["deploy"].Result != StatusSkipped {
		t.Errorf("deploy should be skipped (build failed), got %q", res.Jobs["deploy"].Result)
	}
	if res.Jobs["notify"].Result != StatusSuccess {
		t.Errorf("notify (if: always) should run, got %q", res.Jobs["notify"].Result)
	}
	got := strings.Join(rec.names(), ",")
	if strings.Contains(got, "deploy") {
		t.Errorf("deploy should not have executed: %q", got)
	}
	if !strings.Contains(got, "notify") {
		t.Errorf("notify should have executed: %q", got)
	}
}

func TestRun_SkippedUpstreamIsNotFailure(t *testing.T) {
	// build is skipped by its own condition. deploy (default if) must skip;
	// notify (if: failure()) must NOT run — a skipped dep is not a failure.
	src := `
jobs:
  build:
    if: env.NEVER == 'true'
    steps:
      - { uses: test, with: { tag: build } }
  deploy:
    needs: [build]
    steps:
      - { uses: test, with: { tag: deploy } }
  notify:
    needs: [build]
    if: failure()
    steps:
      - { uses: test, with: { tag: notify } }
`
	r, rec := newTestRunner(t, system.NewFake())
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Jobs["build"].Result != StatusSkipped {
		t.Errorf("build = %q, want skipped", res.Jobs["build"].Result)
	}
	if res.Jobs["deploy"].Result != StatusSkipped {
		t.Errorf("deploy = %q, want skipped (upstream skipped)", res.Jobs["deploy"].Result)
	}
	if res.Jobs["notify"].Result != StatusSkipped {
		t.Errorf("notify = %q, want skipped (failure() must be false for a skipped dep)", res.Jobs["notify"].Result)
	}
	if got := strings.Join(rec.names(), ","); got != "" {
		t.Errorf("no step should have executed, got %q", got)
	}
}

func TestRun_JobOutputsFlowToNeeds(t *testing.T) {
	src := `
jobs:
  build:
    steps:
      - { id: a, uses: test, with: { tag: build, out_artifact: app.tar } }
  deploy:
    needs: [build]
    steps:
      - { uses: test, with: { tag: deploy, name: "${{ needs.build.outputs.artifact }}" } }
`
	r, rec := newTestRunner(t, system.NewFake())
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatal("expected success")
	}
	// deploy's step received the interpolated upstream output.
	var deployCall *action.Input
	for i := range rec.calls {
		if rec.calls[i].With["tag"] == "deploy" {
			deployCall = &rec.calls[i]
		}
	}
	if deployCall == nil || deployCall.With["name"] != "app.tar" {
		t.Errorf("deploy did not receive upstream output: %+v", deployCall)
	}
}

func TestRun_Inputs(t *testing.T) {
	src := `
inputs:
  domain:
    default: example.com
  required_one:
    required: true
jobs:
  main:
    steps:
      - { uses: test, with: { tag: t, host: "${{ inputs.domain }}", token: "${{ inputs.required_one }}" } }
`
	r, rec := newTestRunner(t, system.NewFake())

	// Missing required input -> run error.
	if _, err := r.Run(context.Background(), mustParse(t, src), nil); err == nil {
		t.Error("expected error for missing required input")
	}

	// Provided input + default applied + interpolated into the step.
	res, err := r.Run(context.Background(), mustParse(t, src), map[string]string{"required_one": "tok123"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatal("expected success")
	}
	call := rec.calls[len(rec.calls)-1]
	if call.With["host"] != "example.com" {
		t.Errorf("host = %q, want example.com (default)", call.With["host"])
	}
	if call.With["token"] != "tok123" {
		t.Errorf("token = %q, want tok123 (provided)", call.With["token"])
	}
}

func TestRun_SecretMasking(t *testing.T) {
	src := `
jobs:
  main:
    steps:
      - { id: a, uses: test, with: { tag: a, stdout: "token=SUPERSECRET done", out_leaked: "x SUPERSECRET y" } }
      - { id: b, uses: test, with: { tag: b, fail: "true", msg: "failed with SUPERSECRET inside" } }
`
	r, _ := newTestRunner(t, system.NewFake(), "SUPERSECRET")
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)

	a := res.Jobs["main"].Steps[0]
	if strings.Contains(a.Stdout, "SUPERSECRET") {
		t.Errorf("secret leaked in stdout: %q", a.Stdout)
	}
	if strings.Contains(a.Outputs["leaked"], "SUPERSECRET") {
		t.Errorf("secret leaked in outputs: %q", a.Outputs["leaked"])
	}
	b := res.Jobs["main"].Steps[1]
	if strings.Contains(b.Err, "SUPERSECRET") {
		t.Errorf("secret leaked in error: %q", b.Err)
	}
}

func TestRun_UnknownActionFailsStep(t *testing.T) {
	src := `
jobs:
  main:
    steps:
      - { uses: nonexistent, with: { x: y } }
`
	r, _ := newTestRunner(t, system.NewFake())
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("unknown action should fail the run")
	}
	if !strings.Contains(res.Jobs["main"].Steps[0].Err, "unknown action") {
		t.Errorf("err = %q, want unknown action", res.Jobs["main"].Steps[0].Err)
	}
}

func TestRun_RunStepThroughEngine(t *testing.T) {
	// End-to-end with the real builtin run action and a fake system.
	f := system.NewFake()
	var gotCmd string
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		gotCmd = req.Name
		return system.ExecResult{Stdout: "hi\n", ExitCode: 0}, nil
	}
	src := `
jobs:
  main:
    steps:
      - { run: "echo ${{ inputs.who }}", shell: bash }
inputs:
  who: { default: world }
`
	r := New(Options{System: f})
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, jobs=%+v", res.Jobs["main"].Steps)
	}
	if gotCmd != "echo world" {
		t.Errorf("interpolated command = %q, want 'echo world'", gotCmd)
	}
}

func TestRun_FileActionConfinement(t *testing.T) {
	f := system.NewFake()
	src := `
jobs:
  main:
    steps:
      - { uses: file, with: { path: "../escape", content: x } }
`
	r := New(Options{System: f, ConfineRoot: "/srv/ws"})
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("confined file escape should fail the step")
	}
	if len(f.Filenames()) != 0 {
		t.Errorf("nothing should be written: %v", f.Filenames())
	}
}

func TestRun_EnvLayeringAndInterpolation(t *testing.T) {
	src := `
env:
  STAGE: base
  GLOBAL: g
jobs:
  main:
    env:
      STAGE: job
    steps:
      - id: a
        env:
          STAGE: step
        uses: test
        with: { tag: a, seen: "${{ env.STAGE }}-${{ env.GLOBAL }}" }
`
	r, rec := newTestRunner(t, system.NewFake())
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil || !res.Success {
		t.Fatalf("res=%v err=%v", res, err)
	}
	// step env overrides job overrides workflow; GLOBAL inherited.
	if got := rec.calls[0].With["seen"]; got != "step-g" {
		t.Errorf("env layering = %q, want step-g", got)
	}
}
