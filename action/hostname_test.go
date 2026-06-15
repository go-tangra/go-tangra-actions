package action

import (
	"context"
	"testing"

	"github.com/go-tangra/go-tangra-actions/system"
)

func TestHostname_SetsWithHostnamectl(t *testing.T) {
	f := system.NewFake().AddPath("hostnamectl")
	_ = f.WriteFile(hostnameFile, []byte("old-host\n"), 0o644)
	var got system.ExecRequest
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		got = req
		return system.ExecResult{ExitCode: 0}, nil
	}
	res, err := (&Hostname{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "web-01"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Name != "hostnamectl" || got.Args[0] != "set-hostname" || got.Args[1] != "web-01" {
		t.Errorf("exec = %s %v, want hostnamectl set-hostname web-01", got.Name, got.Args)
	}
	if !res.Changed {
		t.Error("changing the hostname should report changed")
	}
	if res.Outputs["previous"] != "old-host" || res.Outputs["hostname"] != "web-01" {
		t.Errorf("outputs = %+v", res.Outputs)
	}
}

func TestHostname_IdempotentWhenSame(t *testing.T) {
	f := system.NewFake().AddPath("hostnamectl")
	_ = f.WriteFile(hostnameFile, []byte("web-01\n"), 0o644)
	executed := false
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		executed = true
		return system.ExecResult{}, nil
	}
	res, err := (&Hostname{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "web-01"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Changed {
		t.Error("setting the already-current hostname should report no change")
	}
	if executed {
		t.Error("no command should run when the hostname already matches")
	}
}

func TestHostname_FallbackWritesFileAndSetsLive(t *testing.T) {
	f := system.NewFake() // no hostnamectl on PATH
	var got system.ExecRequest
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		got = req
		return system.ExecResult{ExitCode: 0}, nil
	}
	res, err := (&Hostname{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "web-01"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Changed {
		t.Error("expected changed")
	}
	// Persisted to /etc/hostname...
	data, _ := f.ReadFile(hostnameFile)
	if string(data) != "web-01\n" {
		t.Errorf("/etc/hostname = %q, want \"web-01\\n\"", data)
	}
	// ...and the live hostname set via the `hostname` binary.
	if got.Name != "hostname" || got.Args[0] != "web-01" {
		t.Errorf("exec = %s %v, want hostname web-01", got.Name, got.Args)
	}
}

func TestHostname_FallsBackWhenHostnamectlFails(t *testing.T) {
	// hostnamectl is on PATH but fails (e.g. no D-Bus bus on a minimal
	// container) — the action must fall back to /etc/hostname + `hostname`.
	f := system.NewFake().AddPath("hostnamectl")
	var calls []string
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		calls = append(calls, req.Name)
		if req.Name == "hostnamectl" {
			return system.ExecResult{ExitCode: 1, Stderr: "Failed to connect to bus"}, nil
		}
		return system.ExecResult{ExitCode: 0}, nil
	}
	res, err := (&Hostname{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "web-01"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run should fall back, not fail: %v", err)
	}
	if !res.Changed {
		t.Error("expected changed after fallback")
	}
	data, _ := f.ReadFile(hostnameFile)
	if string(data) != "web-01\n" {
		t.Errorf("/etc/hostname = %q, want fallback write", data)
	}
	// Tried hostnamectl first, then the hostname binary.
	if len(calls) != 2 || calls[0] != "hostnamectl" || calls[1] != "hostname" {
		t.Errorf("calls = %v, want [hostnamectl hostname]", calls)
	}
}

func TestHostname_RejectsInvalid(t *testing.T) {
	f := system.NewFake().AddPath("hostnamectl")
	executed := false
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		executed = true
		return system.ExecResult{}, nil
	}
	for _, bad := range []string{"-rf", "bad host", "a;b", "host_underscore", "exam$ple", "-flag"} {
		_, err := (&Hostname{}).Run(context.Background(), Input{
			With:   map[string]string{"name": bad},
			System: f,
		})
		if err == nil {
			t.Errorf("hostname %q should be rejected", bad)
		}
	}
	if executed {
		t.Error("an invalid hostname reached Exec — validation failed")
	}
}
