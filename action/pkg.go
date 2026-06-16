package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-tangra/go-tangra-actions/secure"
	"github.com/go-tangra/go-tangra-actions/system"
)

// Package installs, removes or upgrades operating-system packages, detecting the
// host package manager (apt, dnf, yum, apk, pacman). Package names are validated
// against a strict allowlist (secure.ValidatePackageName) before reaching the
// manager, so a value like "nginx; rm -rf /" or "--force-yes" can never be
// passed as an argument. No shell is used.
//
// On Debian/Ubuntu the modern `apt` binary is preferred over `apt-get` when it
// is present on PATH (falling back to `apt-get`).
//
// Inputs (with):
//
//	name          one or more package names (comma/space/newline separated).
//	              Required unless `upgrade` is set.
//	state         present | absent | latest  (default present)
//	manager       override auto-detection (apt|dnf|yum|apk|pacman)
//	update_cache  refresh the package index before acting (apt update,
//	              dnf/yum makecache, apk update, pacman -Sy). Default false.
//	              A freshly installed host has empty/stale lists, so an install
//	              without a refresh fails; set this true for first-run provisioning.
//	upgrade       upgrade ALL installed packages (no name needed):
//	                false (default) — no full upgrade
//	                true | yes | safe — apt upgrade / dnf upgrade / apk upgrade / pacman -Su
//	                full | dist — apt full-upgrade (apt-get dist-upgrade); same as safe elsewhere
//	              Combine with update_cache:true for the equivalent of
//	              `apt update && apt upgrade -y`.
//
// Outputs: manager, packages, state, cache_updated, upgrade.
type Package struct{}

func (*Package) Name() string { return "package" }

// pkgState is the desired end state of the named packages.
type pkgState string

const (
	statePresent pkgState = "present"
	stateAbsent  pkgState = "absent"
	stateLatest  pkgState = "latest"
)

// upgradeMode selects a full-system upgrade (all installed packages).
type upgradeMode string

const (
	upgradeNone upgradeMode = ""     // no full upgrade
	upgradeSafe upgradeMode = "safe" // conservative upgrade
	upgradeFull upgradeMode = "full" // apt full-upgrade / apt-get dist-upgrade
)

// parseUpgrade maps the `upgrade` input to an upgradeMode.
func parseUpgrade(s string) (upgradeMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false", "no", "0", "off":
		return upgradeNone, nil
	case "true", "yes", "1", "on", "safe":
		return upgradeSafe, nil
	case "full", "dist":
		return upgradeFull, nil
	default:
		return "", fmt.Errorf("invalid upgrade %q (want true/yes/safe | full/dist | false)", s)
	}
}

func (*Package) Run(ctx context.Context, in Input) (Result, error) {
	a := args(in.With)

	upgrade, err := parseUpgrade(a.str("upgrade"))
	if err != nil {
		return Result{}, fmt.Errorf("package: %w", err)
	}

	// Package names are required for install/remove, but not for a full upgrade.
	names := splitList(a.str("name"))
	if upgrade == upgradeNone && len(names) == 0 {
		return Result{}, fmt.Errorf("package: no package names provided")
	}
	for _, n := range names {
		if err := secure.ValidatePackageName(n); err != nil {
			return Result{}, fmt.Errorf("package: %w", err)
		}
	}

	state := pkgState(a.withDefault("state", string(statePresent)))
	switch state {
	case statePresent, stateAbsent, stateLatest:
	default:
		return Result{}, fmt.Errorf("package: invalid state %q (want present|absent|latest)", state)
	}

	updateCache, err := a.boolValue("update_cache", false)
	if err != nil {
		return Result{}, fmt.Errorf("package: %w", err)
	}

	mgr := a.str("manager")
	if mgr == "" {
		var ok bool
		if mgr, ok = detectManager(in.System); !ok {
			return Result{}, fmt.Errorf("package: no supported package manager found (apt/dnf/yum/apk/pacman)")
		}
	} else if !supportedManagers[mgr] {
		// A caller-supplied manager becomes the executed binary name, so it must
		// be one we know — never an arbitrary path.
		return Result{}, fmt.Errorf("package: unsupported manager %q (allowed: apt/dnf/yum/apk/pacman)", mgr)
	}

	// Prefer the modern `apt` CLI when available, else `apt-get`. Resolved once
	// and threaded through every apt invocation so refresh/install/upgrade agree.
	aptBin := aptBinary(in.System)

	// Refresh the package index first when asked. A freshly installed host has
	// empty/stale lists, so `state: present` would otherwise fail (e.g. apt
	// exit 100). Run it as its own command before the install/upgrade.
	if updateCache {
		rbin, rargs, renv := packageRefreshCommand(mgr, aptBin)
		rres, rerr := in.System.Exec(ctx, system.ExecRequest{
			Name: rbin,
			Args: rargs,
			Env:  append(append([]string{}, in.Env...), renv...),
		})
		if rerr != nil {
			return Result{Stdout: rres.Stdout, Stderr: rres.Stderr}, fmt.Errorf("package: exec %s: %w", rbin, rerr)
		}
		if rres.ExitCode != 0 {
			return Result{Stdout: rres.Stdout, Stderr: rres.Stderr}, fmt.Errorf("package: %s exited with code %d", rbin, rres.ExitCode)
		}
	}

	// Build the main command: a full-system upgrade, or an install/remove of
	// the named packages.
	var (
		bin     string
		cmdArgs []string
		env     []string
	)
	if upgrade != upgradeNone {
		bin, cmdArgs, env, err = packageUpgradeCommand(mgr, upgrade, aptBin)
	} else {
		bin, cmdArgs, env, err = packageCommand(mgr, state, names, aptBin)
	}
	if err != nil {
		return Result{}, err
	}

	res, err := in.System.Exec(ctx, system.ExecRequest{
		Name: bin,
		Args: cmdArgs,
		Env:  append(append([]string{}, in.Env...), env...),
	})
	result := Result{
		Stdout: res.Stdout,
		Stderr: res.Stderr,
		Outputs: map[string]string{
			"manager":       mgr,
			"packages":      strings.Join(names, ","),
			"state":         string(state),
			"cache_updated": fmt.Sprintf("%t", updateCache),
			"upgrade":       string(upgrade),
		},
	}
	if err != nil {
		return result, fmt.Errorf("package: exec %s: %w", bin, err)
	}
	if res.ExitCode != 0 {
		return result, fmt.Errorf("package: %s exited with code %d", bin, res.ExitCode)
	}
	result.Changed = true
	return result, nil
}

// aptConffileOpts stop dpkg from prompting on a modified config file during a
// non-interactive run. DEBIAN_FRONTEND=noninteractive alone does NOT decide
// conffile conflicts: when a package ships a new version of a config file the
// admin (or a script) has changed, dpkg still asks "install maintainer's
// version / keep yours?" and, with no TTY attached, fails with "end of file on
// stdin at conffile prompt". These options make the choice automatically — keep
// the currently-installed file, taking the package default only where the admin
// never touched it — which is the safe, unattended-upgrade behaviour.
var aptConffileOpts = []string{
	"-o", "Dpkg::Options::=--force-confdef",
	"-o", "Dpkg::Options::=--force-confold",
}

// supportedManagers is the allowlist of package-manager names a caller may
// force via the `manager` input. The value maps to a fixed binary in
// packageCommand, so anything outside this set is rejected.
var supportedManagers = map[string]bool{
	"apt": true, "dnf": true, "yum": true, "apk": true, "pacman": true,
}

// detectManager finds the first supported package manager on PATH. Both `apt`
// and `apt-get` map to the logical "apt" manager.
func detectManager(sys system.System) (string, bool) {
	for _, m := range []string{"apt", "apt-get", "dnf", "yum", "apk", "pacman"} {
		if _, ok := sys.LookPath(m); ok {
			if m == "apt" || m == "apt-get" {
				return "apt", true
			}
			return m, true
		}
	}
	return "", false
}

// aptBinary returns the apt binary to invoke: the modern `apt` CLI when present
// on PATH, otherwise the scripting-stable `apt-get`.
func aptBinary(sys system.System) string {
	if _, ok := sys.LookPath("apt"); ok {
		return "apt"
	}
	return "apt-get"
}

// packageCommand returns the binary, arguments and extra environment for the
// given manager/state/names. Every flag is fixed and non-interactive; the only
// variable tokens are the validated package names appended at the end.
func packageCommand(mgr string, state pkgState, names []string, aptBin string) (bin string, cmdArgs, env []string, err error) {
	switch mgr {
	case "apt":
		env = []string{"DEBIAN_FRONTEND=noninteractive"}
		switch state {
		case statePresent, stateLatest:
			cmdArgs = append([]string{"install", "-y", "--no-install-recommends"}, aptConffileOpts...)
		case stateAbsent:
			cmdArgs = []string{"remove", "-y"}
		}
		return aptBin, append(cmdArgs, names...), env, nil
	case "dnf", "yum":
		switch state {
		case statePresent:
			cmdArgs = []string{"install", "-y"}
		case stateLatest:
			cmdArgs = []string{"upgrade", "-y"}
		case stateAbsent:
			cmdArgs = []string{"remove", "-y"}
		}
		return mgr, append(cmdArgs, names...), nil, nil
	case "apk":
		switch state {
		case statePresent:
			cmdArgs = []string{"add"}
		case stateLatest:
			cmdArgs = []string{"add", "-u"}
		case stateAbsent:
			cmdArgs = []string{"del"}
		}
		return "apk", append(cmdArgs, names...), nil, nil
	case "pacman":
		switch state {
		case statePresent:
			cmdArgs = []string{"-S", "--noconfirm", "--needed"}
		case stateLatest:
			cmdArgs = []string{"-S", "--noconfirm"}
		case stateAbsent:
			cmdArgs = []string{"-R", "--noconfirm"}
		}
		return "pacman", append(cmdArgs, names...), nil, nil
	default:
		return "", nil, nil, fmt.Errorf("package: unsupported manager %q", mgr)
	}
}

// packageRefreshCommand returns the index-refresh command for a manager (used
// when update_cache is set). mgr is already validated against the allowlist, so
// the binary name is never attacker-controlled. The refresh is non-interactive
// and takes no package arguments. aptBin is the resolved apt binary (apt or
// apt-get).
func packageRefreshCommand(mgr, aptBin string) (bin string, args, env []string) {
	switch mgr {
	case "apt":
		return aptBin, []string{"update"}, []string{"DEBIAN_FRONTEND=noninteractive"}
	case "dnf":
		return "dnf", []string{"makecache"}, nil
	case "yum":
		return "yum", []string{"makecache"}, nil
	case "apk":
		return "apk", []string{"update"}, nil
	case "pacman":
		return "pacman", []string{"-Sy", "--noconfirm"}, nil
	default:
		return "", nil, nil
	}
}

// packageUpgradeCommand returns the full-system upgrade command (all installed
// packages) for a manager. For apt, `full`/`dist` maps to `apt full-upgrade`
// (or `apt-get dist-upgrade` when the binary is apt-get); other managers treat
// safe and full alike. Every flag is fixed and non-interactive.
func packageUpgradeCommand(mgr string, mode upgradeMode, aptBin string) (bin string, args, env []string, err error) {
	switch mgr {
	case "apt":
		env = []string{"DEBIAN_FRONTEND=noninteractive"}
		if mode == upgradeFull {
			// `apt full-upgrade` is the modern spelling of `apt-get dist-upgrade`.
			if aptBin == "apt" {
				args = []string{"full-upgrade", "-y"}
			} else {
				args = []string{"dist-upgrade", "-y"}
			}
		} else {
			args = []string{"upgrade", "-y"}
		}
		args = append(args, aptConffileOpts...)
		return aptBin, args, env, nil
	case "dnf", "yum":
		return mgr, []string{"upgrade", "-y"}, nil, nil
	case "apk":
		return "apk", []string{"upgrade"}, nil, nil
	case "pacman":
		return "pacman", []string{"-Su", "--noconfirm"}, nil, nil
	default:
		return "", nil, nil, fmt.Errorf("package: unsupported manager %q", mgr)
	}
}
