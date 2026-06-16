package action

import (
	"context"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-actions/system"
)

// aptConffileFlags is the joined form of the dpkg conffile options the package
// action appends to apt install/upgrade commands (keep current config files,
// never prompt). Kept in sync with action.aptConffileOpts.
const aptConffileFlags = "-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"

func TestParseUpgrade(t *testing.T) {
	cases := map[string]upgradeMode{
		"":      upgradeNone,
		"false": upgradeNone,
		"no":    upgradeNone,
		"true":  upgradeSafe,
		"yes":   upgradeSafe,
		"safe":  upgradeSafe,
		"full":  upgradeFull,
		"dist":  upgradeFull,
		"FULL":  upgradeFull,
	}
	for in, want := range cases {
		got, err := parseUpgrade(in)
		if err != nil {
			t.Errorf("parseUpgrade(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseUpgrade(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := parseUpgrade("bogus"); err == nil {
		t.Error("parseUpgrade(bogus): expected error")
	}
}

// runPackage runs the package action capturing the single exec call.
func runPackage(t *testing.T, f *system.Fake, with map[string]string) (system.ExecRequest, Result, error) {
	t.Helper()
	var got system.ExecRequest
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		got = req
		return system.ExecResult{ExitCode: 0}, nil
	}
	res, err := (&Package{}).Run(context.Background(), Input{With: with, System: f})
	return got, res, err
}

func TestPackage_PrefersAptOverAptGet(t *testing.T) {
	// Both binaries present → the modern `apt` must win.
	f := system.NewFake().AddPath("apt").AddPath("apt-get")
	got, res, err := runPackage(t, f, map[string]string{"name": "nginx"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Name != "apt" {
		t.Errorf("bin = %q, want apt", got.Name)
	}
	if res.Outputs["manager"] != "apt" {
		t.Errorf("manager = %q, want apt", res.Outputs["manager"])
	}
}

func TestPackage_FallsBackToAptGet(t *testing.T) {
	// Only apt-get present → use it.
	f := system.NewFake().AddPath("apt-get")
	got, _, err := runPackage(t, f, map[string]string{"name": "nginx"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Name != "apt-get" {
		t.Errorf("bin = %q, want apt-get", got.Name)
	}
}

func TestPackage_UpgradeSafe(t *testing.T) {
	f := system.NewFake().AddPath("apt").AddPath("apt-get")
	got, res, err := runPackage(t, f, map[string]string{"upgrade": "true"})
	if err != nil {
		t.Fatalf("Run (no name needed for upgrade): %v", err)
	}
	if got.Name != "apt" || strings.Join(got.Args, " ") != "upgrade -y "+aptConffileFlags {
		t.Errorf("cmd = %q %v, want apt upgrade -y (+ conffile opts)", got.Name, got.Args)
	}
	if res.Outputs["upgrade"] != "safe" {
		t.Errorf("upgrade output = %q, want safe", res.Outputs["upgrade"])
	}
}

func TestPackage_UpgradeFull_Apt(t *testing.T) {
	f := system.NewFake().AddPath("apt").AddPath("apt-get")
	got, _, err := runPackage(t, f, map[string]string{"upgrade": "full"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Name != "apt" || strings.Join(got.Args, " ") != "full-upgrade -y "+aptConffileFlags {
		t.Errorf("cmd = %q %v, want apt full-upgrade -y (+ conffile opts)", got.Name, got.Args)
	}
}

func TestPackage_UpgradeFull_AptGet(t *testing.T) {
	// Only apt-get → dist-upgrade is the equivalent spelling.
	f := system.NewFake().AddPath("apt-get")
	got, _, err := runPackage(t, f, map[string]string{"upgrade": "dist"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Name != "apt-get" || strings.Join(got.Args, " ") != "dist-upgrade -y "+aptConffileFlags {
		t.Errorf("cmd = %q %v, want apt-get dist-upgrade -y (+ conffile opts)", got.Name, got.Args)
	}
}

func TestPackage_UpdateThenUpgrade(t *testing.T) {
	// update_cache + upgrade == `apt update && apt upgrade -y`.
	f := system.NewFake().AddPath("apt").AddPath("apt-get")
	var calls []string
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		calls = append(calls, strings.TrimSpace(req.Name+" "+strings.Join(req.Args, " ")))
		return system.ExecResult{ExitCode: 0}, nil
	}
	_, err := (&Package{}).Run(context.Background(), Input{
		With:   map[string]string{"update_cache": "true", "upgrade": "true"},
		System: f,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"apt update", "apt upgrade -y " + aptConffileFlags}
	if len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

func TestPackage_UpgradeManagerVariants(t *testing.T) {
	cases := []struct {
		bin  string
		want string
	}{
		{"dnf", "dnf upgrade -y"},
		{"yum", "yum upgrade -y"},
		{"apk", "apk upgrade"},
		{"pacman", "pacman -Su --noconfirm"},
	}
	for _, tc := range cases {
		t.Run(tc.bin, func(t *testing.T) {
			f := system.NewFake().AddPath(tc.bin)
			got, _, err := runPackage(t, f, map[string]string{"upgrade": "true"})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if strings.TrimSpace(got.Name+" "+strings.Join(got.Args, " ")) != tc.want {
				t.Errorf("cmd = %q %v, want %q", got.Name, got.Args, tc.want)
			}
		})
	}
}

func TestPackage_NoNameNoUpgradeErrors(t *testing.T) {
	f := system.NewFake().AddPath("apt-get")
	_, _, err := runPackage(t, f, map[string]string{})
	if err == nil {
		t.Error("expected error when neither name nor upgrade is given")
	}
}
