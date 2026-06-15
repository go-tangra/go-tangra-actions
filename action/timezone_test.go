package action

import (
	"context"
	"testing"

	"github.com/go-tangra/go-tangra-actions/system"
)

func TestTimezone_SetsWithTimedatectl(t *testing.T) {
	f := system.NewFake().AddPath("timedatectl")
	_ = f.WriteFile(timezoneFile, []byte("Etc/UTC\n"), 0o644)
	var got system.ExecRequest
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		got = req
		return system.ExecResult{ExitCode: 0}, nil
	}
	res, err := (&Timezone{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "Europe/Sofia"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Name != "timedatectl" || got.Args[0] != "set-timezone" || got.Args[1] != "Europe/Sofia" {
		t.Errorf("exec = %s %v, want timedatectl set-timezone Europe/Sofia", got.Name, got.Args)
	}
	if !res.Changed || res.Outputs["previous"] != "Etc/UTC" || res.Outputs["timezone"] != "Europe/Sofia" {
		t.Errorf("result = %+v", res)
	}
	// /etc/timezone is persisted for idempotency.
	data, _ := f.ReadFile(timezoneFile)
	if string(data) != "Europe/Sofia\n" {
		t.Errorf("/etc/timezone = %q", data)
	}
}

func TestTimezone_IdempotentWhenSame(t *testing.T) {
	f := system.NewFake().AddPath("timedatectl")
	_ = f.WriteFile(timezoneFile, []byte("Europe/Sofia\n"), 0o644)
	executed := false
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		executed = true
		return system.ExecResult{}, nil
	}
	res, err := (&Timezone{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "Europe/Sofia"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Changed {
		t.Error("setting the already-current timezone should report no change")
	}
	if executed {
		t.Error("no command should run when the timezone already matches")
	}
}

func TestTimezone_FallbackCopiesZoneinfo(t *testing.T) {
	// timedatectl present but fails (no bus). Fallback copies the zoneinfo file
	// onto /etc/localtime and writes /etc/timezone.
	f := system.NewFake().AddPath("timedatectl")
	zone := zoneinfoDir + "/Europe/Sofia"
	_ = f.WriteFile(zone, []byte("TZif-fake-bytes"), 0o644)
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		if req.Name == "timedatectl" {
			return system.ExecResult{ExitCode: 1, Stderr: "Failed to connect to bus"}, nil
		}
		return system.ExecResult{ExitCode: 0}, nil
	}
	res, err := (&Timezone{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "Europe/Sofia"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run should fall back, not fail: %v", err)
	}
	if !res.Changed {
		t.Error("expected changed after fallback")
	}
	lt, _ := f.ReadFile(localtimeFile)
	if string(lt) != "TZif-fake-bytes" {
		t.Errorf("/etc/localtime = %q, want copied zoneinfo bytes", lt)
	}
	tz, _ := f.ReadFile(timezoneFile)
	if string(tz) != "Europe/Sofia\n" {
		t.Errorf("/etc/timezone = %q", tz)
	}
}

func TestTimezone_FallbackUnknownZoneErrors(t *testing.T) {
	// No timedatectl, and the requested zone has no zoneinfo file.
	f := system.NewFake()
	_, err := (&Timezone{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "Mars/Olympus_Mons"},
		System: f,
	})
	if err == nil {
		t.Fatal("an unknown timezone should error in the fallback path")
	}
}

func TestTimezone_RejectsInvalid(t *testing.T) {
	f := system.NewFake().AddPath("timedatectl")
	executed := false
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		executed = true
		return system.ExecResult{}, nil
	}
	// Traversal and injection attempts must be rejected before any side effect.
	for _, bad := range []string{"../../etc/shadow", "-flag", "a;b", "/etc/passwd", "..", "Europe/../etc"} {
		_, err := (&Timezone{}).Run(context.Background(), Input{
			With:   map[string]string{"name": bad},
			System: f,
		})
		if err == nil {
			t.Errorf("timezone %q should be rejected", bad)
		}
	}
	if executed {
		t.Error("an invalid timezone reached Exec — validation failed")
	}
}
