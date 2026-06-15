package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-actions/system"
	"github.com/go-tangra/go-tangra-actions/workflow"
)

// serviceHealthRuntime emulates examples/actions/service-health/index.js without
// a real JS engine: it performs the same host calls the script makes, so the
// guard test below exercises the full path (DirResolver loads the package, the
// engine reads main, passes inputs, runs via the runtime against the sandboxed
// host, and propagates outputs).
type serviceHealthRuntime struct{}

func (serviceHealthRuntime) Supports(using string) bool { return using == "javascript" }

func (serviceHealthRuntime) Run(ctx context.Context, inv ScriptInvocation) (ScriptResult, error) {
	if !strings.Contains(inv.Source, "service-health") && !strings.Contains(inv.Source, "is-active") {
		// Sanity: we were handed the real package's main source.
		return ScriptResult{}, nil
	}
	svc := inv.Inputs["service"]
	inv.Host.Log("checking service: " + svc)
	res, err := inv.Host.Exec(ctx, system.ExecRequest{Name: "systemctl", Args: []string{"is-active", svc}})
	if err != nil {
		return ScriptResult{}, err
	}
	state := strings.TrimSpace(res.Stdout)
	if state == "" {
		state = "unknown"
	}
	active := "false"
	if res.ExitCode == 0 {
		active = "true"
	}
	if rp := inv.Inputs["report_path"]; rp != "" {
		if err := inv.Host.WriteFile(rp, []byte(svc+" "+state+"\n"), 0o644); err != nil {
			return ScriptResult{}, err
		}
	}
	return ScriptResult{Outputs: map[string]string{"state": state, "active": active}}, nil
}

// TestExampleWorkflowRuns guards the shipped example: it must parse, validate
// and execute end-to-end (against a fake host with apt + systemctl available)
// with every job succeeding.
func TestExampleWorkflowRuns(t *testing.T) {
	data, err := os.ReadFile("../examples/provision-web.yaml")
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	wf, err := workflow.Parse(data)
	if err != nil {
		t.Fatalf("parse example: %v", err)
	}

	f := system.NewFake().AddPath("apt-get").AddPath("timedatectl")
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		return system.ExecResult{ExitCode: 0}, nil
	}

	r := New(Options{System: f, Resolver: DirResolver{Root: "../examples/actions"}})
	res, err := r.Run(context.Background(), wf, map[string]string{"domain": "test.local"})
	if err != nil {
		t.Fatalf("run example: %v", err)
	}
	if !res.Success {
		for id, jr := range res.Jobs {
			for _, s := range jr.Steps {
				if s.Err != "" {
					t.Logf("job %s step %q failed: %s", id, s.Name, s.Err)
				}
			}
		}
		t.Fatal("example workflow did not succeed")
	}

	// The vhost file was written to the fake FS.
	if fi, _ := f.Stat("/etc/nginx/conf.d/site.conf"); !fi.Exists {
		t.Error("vhost config not written")
	}
	// All jobs ran to success.
	for _, id := range []string{"prepare", "packages", "configure", "notify"} {
		if res.Jobs[id].Result != StatusSuccess {
			t.Errorf("job %s result = %q, want success", id, res.Jobs[id].Result)
		}
	}
}

// TestExampleCompositeActionRuns guards the shipped composite action example:
// it must load via DirResolver and run end-to-end, publishing its output.
func TestExampleCompositeActionRuns(t *testing.T) {
	f := system.NewFake()
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		return system.ExecResult{ExitCode: 0}, nil
	}
	r := New(Options{System: f, Resolver: DirResolver{Root: "../examples/actions"}})

	src := `
jobs:
  web:
    steps:
      - id: vhost
        uses: nginx-vhost
        with: { domain: test.local }
`
	res, err := r.Run(context.Background(), mustParse(t, src), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Success {
		t.Fatalf("composite example did not succeed: %s", collectErrs(res.Jobs["web"].Steps))
	}
	got := res.Jobs["web"].Steps[0].Outputs["conf_path"]
	if got != "/etc/nginx/conf.d/test.local.conf" {
		t.Errorf("conf_path output = %q", got)
	}
	if fi, _ := f.Stat("/etc/nginx/conf.d/test.local.conf"); !fi.Exists {
		t.Error("vhost file not written by composite action")
	}
}

// TestExampleScriptedActionRuns guards the shipped JavaScript action example:
// its package loads via DirResolver and runs end-to-end through a ScriptRuntime,
// publishing outputs that the example workflow branches on.
func TestExampleScriptedActionRuns(t *testing.T) {
	f := system.NewFake()
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		// `systemctl is-active nginx` -> active.
		if req.Name == "systemctl" && len(req.Args) == 2 && req.Args[0] == "is-active" {
			return system.ExecResult{Stdout: "active\n", ExitCode: 0}, nil
		}
		return system.ExecResult{ExitCode: 0}, nil
	}

	data, err := os.ReadFile("../examples/healthcheck.yaml")
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	wf, err := workflow.Parse(data)
	if err != nil {
		t.Fatalf("parse example: %v", err)
	}

	r := New(Options{
		System:        f,
		Resolver:      DirResolver{Root: "../examples/actions"},
		ScriptRuntime: serviceHealthRuntime{},
	})
	res, err := r.Run(context.Background(), wf, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Success {
		t.Fatalf("healthcheck example did not succeed: %s", collectErrs(res.Jobs["check"].Steps))
	}

	health := res.Jobs["check"].Steps[0]
	if health.Outputs["active"] != "true" || health.Outputs["state"] != "active" {
		t.Errorf("scripted action outputs = %+v", health.Outputs)
	}
	// Service is active, so the conditional "Restart if down" step is skipped.
	if res.Jobs["check"].Steps[1].Outcome != StatusSkipped {
		t.Errorf("restart step = %q, want skipped (service is active)", res.Jobs["check"].Steps[1].Outcome)
	}
	// The script wrote its confined report.
	if fi, _ := f.Stat("/var/run/nginx.health"); !fi.Exists {
		t.Errorf("report file not written; files=%v", f.Filenames())
	}
}
