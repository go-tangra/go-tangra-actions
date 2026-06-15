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
// Inputs (with):
//
//	name          (required) one or more package names (comma/space/newline separated)
//	state         present | absent | latest  (default present)
//	manager       override auto-detection (apt|dnf|yum|apk|pacman)
//	update_cache  refresh the package index before acting (apt-get update,
//	              dnf/yum makecache, apk update, pacman -Sy). Default false.
//	              A freshly installed host has empty/stale lists, so an install
//	              without a refresh fails; set this true for first-run provisioning.
//
// Outputs: manager, packages, state, cache_updated.
type Package struct{}

func (*Package) Name() string { return "package" }

// pkgState is the desired end state of the named packages.
type pkgState string

const (
	statePresent pkgState = "present"
	stateAbsent  pkgState = "absent"
	stateLatest  pkgState = "latest"
)

func (*Package) Run(ctx context.Context, in Input) (Result, error) {
	a := args(in.With)

	nameRaw, err := a.required("name")
	if err != nil {
		return Result{}, err
	}
	names := splitList(nameRaw)
	if len(names) == 0 {
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

	// Refresh the package index first when asked. A freshly installed host has
	// empty/stale lists, so `state: present` would otherwise fail (e.g. apt-get
	// exit 100). Run it as its own command before the install/upgrade.
	if updateCache {
		rbin, rargs, renv := packageRefreshCommand(mgr)
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

	bin, cmdArgs, env, err := packageCommand(mgr, state, names)
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

// supportedManagers is the allowlist of package-manager names a caller may
// force via the `manager` input. The value maps to a fixed binary in
// packageCommand, so anything outside this set is rejected.
var supportedManagers = map[string]bool{
	"apt": true, "dnf": true, "yum": true, "apk": true, "pacman": true,
}

// detectManager finds the first supported package manager on PATH.
func detectManager(sys system.System) (string, bool) {
	for _, m := range []string{"apt-get", "dnf", "yum", "apk", "pacman"} {
		if _, ok := sys.LookPath(m); ok {
			if m == "apt-get" {
				return "apt", true
			}
			return m, true
		}
	}
	return "", false
}

// packageCommand returns the binary, arguments and extra environment for the
// given manager/state/names. Every flag is fixed and non-interactive; the only
// variable tokens are the validated package names appended at the end.
func packageCommand(mgr string, state pkgState, names []string) (bin string, cmdArgs, env []string, err error) {
	switch mgr {
	case "apt":
		env = []string{"DEBIAN_FRONTEND=noninteractive"}
		switch state {
		case statePresent, stateLatest:
			cmdArgs = []string{"install", "-y", "--no-install-recommends"}
		case stateAbsent:
			cmdArgs = []string{"remove", "-y"}
		}
		return "apt-get", append(cmdArgs, names...), env, nil
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
// and takes no package arguments.
func packageRefreshCommand(mgr string) (bin string, args, env []string) {
	switch mgr {
	case "apt":
		return "apt-get", []string{"update"}, []string{"DEBIAN_FRONTEND=noninteractive"}
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
