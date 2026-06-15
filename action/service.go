package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-tangra/go-tangra-actions/secure"
	"github.com/go-tangra/go-tangra-actions/system"
)

// Service manages one or more systemd units via systemctl. Unit names are
// validated with secure.ValidateServiceName before use; no shell is invoked.
//
// Inputs (with):
//
//	name            (required) one or more unit names (comma/space/newline
//	                separated), e.g. "nginx", "nginx.service", "apt-daily.timer"
//	state           started | stopped | restarted | reloaded   (optional)
//	enabled         true | false — enable/disable at boot       (optional)
//	ignore_missing  true | false — when true, units that do not exist on the
//	                host are silently skipped instead of failing (default false).
//	                Useful for disabling services that may or may not be present
//	                (e.g. zabbix-agent) on a freshly provisioned host.
//
// At least one of state/enabled must be set. The same operation is applied to
// every named unit. Outputs: name, state, enabled, missing (skipped units).
type Service struct{}

func (*Service) Name() string { return "service" }

var serviceStateCmd = map[string]string{
	"started":   "start",
	"stopped":   "stop",
	"restarted": "restart",
	"reloaded":  "reload",
}

func (*Service) Run(ctx context.Context, in Input) (Result, error) {
	a := args(in.With)

	nameRaw, err := a.required("name")
	if err != nil {
		return Result{}, err
	}
	names := splitList(nameRaw)
	if len(names) == 0 {
		return Result{}, fmt.Errorf("service: no service names provided")
	}
	for _, n := range names {
		if err := secure.ValidateServiceName(n); err != nil {
			return Result{}, fmt.Errorf("service: %w", err)
		}
	}

	state := a.str("state")
	enabledRaw := a.str("enabled")
	if state == "" && enabledRaw == "" {
		return Result{}, fmt.Errorf("service: set at least one of `state` or `enabled`")
	}

	ignoreMissing, err := a.boolValue("ignore_missing", false)
	if err != nil {
		return Result{}, fmt.Errorf("service: %w", err)
	}

	// Resolve the enable/disable and state verbs once; they apply to every unit.
	var enableVerb string
	var enabledVal bool
	if enabledRaw != "" {
		enabledVal, err = a.boolValue("enabled", false)
		if err != nil {
			return Result{}, fmt.Errorf("service: %w", err)
		}
		enableVerb = "disable"
		if enabledVal {
			enableVerb = "enable"
		}
	}
	var stateVerb string
	if state != "" {
		v, ok := serviceStateCmd[state]
		if !ok {
			return Result{}, fmt.Errorf("service: invalid state %q (want started|stopped|restarted|reloaded)", state)
		}
		stateVerb = v
	}

	result := Result{Outputs: map[string]string{"name": strings.Join(names, ",")}}
	var changed bool
	var missing []string

	for _, n := range names {
		if ignoreMissing && !unitExists(ctx, in, n) {
			missing = append(missing, n)
			continue
		}
		// Enable/disable first, so a freshly-enabled unit can then be started.
		if enableVerb != "" {
			if err := systemctl(ctx, in, enableVerb, n); err != nil {
				return result, err
			}
			changed = true
		}
		if stateVerb != "" {
			if err := systemctl(ctx, in, stateVerb, n); err != nil {
				return result, err
			}
			changed = true
		}
	}

	if enabledRaw != "" {
		result.Outputs["enabled"] = fmt.Sprintf("%t", enabledVal)
	}
	if state != "" {
		result.Outputs["state"] = state
	}
	if len(missing) > 0 {
		result.Outputs["missing"] = strings.Join(missing, ",")
	}
	result.Changed = changed
	return result, nil
}

// unitExists reports whether systemd knows the named unit. `systemctl cat`
// exits 0 when a unit file exists and non-zero when it does not, with no side
// effects — the right probe for ignore_missing.
func unitExists(ctx context.Context, in Input, name string) bool {
	res, err := in.System.Exec(ctx, system.ExecRequest{
		Name: "systemctl",
		Args: []string{"cat", name},
		Env:  in.Env,
	})
	return err == nil && res.ExitCode == 0
}

// systemctl runs `systemctl <verb> <name>`. A non-zero exit is an error.
func systemctl(ctx context.Context, in Input, verb, name string) error {
	res, err := in.System.Exec(ctx, system.ExecRequest{
		Name: "systemctl",
		Args: []string{verb, name},
		Env:  in.Env,
	})
	if err != nil {
		return fmt.Errorf("service: exec systemctl %s: %w", verb, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("service: systemctl %s %s exited with code %d: %s", verb, name, res.ExitCode, res.Stderr)
	}
	return nil
}
