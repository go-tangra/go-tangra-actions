package action

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-actions/system"
)

func TestRegistry(t *testing.T) {
	r := DefaultRegistry()
	for _, name := range []string{"run", "package", "file", "file_line", "service", "service_status", "log", "hostname", "timezone"} {
		if _, ok := r.Get(name); !ok {
			t.Errorf("default registry missing %q", name)
		}
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("unexpected action found")
	}
	got := strings.Join(r.Names(), ",")
	if got != "file,file_line,hostname,log,package,run,service,service_status,timezone" {
		t.Errorf("Names() = %q", got)
	}
}

func TestArgsHelpers(t *testing.T) {
	a := args{"k": "  v  ", "b": "yes", "empty": ""}
	if a.str("k") != "v" {
		t.Errorf("str trim failed: %q", a.str("k"))
	}
	if _, err := a.required("missing"); err == nil {
		t.Error("required should error on missing")
	}
	if a.withDefault("missing", "def") != "def" {
		t.Error("withDefault fallback failed")
	}
	if v, _ := a.boolValue("b", false); !v {
		t.Error("boolValue yes should be true")
	}
	if v, _ := a.boolValue("empty", true); !v {
		t.Error("boolValue empty should use default")
	}
	if _, err := (args{"x": "maybe"}).boolValue("x", false); err == nil {
		t.Error("boolValue should reject non-boolean")
	}
}

func TestParseMode(t *testing.T) {
	v, ok, err := parseMode("0644")
	if err != nil || !ok || v != 0o644 {
		t.Errorf("parseMode(0644) = %o, %v, %v", v, ok, err)
	}
	if _, ok, _ := parseMode(""); ok {
		t.Error("empty mode should be ok=false")
	}
	if _, _, err := parseMode("999"); err == nil {
		t.Error("invalid octal should error")
	}
	if _, _, err := parseMode("0xff"); err == nil {
		t.Error("hex should error")
	}
}

func TestSplitList(t *testing.T) {
	got := splitList("a, b\nc\td  ,,e")
	want := []string{"a", "b", "c", "d", "e"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("splitList = %v, want %v", got, want)
	}
}

func TestRun_Success(t *testing.T) {
	f := system.NewFake()
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		if req.Shell != "bash" {
			t.Errorf("shell = %q, want bash", req.Shell)
		}
		if req.Name != "echo hi" {
			t.Errorf("command = %q", req.Name)
		}
		return system.ExecResult{Stdout: "hi\n", ExitCode: 0}, nil
	}
	res, err := (&Run{}).Run(context.Background(), Input{
		With:   map[string]string{"command": "echo hi", "shell": "bash"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Changed || res.Outputs["stdout"] != "hi\n" || res.Outputs["exit_code"] != "0" {
		t.Errorf("result = %+v", res)
	}
}

func TestRun_NonZeroExitIsError(t *testing.T) {
	f := system.NewFake()
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		return system.ExecResult{Stdout: "", Stderr: "boom", ExitCode: 2}, nil
	}
	res, err := (&Run{}).Run(context.Background(), Input{
		With:   map[string]string{"command": "false"},
		System: f,
	})
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if res.Outputs["exit_code"] != "2" {
		t.Errorf("exit_code output = %q", res.Outputs["exit_code"])
	}
}

func TestRun_MissingCommand(t *testing.T) {
	_, err := (&Run{}).Run(context.Background(), Input{With: map[string]string{}, System: system.NewFake()})
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestRun_RejectsArbitraryShell(t *testing.T) {
	f := system.NewFake()
	executed := false
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		executed = true
		return system.ExecResult{}, nil
	}
	for _, shell := range []string{"/usr/bin/python3", "node", "/attacker/bin", "bash -p"} {
		_, err := (&Run{}).Run(context.Background(), Input{
			With:   map[string]string{"command": "echo hi", "shell": shell},
			System: f,
		})
		if err == nil {
			t.Errorf("shell %q should be rejected", shell)
		}
	}
	if executed {
		t.Error("a disallowed shell reached Exec")
	}
}

func TestRun_WorkdirConfinement(t *testing.T) {
	f := system.NewFake()
	executed := false
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		executed = true
		return system.ExecResult{}, nil
	}
	_, err := (&Run{}).Run(context.Background(), Input{
		With:        map[string]string{"command": "ls", "workdir": "../../etc"},
		System:      f,
		ConfineRoot: "/srv/ws",
	})
	if err == nil {
		t.Fatal("workdir escaping the confine root should be rejected")
	}
	if executed {
		t.Error("command ran despite an out-of-bounds workdir")
	}
}

func TestPackage_AptInstall(t *testing.T) {
	f := system.NewFake().AddPath("apt-get")
	var got system.ExecRequest
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		got = req
		return system.ExecResult{ExitCode: 0}, nil
	}
	res, err := (&Package{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "nginx curl", "state": "present"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Name != "apt-get" {
		t.Errorf("bin = %q, want apt-get", got.Name)
	}
	wantArgs := "install -y --no-install-recommends nginx curl"
	if strings.Join(got.Args, " ") != wantArgs {
		t.Errorf("args = %v, want %q", got.Args, wantArgs)
	}
	if !slices.Contains(got.Env, "DEBIAN_FRONTEND=noninteractive") {
		t.Errorf("env missing DEBIAN_FRONTEND: %v", got.Env)
	}
	if !res.Changed || res.Outputs["manager"] != "apt" {
		t.Errorf("result = %+v", res)
	}
}

func TestPackage_UpdateCacheRefreshesFirst(t *testing.T) {
	tests := []struct {
		mgrBin      string
		wantRefresh string // bin + args of the refresh command
	}{
		{"apt-get", "apt-get update"},
		{"dnf", "dnf makecache"},
		{"yum", "yum makecache"},
		{"apk", "apk update"},
		{"pacman", "pacman -Sy --noconfirm"},
	}
	for _, tt := range tests {
		t.Run(tt.mgrBin, func(t *testing.T) {
			f := system.NewFake().AddPath(tt.mgrBin)
			var calls []string
			f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
				calls = append(calls, strings.TrimSpace(req.Name+" "+strings.Join(req.Args, " ")))
				return system.ExecResult{ExitCode: 0}, nil
			}
			res, err := (&Package{}).Run(context.Background(), Input{
				With:   map[string]string{"name": "nginx", "update_cache": "true"},
				System: f,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(calls) != 2 {
				t.Fatalf("expected 2 exec calls (refresh then install), got %v", calls)
			}
			if calls[0] != tt.wantRefresh {
				t.Errorf("refresh = %q, want %q", calls[0], tt.wantRefresh)
			}
			if !strings.Contains(calls[1], "nginx") {
				t.Errorf("install call %q should reference the package", calls[1])
			}
			if res.Outputs["cache_updated"] != "true" {
				t.Errorf("cache_updated = %q, want true", res.Outputs["cache_updated"])
			}
		})
	}
}

func TestPackage_UpdateCacheDefaultsOff(t *testing.T) {
	f := system.NewFake().AddPath("apt-get")
	var calls int
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		calls++
		return system.ExecResult{ExitCode: 0}, nil
	}
	res, err := (&Package{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "nginx"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected a single exec (no refresh), got %d", calls)
	}
	if res.Outputs["cache_updated"] != "false" {
		t.Errorf("cache_updated = %q, want false", res.Outputs["cache_updated"])
	}
}

func TestPackage_UpdateCacheFailureAbortsInstall(t *testing.T) {
	f := system.NewFake().AddPath("apt-get")
	var calls []string
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		calls = append(calls, req.Name+" "+strings.Join(req.Args, " "))
		if slices.Contains(req.Args, "update") {
			return system.ExecResult{ExitCode: 1, Stderr: "could not resolve mirror"}, nil
		}
		return system.ExecResult{ExitCode: 0}, nil
	}
	_, err := (&Package{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "nginx", "update_cache": "yes"},
		System: f,
	})
	if err == nil {
		t.Fatal("a failed cache refresh should fail the action")
	}
	if len(calls) != 1 {
		t.Errorf("install must not run after a failed refresh; calls = %v", calls)
	}
}

func TestPackage_ManagerVariants(t *testing.T) {
	tests := []struct {
		mgrBin   string
		state    string
		wantBin  string
		wantArgs string
	}{
		{"dnf", "present", "dnf", "install -y zsh"},
		{"dnf", "latest", "dnf", "upgrade -y zsh"},
		{"dnf", "absent", "dnf", "remove -y zsh"},
		{"apk", "present", "apk", "add zsh"},
		{"apk", "latest", "apk", "add -u zsh"},
		{"pacman", "present", "pacman", "-S --noconfirm --needed zsh"},
		{"pacman", "absent", "pacman", "-R --noconfirm zsh"},
	}
	for _, tt := range tests {
		t.Run(tt.mgrBin+"/"+tt.state, func(t *testing.T) {
			f := system.NewFake().AddPath(tt.mgrBin)
			var got system.ExecRequest
			f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
				got = req
				return system.ExecResult{ExitCode: 0}, nil
			}
			_, err := (&Package{}).Run(context.Background(), Input{
				With:   map[string]string{"name": "zsh", "state": tt.state},
				System: f,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got.Name != tt.wantBin || strings.Join(got.Args, " ") != tt.wantArgs {
				t.Errorf("got %s %v, want %s %q", got.Name, got.Args, tt.wantBin, tt.wantArgs)
			}
		})
	}
}

func TestPackage_RejectsMaliciousNames(t *testing.T) {
	f := system.NewFake().AddPath("apt-get")
	executed := false
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		executed = true
		return system.ExecResult{}, nil
	}
	// Each token (after comma/space/newline splitting) must be a valid package
	// name; any shell metacharacter or leading dash is rejected before exec.
	bad := []string{"nginx; rm -rf /", "--force-yes", "-rf", "$(reboot)", "foo`id`", "a|b", "a&b"}
	for _, name := range bad {
		_, err := (&Package{}).Run(context.Background(), Input{
			With:   map[string]string{"name": name},
			System: f,
		})
		if err == nil {
			t.Errorf("Package with name %q should be rejected", name)
		}
	}
	if executed {
		t.Error("a malicious package name reached Exec — validation failed")
	}
}

func TestPackage_NoManagerDetected(t *testing.T) {
	f := system.NewFake() // no package manager on PATH
	_, err := (&Package{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "nginx"},
		System: f,
	})
	if err == nil || !strings.Contains(err.Error(), "no supported package manager") {
		t.Errorf("err = %v, want no-manager error", err)
	}
}

func TestPackage_RejectsArbitraryManager(t *testing.T) {
	f := system.NewFake()
	executed := false
	f.ExecFunc = func(_ context.Context, _ system.ExecRequest) (system.ExecResult, error) {
		executed = true
		return system.ExecResult{}, nil
	}
	for _, mgr := range []string{"/attacker/bin", "reboot", "apt-get; rm -rf /"} {
		_, err := (&Package{}).Run(context.Background(), Input{
			With:   map[string]string{"name": "nginx", "manager": mgr},
			System: f,
		})
		if err == nil {
			t.Errorf("manager %q should be rejected", mgr)
		}
	}
	if executed {
		t.Error("an arbitrary manager binary reached Exec")
	}
}

func TestPackage_ExplicitManagerOverride(t *testing.T) {
	f := system.NewFake() // nothing on PATH, but manager is forced
	var got system.ExecRequest
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		got = req
		return system.ExecResult{}, nil
	}
	_, err := (&Package{}).Run(context.Background(), Input{
		With:   map[string]string{"name": "vim", "manager": "dnf"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Name != "dnf" {
		t.Errorf("bin = %q, want dnf", got.Name)
	}
}
