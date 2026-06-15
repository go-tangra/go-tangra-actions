package jsruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-actions/engine"
	"github.com/go-tangra/go-tangra-actions/system"
)

// fakeHost records host calls and serves canned exec results.
type fakeHost struct {
	execFn   func(system.ExecRequest) (system.ExecResult, error)
	written  map[string]string
	writeErr error
	readData map[string]string
	logLines []string
}

func newFakeHost() *fakeHost {
	return &fakeHost{written: map[string]string{}, readData: map[string]string{}}
}

func (h *fakeHost) Exec(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
	if h.execFn != nil {
		return h.execFn(req)
	}
	return system.ExecResult{}, nil
}
func (h *fakeHost) ReadFile(path string) ([]byte, error) {
	if v, ok := h.readData[path]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("not found")
}
func (h *fakeHost) WriteFile(path string, data []byte, _ uint32) error {
	if h.writeErr != nil {
		return h.writeErr
	}
	h.written[path] = string(data)
	return nil
}
func (h *fakeHost) Log(line string) { h.logLines = append(h.logLines, line) }

func run(t *testing.T, src string, inv engine.ScriptInvocation) (engine.ScriptResult, error) {
	t.Helper()
	inv.Source = src
	if inv.Main == "" {
		inv.Main = "index.js"
	}
	return New().Run(context.Background(), inv)
}

func TestSupports(t *testing.T) {
	rt := New()
	for _, u := range []string{"javascript", "js", "node", "node20"} {
		if !rt.Supports(u) {
			t.Errorf("Supports(%q) = false", u)
		}
	}
	for _, u := range []string{"composite", "lua", "python", ""} {
		if rt.Supports(u) {
			t.Errorf("Supports(%q) = true", u)
		}
	}
}

func TestRun_InputsOutputsLog(t *testing.T) {
	host := newFakeHost()
	res, err := run(t, `
		const who = tangra.getInput("who");
		tangra.log("hi " + who);
		tangra.setOutput("greeting", "hello " + who);
	`, engine.ScriptInvocation{
		Inputs: map[string]string{"who": "world"},
		Host:   host,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outputs["greeting"] != "hello world" {
		t.Errorf("outputs = %v", res.Outputs)
	}
	if len(host.logLines) != 1 || host.logLines[0] != "hi world" {
		t.Errorf("logs = %v", host.logLines)
	}
}

func TestRun_Exec(t *testing.T) {
	host := newFakeHost()
	host.execFn = func(req system.ExecRequest) (system.ExecResult, error) {
		if req.Name != "echo" || req.Args[0] != "x" {
			t.Errorf("exec got %+v", req)
		}
		return system.ExecResult{Stdout: "x\n", ExitCode: 0}, nil
	}
	res, err := run(t, `
		const r = tangra.exec("echo", ["x"]);
		tangra.setOutput("out", r.stdout.trim());
		tangra.setOutput("code", String(r.code));
	`, engine.ScriptInvocation{Host: host})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outputs["out"] != "x" || res.Outputs["code"] != "0" {
		t.Errorf("outputs = %v", res.Outputs)
	}
}

func TestRun_WriteFile(t *testing.T) {
	host := newFakeHost()
	_, err := run(t, `tangra.writeFile("/etc/app.conf", "k=v", "0600");`,
		engine.ScriptInvocation{Host: host})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if host.written["/etc/app.conf"] != "k=v" {
		t.Errorf("written = %v", host.written)
	}
}

func TestRun_WriteFileErrorPropagates(t *testing.T) {
	host := newFakeHost()
	host.writeErr = errors.New("path escapes confinement root")
	_, err := run(t, `tangra.writeFile("../escape", "x");`, engine.ScriptInvocation{Host: host})
	if err == nil {
		t.Fatal("expected error from host WriteFile to propagate as a script failure")
	}
	if !strings.Contains(err.Error(), "confinement") {
		t.Errorf("err = %v", err)
	}
}

func TestRun_CompileError(t *testing.T) {
	_, err := run(t, `this is not valid javascript ===`, engine.ScriptInvocation{Host: newFakeHost()})
	if err == nil {
		t.Fatal("expected compile error")
	}
}

func TestRun_ScriptThrowFails(t *testing.T) {
	_, err := run(t, `throw new Error("boom from js")`, engine.ScriptInvocation{Host: newFakeHost()})
	if err == nil || !strings.Contains(err.Error(), "boom from js") {
		t.Fatalf("err = %v, want script throw", err)
	}
}

func TestRun_ContextCancelInterrupts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := New().Run(ctx, engine.ScriptInvocation{
		Main:   "loop.js",
		Source: `while (true) {}`,
		Host:   newFakeHost(),
	})
	if err == nil {
		t.Fatal("expected interruption of an infinite loop under a cancelled context")
	}
}

func TestRun_EnvExposed(t *testing.T) {
	res, err := run(t, `tangra.setOutput("v", tangra.env["STAGE"]);`,
		engine.ScriptInvocation{Env: map[string]string{"STAGE": "prod"}, Host: newFakeHost()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outputs["v"] != "prod" {
		t.Errorf("env not exposed: %v", res.Outputs)
	}
}
