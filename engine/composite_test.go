package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-actions/action"
	"github.com/go-tangra/go-tangra-actions/system"
	"github.com/go-tangra/go-tangra-actions/workflow"
)

// newCompositeRunner wires a runner with the recording test action plus a
// MapResolver catalog of composite actions.
func newCompositeRunner(t *testing.T, catalog MapResolver, secrets ...string) (*Runner, *recordAction) {
	t.Helper()
	rec := &recordAction{}
	reg := action.DefaultRegistry()
	reg.Register(rec)
	r := New(Options{
		System:   system.NewFake(),
		Registry: reg,
		Resolver: catalog,
		Secrets:  secrets,
	})
	return r, rec
}

// mustAction parses a composite manifest and wraps it as a ResolvedAction (no
// package files needed for composites), ready to drop into a MapResolver.
func mustAction(t *testing.T, src string) *ResolvedAction {
	t.Helper()
	def, err := workflow.ParseAction([]byte(src))
	if err != nil {
		t.Fatalf("ParseAction: %v", err)
	}
	return &ResolvedAction{Def: def}
}

func TestComposite_RunsWithInputsAndOutputs(t *testing.T) {
	catalog := MapResolver{
		"setup": mustAction(t, `
inputs:
  who:
    required: true
  greeting:
    default: hello
outputs:
  message:
    value: "${{ steps.s.outputs.echo }}"
runs:
  using: composite
  steps:
    - id: s
      uses: test
      with: { tag: inner, out_echo: "${{ inputs.greeting }} ${{ inputs.who }}" }
`),
	}
	r, rec := newCompositeRunner(t, catalog)

	src := `
jobs:
  main:
    steps:
      - id: call
        uses: setup
        with: { who: world }
      - id: after
        uses: test
        with: { tag: after, saw: "${{ steps.call.outputs.message }}" }
`
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, steps=%+v", res.Jobs["main"].Steps)
	}

	// The composite's declared output flowed to the caller's step outputs and
	// then interpolated into the next step.
	call := res.Jobs["main"].Steps[0]
	if call.Outputs["message"] != "hello world" {
		t.Errorf("composite output = %q, want 'hello world'", call.Outputs["message"])
	}
	if call.Uses != "setup" {
		t.Errorf("Uses = %q, want setup", call.Uses)
	}
	if len(call.Steps) != 1 || call.Steps[0].Name == "" && call.Steps[0].ID != "s" {
		t.Errorf("expected one nested step report with id s, got %+v", call.Steps)
	}
	// The downstream step saw the upstream composite's output.
	var afterSaw string
	for _, c := range rec.calls {
		if c.With["tag"] == "after" {
			afterSaw = c.With["saw"]
		}
	}
	if afterSaw != "hello world" {
		t.Errorf("after step saw %q, want 'hello world'", afterSaw)
	}
}

func TestComposite_MissingRequiredInput(t *testing.T) {
	catalog := MapResolver{
		"need": mustAction(t, `
inputs:
  must:
    required: true
runs:
  using: composite
  steps:
    - { uses: test, with: { tag: x } }
`),
	}
	r, _ := newCompositeRunner(t, catalog)
	src := `
jobs:
  main:
    steps:
      - { uses: need, with: {} }
`
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("missing required composite input should fail the step")
	}
	if !strings.Contains(res.Jobs["main"].Steps[0].Err, "must") {
		t.Errorf("err = %q, want mention of required input", res.Jobs["main"].Steps[0].Err)
	}
}

func TestComposite_InnerFailurePropagates(t *testing.T) {
	catalog := MapResolver{
		"boom": mustAction(t, `
runs:
  using: composite
  steps:
    - { uses: test, with: { tag: inner, fail: "true", msg: kaboom } }
`),
	}
	r, _ := newCompositeRunner(t, catalog)
	src := `
jobs:
  main:
    steps:
      - { id: c, uses: boom, with: {} }
`
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("inner failure should fail the composite and the run")
	}
	c := res.Jobs["main"].Steps[0]
	if c.Outcome != StatusFailure {
		t.Errorf("composite outcome = %q, want failure", c.Outcome)
	}
	if len(c.Steps) != 1 || c.Steps[0].Outcome != StatusFailure {
		t.Errorf("nested report should show the failed inner step: %+v", c.Steps)
	}
}

func TestComposite_Nested(t *testing.T) {
	catalog := MapResolver{
		"leaf": mustAction(t, `
outputs:
  v:
    value: "${{ steps.s.outputs.x }}"
runs:
  using: composite
  steps:
    - { id: s, uses: test, with: { tag: leaf, out_x: deep } }
`),
		"mid": mustAction(t, `
outputs:
  v:
    value: "${{ steps.l.outputs.v }}"
runs:
  using: composite
  steps:
    - { id: l, uses: leaf, with: {} }
`),
	}
	r, _ := newCompositeRunner(t, catalog)
	src := `
jobs:
  main:
    steps:
      - { id: m, uses: mid, with: {} }
`
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatal("nested composite should succeed")
	}
	if got := res.Jobs["main"].Steps[0].Outputs["v"]; got != "deep" {
		t.Errorf("nested output = %q, want deep", got)
	}
}

func TestComposite_CycleDetected(t *testing.T) {
	catalog := MapResolver{
		"a": mustAction(t, "runs:\n  using: composite\n  steps:\n    - { uses: b, with: {} }\n"),
		"b": mustAction(t, "runs:\n  using: composite\n  steps:\n    - { uses: a, with: {} }\n"),
	}
	r, _ := newCompositeRunner(t, catalog)
	src := `
jobs:
  main:
    steps:
      - { uses: a, with: {} }
`
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("cyclic composite actions should fail")
	}
	if !strings.Contains(collectErrs(res.Jobs["main"].Steps), "cycle") {
		t.Errorf("no nested step reported a cycle error: %s", collectErrs(res.Jobs["main"].Steps))
	}
}

func TestComposite_DepthLimit(t *testing.T) {
	// A self-... well, two actions can't cycle past the guard; build a chain
	// longer than the configured depth instead.
	catalog := MapResolver{}
	// chain: a0 -> a1 -> ... -> a5, with maxDepth=3 so it must abort.
	for i := range 5 {
		next := "a" + string(rune('0'+i+1))
		catalog["a"+string(rune('0'+i))] = mustAction(t,
			"runs:\n  using: composite\n  steps:\n    - { uses: "+next+", with: {} }\n")
	}
	catalog["a5"] = mustAction(t, "runs:\n  using: composite\n  steps:\n    - { uses: test, with: { tag: leaf } }\n")

	rec := &recordAction{}
	reg := action.DefaultRegistry()
	reg.Register(rec)
	r := New(Options{System: system.NewFake(), Registry: reg, Resolver: catalog, MaxActionDepth: 3})

	src := "jobs:\n  main:\n    steps:\n      - { uses: a0, with: {} }\n"
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("chain deeper than MaxActionDepth should fail")
	}
	// The depth error surfaces at the deepest nested step; the wrappers above it
	// report a generic composite failure. Walk the tree to find it.
	if !strings.Contains(collectErrs(res.Jobs["main"].Steps), "depth") {
		t.Errorf("no nested step reported a depth error: %s", collectErrs(res.Jobs["main"].Steps))
	}
}

// collectErrs concatenates the Err of a step tree (including nested composite
// steps) for assertions.
func collectErrs(steps []StepReport) string {
	var b strings.Builder
	var walk func([]StepReport)
	walk = func(ss []StepReport) {
		for _, s := range ss {
			if s.Err != "" {
				b.WriteString(s.Err)
				b.WriteByte('\n')
			}
			walk(s.Steps)
		}
	}
	walk(steps)
	return b.String()
}

func TestComposite_UnknownRefFailsStep(t *testing.T) {
	r, _ := newCompositeRunner(t, MapResolver{})
	src := "jobs:\n  main:\n    steps:\n      - { uses: ghost, with: {} }\n"
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("unknown action ref should fail")
	}
	if !strings.Contains(res.Jobs["main"].Steps[0].Err, "ghost") {
		t.Errorf("err = %q", res.Jobs["main"].Steps[0].Err)
	}
}

func TestComposite_SecretMaskedInOutputs(t *testing.T) {
	catalog := MapResolver{
		"emit": mustAction(t, `
outputs:
  leaked:
    value: "token ${{ steps.s.outputs.v }}"
runs:
  using: composite
  steps:
    - { id: s, uses: test, with: { tag: e, out_v: SUPERSECRET } }
`),
	}
	r, _ := newCompositeRunner(t, catalog, "SUPERSECRET")
	src := "jobs:\n  main:\n    steps:\n      - { id: c, uses: emit, with: {} }\n"
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if strings.Contains(res.Jobs["main"].Steps[0].Outputs["leaked"], "SUPERSECRET") {
		t.Errorf("secret leaked through composite output: %q", res.Jobs["main"].Steps[0].Outputs["leaked"])
	}
}

func TestMapResolver(t *testing.T) {
	def := mustAction(t, "runs:\n  using: composite\n  steps: [{run: echo hi}]\n")
	m := MapResolver{"x": def}
	if _, err := m.Resolve(context.Background(), "x"); err != nil {
		t.Errorf("Resolve(x): %v", err)
	}
	_, err := m.Resolve(context.Background(), "missing")
	var nf *ErrActionNotFound
	if err == nil || !errors.As(err, &nf) {
		t.Errorf("Resolve(missing) err = %v, want ErrActionNotFound", err)
	}
}

func TestDirResolver(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "greet"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "runs:\n  using: composite\n  steps:\n    - { uses: test, with: { tag: dir } }\n"
	if err := os.WriteFile(filepath.Join(root, "greet", "action.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// Also a flat <ref>.yaml form.
	if err := os.WriteFile(filepath.Join(root, "flat.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	d := DirResolver{Root: root}
	if _, err := d.Resolve(context.Background(), "greet"); err != nil {
		t.Errorf("Resolve(greet): %v", err)
	}
	if _, err := d.Resolve(context.Background(), "flat"); err != nil {
		t.Errorf("Resolve(flat): %v", err)
	}
	if _, err := d.Resolve(context.Background(), "nope"); err == nil {
		t.Error("Resolve(nope) should error")
	}
}

func TestDirResolver_RejectsTraversal(t *testing.T) {
	// A reference must not escape the action root.
	d := DirResolver{Root: t.TempDir()}
	if _, err := d.Resolve(context.Background(), "../../etc/passwd"); err == nil {
		t.Error("DirResolver should reject a traversal reference")
	}
}

func TestDirResolverEndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "say.yaml"),
		[]byte("runs:\n  using: composite\n  steps:\n    - { run: \"echo from-dir\", shell: sh }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := system.NewFake()
	var gotCmd string
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		gotCmd = req.Name
		return system.ExecResult{ExitCode: 0}, nil
	}
	r := New(Options{System: f, Resolver: DirResolver{Root: root}})
	src := "jobs:\n  main:\n    steps:\n      - { uses: say, with: {} }\n"
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success: %+v", res.Jobs["main"].Steps)
	}
	if gotCmd != "echo from-dir" {
		t.Errorf("command = %q, want 'echo from-dir'", gotCmd)
	}
}
