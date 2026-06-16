package action

import (
	"context"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-actions/system"
)

func TestServiceStatus(t *testing.T) {
	cases := []struct {
		name     string
		show     string // fake `systemctl show` output
		exists   string
		running  string
		enabled  string // is_enabled output
		active   string
		unitFile string // enabled output
	}{
		{
			name:     "active and enabled",
			show:     "LoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\n",
			exists:   "true",
			running:  "true",
			enabled:  "true",
			active:   "active",
			unitFile: "enabled",
		},
		{
			name:     "installed but disabled and stopped",
			show:     "LoadState=loaded\nActiveState=inactive\nSubState=dead\nUnitFileState=disabled\n",
			exists:   "true",
			running:  "false",
			enabled:  "false",
			active:   "inactive",
			unitFile: "disabled",
		},
		{
			name:     "not installed",
			show:     "LoadState=not-found\nActiveState=inactive\nSubState=dead\nUnitFileState=\n",
			exists:   "false",
			running:  "false",
			enabled:  "false",
			active:   "inactive",
			unitFile: "",
		},
		{
			name:     "enabled-runtime counts as enabled",
			show:     "LoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled-runtime\n",
			exists:   "true",
			running:  "true",
			enabled:  "true",
			active:   "active",
			unitFile: "enabled-runtime",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := system.NewFake()
			var got system.ExecRequest
			f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
				got = req
				return system.ExecResult{Stdout: tc.show, ExitCode: 0}, nil
			}
			res, err := (&ServiceStatus{}).Run(context.Background(), Input{
				With:   map[string]string{"name": "nginx.service"},
				System: f,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got.Name != "systemctl" || got.Args[0] != "show" {
				t.Errorf("cmd = %q %v, want systemctl show ...", got.Name, got.Args)
			}
			if res.Changed {
				t.Error("service_status must not report Changed (read-only)")
			}
			checks := map[string]string{
				"exists":     tc.exists,
				"running":    tc.running,
				"is_enabled": tc.enabled,
				"active":     tc.active,
				"enabled":    tc.unitFile,
			}
			for k, want := range checks {
				if res.Outputs[k] != want {
					t.Errorf("output %q = %q, want %q", k, res.Outputs[k], want)
				}
			}
		})
	}
}

func TestServiceStatus_RejectsMaliciousName(t *testing.T) {
	f := system.NewFake()
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		t.Error("Exec must not run for an invalid unit name")
		return system.ExecResult{}, nil
	}
	_, err := (&ServiceStatus{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "nginx; rm -rf /"},
		System: f,
	})
	if err == nil || !strings.Contains(err.Error(), "service_status") {
		t.Errorf("expected validation error, got %v", err)
	}
}
