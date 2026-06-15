# Examples

| File | Shows |
|---|---|
| `provision-web.yaml` | A multi-job workflow: inputs, env, `needs`, per-step `if`, status functions, `${{ }}` interpolation, builtin `package`/`file`/`service`/`run` actions. |
| `actions/nginx-vhost/action.yaml` | A **composite** action (a reusable step list with `inputs`/`outputs`). |
| `actions/greet/` | A small self-contained **JavaScript** action (`action.yaml` + `index.js`) — runs anywhere. |
| `actions/service-health/` | A realistic **scripted (JavaScript)** action that shells out to `systemctl`. |
| `actions/install-fzf/` | A **scripted (JavaScript)** action that installs fzf and writes a bash integration drop-in (idempotent, multi–package-manager). |
| `actions/disable-auto-updates/action.yaml` | A **composite** action that disables apt auto-update timers + zabbix via `service` (`ignore_missing`). |
| `actions/common-grub/action.yaml` | A **composite** action: the Puppet `common::grub` equivalent — `file_line` edits + a refresh-only `update-grub` (runs only if a line changed). |
| `actions/system-update/action.yaml` | A **composite** action: `apt update && apt upgrade` (multi–package-manager; prefers `apt` over `apt-get`). Input `upgrade: safe\|full`. |
| `js-demo.yaml` | A workflow that runs the `greet` JS action and uses its output. |
| `healthcheck.yaml` | A workflow that `uses:` the scripted action and branches on its outputs. |
| `setup-fzf.yaml` | A workflow that `uses:` the `install-fzf` JS action. |
| `grub.yaml` | A workflow that `uses:` the `common-grub` composite (needs a real bootloader; Tier-2 VM). |
| `system-update.yaml` | A workflow that `uses:` the `system-update` composite (`upgrade` input safe/full). |

The CLI runs JS actions directly (it embeds the `jsruntime` goja runtime):

```bash
./bin/tangra-actions -actions examples/actions examples/js-demo.yaml
# ✓ Greet (in JS)   message: HELLO, WORLD!
```

## Running a workflow (consumer side)

```go
import (
    "context"
    "github.com/go-tangra/go-tangra-actions/engine"
    "github.com/go-tangra/go-tangra-actions/workflow"
)

wf, _ := workflow.Parse(yamlBytes)

r := engine.New(engine.Options{
    // System defaults to the real host; Registry defaults to the builtins.
    Resolver:      engine.DirResolver{Root: "examples/actions"}, // external actions
    ScriptRuntime: myJSRuntime,                                  // executes scripted actions
    ConfineRoot:   "/var/lib/tangra/work",                       // optional path jail
    Secrets:       []string{token},                              // masked in all output
})

result, err := r.Run(context.Background(), wf, map[string]string{"service": "nginx"})
```

`provision-web.yaml` and `actions/nginx-vhost` need only a `Resolver` (no script
runtime). `healthcheck.yaml` additionally needs a `ScriptRuntime` because
`service-health` is a JavaScript action.

## Scripted-action host API (the `tangra` global)

The library defines `engine.ScriptRuntime` (executes a script) and
`engine.ScriptHost` (the sandboxed capabilities a script may use). It ships **no**
JavaScript engine — a consumer (e.g. `go-tangra-client`, which already embeds a
JS/Lua engine) implements `ScriptRuntime` and binds `ScriptHost` + the action's
inputs/outputs into the script's globals.

The convention used by `actions/service-health/index.js` is a `tangra` global:

| JS | Backed by |
|---|---|
| `tangra.getInput(name)` | `ScriptInvocation.Inputs[name]` |
| `tangra.setOutput(name, value)` | accumulated into `ScriptResult.Outputs` |
| `tangra.exec(name, args)` → `{stdout, stderr, code}` | `ScriptHost.Exec` (through `system.System`) |
| `tangra.readFile(path)` / `tangra.writeFile(path, data, mode?)` | `ScriptHost.ReadFile` / `WriteFile` (path-confined) |
| `tangra.log(line)` | `ScriptHost.Log` (becomes the step's stdout) |
| `tangra.env[name]` | `ScriptInvocation.Env` |

Because every capability routes back through `system` + `secure`, a downloaded
script is confined exactly like a builtin action: it cannot touch paths outside
`ConfineRoot`, and any registered secret is masked in its outputs and logs.
