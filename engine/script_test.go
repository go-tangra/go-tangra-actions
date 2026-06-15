package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-tangra/go-tangra-actions/action"
	"github.com/go-tangra/go-tangra-actions/system"
	"github.com/go-tangra/go-tangra-actions/workflow"
)

// stubRuntime is a minimal ScriptRuntime for tests. It does not interpret real
// JavaScript; instead each "script" is a Go function keyed by its source, so we
// can exercise the engine wiring and the ScriptHost sandbox without pulling in a
// JS engine. This mirrors what go-tangra-client's goja-backed runtime does at
// the boundary.
type stubRuntime struct {
	langs   map[string]bool
	scripts map[string]func(inv ScriptInvocation) (ScriptResult, error)
}

func newStubRuntime() *stubRuntime {
	return &stubRuntime{
		langs:   map[string]bool{"javascript": true, "lua": true},
		scripts: map[string]func(ScriptInvocation) (ScriptResult, error){},
	}
}

func (s *stubRuntime) Supports(using string) bool { return s.langs[using] }

func (s *stubRuntime) Run(_ context.Context, inv ScriptInvocation) (ScriptResult, error) {
	fn, ok := s.scripts[strings.TrimSpace(inv.Source)]
	if !ok {
		return ScriptResult{}, fmt.Errorf("stub: no behaviour for script %q", inv.Source)
	}
	return fn(inv)
}

// scriptPkg builds a ResolvedAction for a scripted action whose main source maps
// to a registered stub behaviour.
func scriptAction(using, mainSrc string, inputs map[string]workflow.Input, outputs map[string]workflow.ActionOutput) *ResolvedAction {
	def := &workflow.ActionDef{
		Inputs:  inputs,
		Outputs: outputs,
		Runs:    workflow.Runs{Using: using, Main: "index.js"},
	}
	return &ResolvedAction{
		Def:   def,
		Files: fstest.MapFS{"index.js": &fstest.MapFile{Data: []byte(mainSrc)}},
	}
}

func TestScript_RunsWithInputsAndOutputs(t *testing.T) {
	rt := newStubRuntime()
	rt.scripts["BODY"] = func(inv ScriptInvocation) (ScriptResult, error) {
		inv.Host.Log("hello from script")
		// Echo an input back as an output.
		return ScriptResult{Outputs: map[string]string{"echoed": inv.Inputs["who"]}}, nil
	}

	catalog := MapResolver{
		"greet": scriptAction("javascript", "BODY",
			map[string]workflow.Input{"who": {Required: true}},
			map[string]workflow.ActionOutput{"echoed": {Value: "x"}}),
	}

	reg := action.DefaultRegistry()
	r := New(Options{System: system.NewFake(), Registry: reg, Resolver: catalog, ScriptRuntime: rt})

	src := `
jobs:
  main:
    steps:
      - id: g
        uses: greet
        with: { who: world }
`
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success: %s", collectErrs(res.Jobs["main"].Steps))
	}
	step := res.Jobs["main"].Steps[0]
	if step.Outputs["echoed"] != "world" {
		t.Errorf("output echoed = %q, want world", step.Outputs["echoed"])
	}
	if !strings.Contains(step.Stdout, "hello from script") {
		t.Errorf("script log not captured as stdout: %q", step.Stdout)
	}
}

func TestScript_HostExecGoesThroughSystem(t *testing.T) {
	f := system.NewFake()
	var ran system.ExecRequest
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		ran = req
		return system.ExecResult{Stdout: "ok", ExitCode: 0}, nil
	}
	rt := newStubRuntime()
	rt.scripts["EXEC"] = func(inv ScriptInvocation) (ScriptResult, error) {
		res, err := inv.Host.Exec(context.Background(), system.ExecRequest{Name: "systemctl", Args: []string{"status", "nginx"}})
		if err != nil {
			return ScriptResult{}, err
		}
		return ScriptResult{Outputs: map[string]string{"out": res.Stdout}}, nil
	}
	catalog := MapResolver{"probe": scriptAction("javascript", "EXEC", nil, nil)}

	r := New(Options{System: f, Resolver: catalog, ScriptRuntime: rt})
	src := "jobs:\n  main:\n    steps:\n      - { uses: probe, with: {} }\n"
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatal("expected success")
	}
	if ran.Name != "systemctl" || ran.Args[1] != "nginx" {
		t.Errorf("host.Exec did not route through system: %+v", ran)
	}
}

func TestScript_HostWriteFileConfined(t *testing.T) {
	f := system.NewFake()
	rt := newStubRuntime()
	rt.scripts["WRITE"] = func(inv ScriptInvocation) (ScriptResult, error) {
		// Attempt to escape the confinement root.
		return ScriptResult{}, inv.Host.WriteFile("../../etc/evil", []byte("pwned"), 0o644)
	}
	catalog := MapResolver{"w": scriptAction("javascript", "WRITE", nil, nil)}

	r := New(Options{System: f, Resolver: catalog, ScriptRuntime: rt, ConfineRoot: "/srv/ws"})
	src := "jobs:\n  main:\n    steps:\n      - { uses: w, with: {} }\n"
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("script writing outside the confine root should fail")
	}
	if len(f.Filenames()) != 0 {
		t.Errorf("script bypassed confinement: %v", f.Filenames())
	}
}

func TestScript_HostWriteFileWithinRoot(t *testing.T) {
	f := system.NewFake()
	rt := newStubRuntime()
	rt.scripts["WRITE"] = func(inv ScriptInvocation) (ScriptResult, error) {
		return ScriptResult{}, inv.Host.WriteFile("out.txt", []byte("data"), 0o644)
	}
	catalog := MapResolver{"w": scriptAction("javascript", "WRITE", nil, nil)}

	r := New(Options{System: f, Resolver: catalog, ScriptRuntime: rt, ConfineRoot: "/srv/ws"})
	src := "jobs:\n  main:\n    steps:\n      - { uses: w, with: {} }\n"
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil || !res.Success {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if fi, _ := f.Stat("/srv/ws/out.txt"); !fi.Exists {
		t.Errorf("confined write not performed; files=%v", f.Filenames())
	}
}

func TestScript_SecretMaskedInOutputAndLog(t *testing.T) {
	rt := newStubRuntime()
	rt.scripts["LEAK"] = func(inv ScriptInvocation) (ScriptResult, error) {
		inv.Host.Log("printing SUPERSECRET to log")
		return ScriptResult{Outputs: map[string]string{"v": "value=SUPERSECRET"}}, nil
	}
	catalog := MapResolver{"leak": scriptAction("javascript", "LEAK", nil, nil)}

	r := New(Options{System: system.NewFake(), Resolver: catalog, ScriptRuntime: rt, Secrets: []string{"SUPERSECRET"}})
	src := "jobs:\n  main:\n    steps:\n      - { id: s, uses: leak, with: {} }\n"
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	step := res.Jobs["main"].Steps[0]
	if strings.Contains(step.Outputs["v"], "SUPERSECRET") {
		t.Errorf("secret leaked in script output: %q", step.Outputs["v"])
	}
	if strings.Contains(step.Stdout, "SUPERSECRET") {
		t.Errorf("secret leaked in script log: %q", step.Stdout)
	}
}

func TestScript_NoRuntimeFails(t *testing.T) {
	catalog := MapResolver{"x": scriptAction("javascript", "BODY", nil, nil)}
	r := New(Options{System: system.NewFake(), Resolver: catalog}) // no ScriptRuntime
	src := "jobs:\n  main:\n    steps:\n      - { uses: x, with: {} }\n"
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("scripted action without a runtime should fail")
	}
	if !strings.Contains(res.Jobs["main"].Steps[0].Err, "no script runtime") {
		t.Errorf("err = %q", res.Jobs["main"].Steps[0].Err)
	}
}

func TestScript_UnsupportedLanguageFails(t *testing.T) {
	rt := newStubRuntime() // supports javascript/lua only
	catalog := MapResolver{"x": scriptAction("python", "BODY", nil, nil)}
	r := New(Options{System: system.NewFake(), Resolver: catalog, ScriptRuntime: rt})
	src := "jobs:\n  main:\n    steps:\n      - { uses: x, with: {} }\n"
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("unsupported script language should fail")
	}
	if !strings.Contains(res.Jobs["main"].Steps[0].Err, "using=\"python\"") {
		t.Errorf("err = %q", res.Jobs["main"].Steps[0].Err)
	}
}

func TestScript_ScriptErrorFailsStep(t *testing.T) {
	rt := newStubRuntime()
	rt.scripts["ERR"] = func(ScriptInvocation) (ScriptResult, error) {
		return ScriptResult{}, fmt.Errorf("boom in script")
	}
	catalog := MapResolver{"x": scriptAction("javascript", "ERR", nil, nil)}
	r := New(Options{System: system.NewFake(), Resolver: catalog, ScriptRuntime: rt})
	src := "jobs:\n  main:\n    steps:\n      - { uses: x, with: {} }\n"
	res, _ := r.Run(context.Background(), mustParse(t, src), nil)
	if res.Success {
		t.Error("script error should fail the step")
	}
	if !strings.Contains(res.Jobs["main"].Steps[0].Err, "boom in script") {
		t.Errorf("err = %q", res.Jobs["main"].Steps[0].Err)
	}
}
