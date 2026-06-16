package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-tangra/go-tangra-actions/secure"
	"github.com/go-tangra/go-tangra-actions/system"
)

// ServiceStatus reports the state of a single systemd unit without changing it —
// the read-only counterpart to the `service` action. It is the clean way for a
// workflow to branch on whether a unit exists, is running, or is enabled,
// instead of shelling out to `systemctl is-active`/`is-enabled` and parsing exit
// codes. The unit name is validated with secure.ValidateServiceName; no shell is
// invoked.
//
// It runs a single `systemctl show <unit>` (which exits 0 even for unknown units,
// reporting LoadState=not-found) and exposes the parsed properties.
//
// Inputs (with):
//
//	name  (required) one unit name, e.g. "nginx" or "php8.3-fpm.service"
//
// Outputs:
//
//	name        the queried unit
//	exists      "true" when the unit is known (LoadState != not-found)
//	active      ActiveState: active | inactive | failed | activating | ...
//	sub         SubState: running | dead | exited | ...
//	load        LoadState: loaded | not-found | masked | ...
//	enabled     UnitFileState: enabled | disabled | static | masked | ...
//	running     "true" when active == "active"
//	is_enabled  "true" when the unit is enabled (UnitFileState starts with "enabled")
type ServiceStatus struct{}

func (*ServiceStatus) Name() string { return "service_status" }

func (*ServiceStatus) Run(ctx context.Context, in Input) (Result, error) {
	a := args(in.With)

	name, err := a.required("name")
	if err != nil {
		return Result{}, err
	}
	if err := secure.ValidateServiceName(name); err != nil {
		return Result{}, fmt.Errorf("service_status: %w", err)
	}

	res, err := in.System.Exec(ctx, system.ExecRequest{
		Name: "systemctl",
		Args: []string{"show", name, "-p", "LoadState", "-p", "ActiveState", "-p", "SubState", "-p", "UnitFileState"},
		Env:  in.Env,
	})
	if err != nil {
		return Result{Stdout: res.Stdout, Stderr: res.Stderr}, fmt.Errorf("service_status: exec systemctl show: %w", err)
	}

	props := parseShowProperties(res.Stdout)
	load := props["LoadState"]
	active := props["ActiveState"]
	sub := props["SubState"]
	unitFile := props["UnitFileState"]

	exists := load != "" && load != "not-found"
	running := active == "active"
	isEnabled := strings.HasPrefix(unitFile, "enabled")

	return Result{
		Stdout: res.Stdout,
		Stderr: res.Stderr,
		Outputs: map[string]string{
			"name":       name,
			"exists":     fmt.Sprintf("%t", exists),
			"active":     active,
			"sub":        sub,
			"load":       load,
			"enabled":    unitFile,
			"running":    fmt.Sprintf("%t", running),
			"is_enabled": fmt.Sprintf("%t", isEnabled),
		},
	}, nil
}

// parseShowProperties parses `systemctl show` output (Key=Value lines) into a map.
func parseShowProperties(out string) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return props
}
