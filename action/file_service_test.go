package action

import (
	"context"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-actions/system"
)

func TestFile_CreateAndIdempotent(t *testing.T) {
	f := system.NewFake()
	in := Input{
		With:   map[string]string{"path": "/etc/app/conf", "content": "x=1", "mode": "0640"},
		System: f,
	}
	// First run creates the file.
	res, err := (&File{}).Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Changed {
		t.Error("first write should report changed")
	}
	fi, _ := f.Stat("/etc/app/conf")
	if !fi.Exists || fi.Mode != 0o640 {
		t.Errorf("stat = %+v", fi)
	}
	data, _ := f.ReadFile("/etc/app/conf")
	if string(data) != "x=1" {
		t.Errorf("content = %q", data)
	}

	// Second identical run is a no-op.
	res, err = (&File{}).Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if res.Changed {
		t.Error("identical re-run should report no change (not idempotent)")
	}
}

func TestFile_ContentChangeIsChanged(t *testing.T) {
	f := system.NewFake()
	_ = f.WriteFile("/f", []byte("old"), 0o644)
	res, err := (&File{}).Run(context.Background(), Input{
		With:   map[string]string{"path": "/f", "content": "new"},
		System: f,
	})
	if err != nil || !res.Changed {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	data, _ := f.ReadFile("/f")
	if string(data) != "new" {
		t.Errorf("content = %q", data)
	}
}

func TestFile_ModeOnlyChange(t *testing.T) {
	f := system.NewFake()
	_ = f.WriteFile("/f", []byte("same"), 0o600)
	res, err := (&File{}).Run(context.Background(), Input{
		With:   map[string]string{"path": "/f", "content": "same", "mode": "0644"},
		System: f,
	})
	if err != nil || !res.Changed {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if fi, _ := f.Stat("/f"); fi.Mode != 0o644 {
		t.Errorf("mode = %o, want 644", fi.Mode)
	}
}

func TestFile_Directory(t *testing.T) {
	f := system.NewFake()
	res, err := (&File{}).Run(context.Background(), Input{
		With:   map[string]string{"path": "/var/lib/app", "state": "directory"},
		System: f,
	})
	if err != nil || !res.Changed {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if fi, _ := f.Stat("/var/lib/app"); !fi.IsDir {
		t.Error("expected directory")
	}
	// Idempotent.
	res, _ = (&File{}).Run(context.Background(), Input{
		With:   map[string]string{"path": "/var/lib/app", "state": "directory"},
		System: f,
	})
	if res.Changed {
		t.Error("re-creating existing dir should be no change")
	}
}

func TestFile_Absent(t *testing.T) {
	f := system.NewFake()
	_ = f.WriteFile("/gone", []byte("x"), 0o644)
	res, err := (&File{}).Run(context.Background(), Input{
		With:   map[string]string{"path": "/gone", "state": "absent"},
		System: f,
	})
	if err != nil || !res.Changed {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if fi, _ := f.Stat("/gone"); fi.Exists {
		t.Error("file should be removed")
	}
	// Removing a missing file is a no-op, not an error.
	res, err = (&File{}).Run(context.Background(), Input{
		With:   map[string]string{"path": "/gone", "state": "absent"},
		System: f,
	})
	if err != nil || res.Changed {
		t.Errorf("absent on missing: res=%+v err=%v", res, err)
	}
}

func TestFile_PathConfinementBlocksTraversal(t *testing.T) {
	f := system.NewFake()
	_, err := (&File{}).Run(context.Background(), Input{
		With:        map[string]string{"path": "../../etc/passwd", "content": "pwned"},
		System:      f,
		ConfineRoot: "/srv/workspace",
	})
	if err == nil {
		t.Fatal("traversal outside confine root should be rejected")
	}
	if len(f.Filenames()) != 0 {
		t.Errorf("nothing should have been written, got %v", f.Filenames())
	}
}

func TestFile_PathConfinementAllowsInside(t *testing.T) {
	f := system.NewFake()
	res, err := (&File{}).Run(context.Background(), Input{
		With:        map[string]string{"path": "sub/conf", "content": "ok"},
		System:      f,
		ConfineRoot: "/srv/workspace",
	})
	if err != nil || !res.Changed {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if fi, _ := f.Stat("/srv/workspace/sub/conf"); !fi.Exists {
		t.Errorf("file not written to confined path; files=%v", f.Filenames())
	}
}

func TestFile_InvalidStateAndMode(t *testing.T) {
	f := system.NewFake()
	if _, err := (&File{}).Run(context.Background(), Input{
		With: map[string]string{"path": "/f", "state": "bogus"}, System: f,
	}); err == nil {
		t.Error("invalid state should error")
	}
	if _, err := (&File{}).Run(context.Background(), Input{
		With: map[string]string{"path": "/f", "mode": "99x"}, System: f,
	}); err == nil {
		t.Error("invalid mode should error")
	}
	if _, err := (&File{}).Run(context.Background(), Input{
		With: map[string]string{}, System: f,
	}); err == nil {
		t.Error("missing path should error")
	}
}

func TestService_StateVerbs(t *testing.T) {
	tests := []struct {
		state    string
		wantVerb string
	}{
		{"started", "start"},
		{"stopped", "stop"},
		{"restarted", "restart"},
		{"reloaded", "reload"},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			f := system.NewFake()
			var got system.ExecRequest
			f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
				got = req
				return system.ExecResult{ExitCode: 0}, nil
			}
			res, err := (&Service{}).Run(context.Background(), Input{
				With:   map[string]string{"name": "nginx", "state": tt.state},
				System: f,
			})
			if err != nil || !res.Changed {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			if got.Name != "systemctl" || got.Args[0] != tt.wantVerb || got.Args[1] != "nginx" {
				t.Errorf("exec = %s %v, want systemctl %s nginx", got.Name, got.Args, tt.wantVerb)
			}
		})
	}
}

func TestService_EnableThenStartOrder(t *testing.T) {
	f := system.NewFake()
	_, err := (&Service{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "nginx", "state": "started", "enabled": "true"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 systemctl calls, got %d", len(calls))
	}
	// enable must come before start so a new unit can be started.
	if calls[0].Args[0] != "enable" || calls[1].Args[0] != "start" {
		t.Errorf("order = %v then %v, want enable then start", calls[0].Args, calls[1].Args)
	}
}

func TestService_DisabledVerb(t *testing.T) {
	f := system.NewFake()
	var got system.ExecRequest
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		got = req
		return system.ExecResult{}, nil
	}
	_, err := (&Service{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "nginx", "enabled": "false"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Args[0] != "disable" {
		t.Errorf("verb = %q, want disable", got.Args[0])
	}
}

func TestService_ListAppliesToEach(t *testing.T) {
	f := system.NewFake()
	var calls []string
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		calls = append(calls, strings.Join(req.Args, " "))
		return system.ExecResult{ExitCode: 0}, nil
	}
	res, err := (&Service{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "a b", "state": "stopped"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"stop a", "stop b"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if res.Outputs["name"] != "a,b" {
		t.Errorf("name output = %q, want a,b", res.Outputs["name"])
	}
}

func TestService_IgnoreMissingSkipsAbsent(t *testing.T) {
	f := system.NewFake()
	present := map[string]bool{"apt-daily.timer": true} // zabbix-agent is absent
	var verbs []string
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		if req.Args[0] == "cat" { // existence probe
			if present[req.Args[1]] {
				return system.ExecResult{ExitCode: 0}, nil
			}
			return system.ExecResult{ExitCode: 1, Stderr: "No files found"}, nil
		}
		verbs = append(verbs, req.Args[0]+" "+req.Args[1])
		return system.ExecResult{ExitCode: 0}, nil
	}
	res, err := (&Service{}).Run(context.Background(), Input{
		With: map[string]string{
			"name":           "apt-daily.timer\nzabbix-agent",
			"state":          "stopped",
			"enabled":        "false",
			"ignore_missing": "true",
		},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Only the present unit is acted on (disable then stop); the absent one is
	// skipped, never reaching disable/stop.
	want := []string{"disable apt-daily.timer", "stop apt-daily.timer"}
	if strings.Join(verbs, ",") != strings.Join(want, ",") {
		t.Errorf("verbs = %v, want %v", verbs, want)
	}
	if res.Outputs["missing"] != "zabbix-agent" {
		t.Errorf("missing = %q, want zabbix-agent", res.Outputs["missing"])
	}
	if !res.Changed {
		t.Error("acting on the present unit should report changed")
	}
}

func TestService_AbsentUnitFailsWithoutIgnoreMissing(t *testing.T) {
	f := system.NewFake()
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		// systemctl disable on a non-existent unit fails; no ignore_missing means
		// the action must surface that error (no silent skip, no cat probe).
		if req.Args[0] == "cat" {
			t.Error("cat probe must not run without ignore_missing")
		}
		return system.ExecResult{ExitCode: 1, Stderr: "Unit file does not exist"}, nil
	}
	_, err := (&Service{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "zabbix-agent", "enabled": "false"},
		System: f,
	})
	if err == nil {
		t.Fatal("disabling an absent unit without ignore_missing should error")
	}
}

func TestService_RejectsMaliciousName(t *testing.T) {
	f := system.NewFake()
	executed := false
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		executed = true
		return system.ExecResult{}, nil
	}
	_, err := (&Service{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "nginx; reboot", "state": "started"},
		System: f,
	})
	if err == nil {
		t.Fatal("malicious service name should be rejected")
	}
	if executed {
		t.Error("malicious name reached systemctl")
	}
}

func TestService_RequiresStateOrEnabled(t *testing.T) {
	f := system.NewFake()
	if _, err := (&Service{}).Run(context.Background(), Input{
		With: map[string]string{"name": "nginx"}, System: f,
	}); err == nil {
		t.Error("service with neither state nor enabled should error")
	}
}

func TestService_NonZeroExitIsError(t *testing.T) {
	f := system.NewFake()
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		return system.ExecResult{ExitCode: 5, Stderr: "Failed"}, nil
	}
	_, err := (&Service{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "nginx", "state": "started"},
		System: f,
	})
	if err == nil {
		t.Fatal("non-zero systemctl exit should be an error")
	}
}
