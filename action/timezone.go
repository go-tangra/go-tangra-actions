package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-tangra/go-tangra-actions/secure"
	"github.com/go-tangra/go-tangra-actions/system"
)

// Timezone sets the system timezone. It is idempotent: when the current zone
// already equals the requested one it makes no change. The zone name is
// validated (IANA shape) before use and no shell is invoked.
//
// It prefers systemd's `timedatectl set-timezone <zone>`. When timedatectl is
// absent — or present but unusable, e.g. on a minimal container with no D-Bus
// system bus — it falls back to the portable method: copy
// /usr/share/zoneinfo/<zone> onto /etc/localtime (glibc reads a regular file
// there, no symlink needed). Either way the zone is recorded in /etc/timezone
// for Debian tooling and for this action's own idempotency check.
//
// Inputs (with):
//
//	name  (required) IANA timezone (e.g. UTC, Europe/Sofia, America/New_York)
//
// Outputs: timezone, previous.
type Timezone struct{}

func (*Timezone) Name() string { return "timezone" }

const (
	timezoneFile  = "/etc/timezone"
	localtimeFile = "/etc/localtime"
	zoneinfoDir   = "/usr/share/zoneinfo"
)

func (*Timezone) Run(ctx context.Context, in Input) (Result, error) {
	a := args(in.With)

	name, err := a.required("name")
	if err != nil {
		return Result{}, err
	}
	if err := secure.ValidateTimezone(name); err != nil {
		return Result{}, fmt.Errorf("timezone: %w", err)
	}

	current := currentTimezone(in.System)
	result := Result{Outputs: map[string]string{
		"timezone": name,
		"previous": current,
	}}
	if current == name {
		return result, nil // already set — idempotent no-op
	}

	// Preferred path: timedatectl. Like hostnamectl it can be on PATH yet fail to
	// reach the D-Bus bus, so on any error we fall through to the portable copy.
	set := false
	if _, ok := in.System.LookPath("timedatectl"); ok {
		if err := execOK(ctx, in, "timedatectl", "set-timezone", name); err == nil {
			set = true
		}
	}
	if !set {
		// Reading the zoneinfo file both validates the zone exists and gives us
		// the bytes to install as /etc/localtime.
		zonePath := zoneinfoDir + "/" + name
		data, err := in.System.ReadFile(zonePath)
		if err != nil {
			return result, fmt.Errorf("timezone: unknown timezone %q (no %s): %w", name, zonePath, err)
		}
		// /etc/localtime is normally a symlink into the zoneinfo tree. Writing
		// straight to it would FOLLOW that symlink and overwrite the canonical
		// zone file (e.g. corrupt Etc/UTC). Remove the link first so the write
		// creates a fresh regular file at /etc/localtime instead.
		if err := in.System.Remove(localtimeFile); err != nil {
			return result, fmt.Errorf("timezone: remove %s: %w", localtimeFile, err)
		}
		if err := in.System.WriteFile(localtimeFile, data, 0o644); err != nil {
			return result, fmt.Errorf("timezone: write %s: %w", localtimeFile, err)
		}
	}

	// Persist the zone name for Debian tooling and our idempotency check.
	if err := in.System.WriteFile(timezoneFile, []byte(name+"\n"), 0o644); err != nil {
		return result, fmt.Errorf("timezone: write %s: %w", timezoneFile, err)
	}

	result.Changed = true
	return result, nil
}

// currentTimezone reads the persisted zone from /etc/timezone, returning "" if
// it cannot be determined. Used only for the idempotency check.
func currentTimezone(sys system.System) string {
	data, err := sys.ReadFile(timezoneFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
