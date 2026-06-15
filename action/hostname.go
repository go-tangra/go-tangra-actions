package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-tangra/go-tangra-actions/secure"
	"github.com/go-tangra/go-tangra-actions/system"
)

// Hostname sets the system hostname. It is idempotent: when the static hostname
// already equals the requested name it makes no change. The name is validated
// (RFC 1123) before use and no shell is invoked.
//
// It prefers systemd's `hostnamectl set-hostname <name>` (which updates the
// static and transient hostname and /etc/hostname). When hostnamectl is absent
// — or present but unusable, e.g. on a minimal container with no D-Bus system
// bus — it falls back to the portable method: write /etc/hostname and run
// `hostname <name>` (the hostname binary needs no bus).
//
// Inputs (with):
//
//	name  (required) the hostname to set (e.g. web-01, web-01.example.com)
//
// Outputs: hostname, previous.
type Hostname struct{}

func (*Hostname) Name() string { return "hostname" }

// hostnameFile is the canonical location of the persistent static hostname.
const hostnameFile = "/etc/hostname"

func (*Hostname) Run(ctx context.Context, in Input) (Result, error) {
	a := args(in.With)

	name, err := a.required("name")
	if err != nil {
		return Result{}, err
	}
	if err := secure.ValidateHostname(name); err != nil {
		return Result{}, fmt.Errorf("hostname: %w", err)
	}

	current := currentHostname(in.System)
	result := Result{Outputs: map[string]string{
		"hostname": name,
		"previous": current,
	}}
	if current == name {
		return result, nil // already set — idempotent no-op
	}

	// Preferred path: hostnamectl. It can be on PATH yet still fail to reach the
	// D-Bus system bus (minimal containers), so on any error we fall through to
	// the portable method rather than failing the step.
	if _, ok := in.System.LookPath("hostnamectl"); ok {
		if err := execOK(ctx, in, "hostnamectl", "set-hostname", name); err == nil {
			result.Changed = true
			return result, nil
		}
	}

	// Portable fallback: persist the static name and set the live one.
	if err := in.System.WriteFile(hostnameFile, []byte(name+"\n"), 0o644); err != nil {
		return result, fmt.Errorf("hostname: write %s: %w", hostnameFile, err)
	}
	if err := execOK(ctx, in, "hostname", name); err != nil {
		return result, fmt.Errorf("hostname: %w", err)
	}

	result.Changed = true
	return result, nil
}

// currentHostname reads the static hostname from /etc/hostname, returning "" if
// it cannot be determined. Used only for the idempotency check.
func currentHostname(sys system.System) string {
	data, err := sys.ReadFile(hostnameFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
