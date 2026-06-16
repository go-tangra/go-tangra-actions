# go-tangra-actions

A **GitHub-Actions-style execution engine for host operations**, packaged as a
pure Go library (no gRPC / no DB / no infra). It parses a workflow of *jobs* and
*steps*, evaluates per-step/per-job **conditions** (CEL, with GitHub-Actions
semantics), and runs structured **actions** on the local host: install packages,
edit files, manage services, run commands.

It is consumed by:

- **`go-tangra-client`** — the on-host agent that actually executes the actions
  locally.
- **`go-tangra-executor`** — orchestrates which workflows run where (later).

> Execution is **local to the host running the agent**. "Remote host" is the
> platform's point of view; this library only ever touches the machine it runs
> on, through a single mockable `system.System` boundary.

## Concepts

```yaml
name: provision-web
inputs:                       # run-time parameters (${{ inputs.x }})
  domain:
    default: example.com
env:                          # workflow env (${{ env.X }})
  STAGE: prod
jobs:
  web:
    steps:
      - id: pkg
        name: Install nginx
        uses: package
        with: { name: nginx, state: present }

      - name: Write vhost
        if: steps.pkg.outcome == 'success'      # gh-actions-style condition
        uses: file
        with:
          path: /etc/nginx/conf.d/site.conf
          content: "server_name ${{ inputs.domain }};"
          mode: "0644"

      - name: Restart
        if: success()                           # status function
        uses: service
        with: { name: nginx, state: restarted }

      - name: Always log
        if: always()
        run: echo "done on ${{ runner.os }}"
```

## Conditions (CEL with GitHub-Actions semantics)

`if:` expressions are evaluated with [CEL](https://github.com/google/cel-go) —
the same engine the rest of go-tangra uses — exposing GitHub-Actions contexts
and status functions:

| Context / fn | Meaning |
|---|---|
| `env.<KEY>` | merged workflow + job + step env |
| `inputs.<name>` | workflow inputs |
| `steps.<id>.outcome` / `.conclusion` | step result before / after `continue-on-error` |
| `steps.<id>.outputs.<name>` | output set by a previous step |
| `job.status` | cumulative status of the current job |
| `needs.<job>.result` / `.outputs.<name>` | upstream job results |
| `runner.os` / `runner.arch` | host info |
| `success()` | no prior step in the job failed (the **default** when `if` is omitted) |
| `failure()` | a prior step failed |
| `always()` | always run (even after failure / cancel) |
| `cancelled()` | the run was cancelled |

`${{ … }}` interpolation is supported in `run`, `with:` values and `env:` values.

## Actions are extensible (like GitHub Actions)

A step's `uses:` resolves three ways, so the verb set is fully open:

1. **Native (Go) actions** — implement `action.Action` and `Registry.Register`
   it. This is how a consumer (`go-tangra-client`) adds first-class actions in
   code. The builtins (`run`, `package`, `file`, `file_line`, `service`,
   `service_status`, `hostname`, `timezone`) are exactly these.
2. **Scripted (JS/Lua) actions** — downloadable code packages, the way GitHub
   actions work (`runs.using: node20` + `main: index.js`). A `ScriptRuntime`
   (supplied by the consumer — `go-tangra-client` already embeds a JS/Lua engine)
   executes the package's `main` script against a **sandboxed `ScriptHost`**
   (`Exec`, `ReadFile`, `WriteFile`, `Log`) that routes every side effect back
   through `system` + `secure`. The script gets no raw OS access, so masking and
   path confinement apply to downloaded code exactly as to builtins:

   ```yaml
   # actions/notify/action.yaml
   name: notify
   inputs:
     message: { required: true }
   outputs:
     status: { value: "set by the script" }
   runs:
     using: javascript      # any language the injected ScriptRuntime supports
     main: index.js         # executed from the action package's files
   ```

   The library ships **no** script engine and does no fetching — it defines the
   `ScriptRuntime`/`ScriptHost` interfaces and the `Resolver` that supplies the
   package files; the consumer plugs in the engine (and decides how packages are
   fetched/verified).
3. **Composite (YAML) actions** — reusable actions defined as data, the analogue
   of a GitHub composite `action.yml`. A composite action declares
   `inputs`/`outputs` and a `runs.using: composite` step list that reuses other
   actions:

   ```yaml
   # actions/nginx-vhost/action.yaml
   name: nginx-vhost
   inputs:
     domain: { required: true }
   outputs:
     conf_path: { value: "${{ steps.write.outputs.path }}" }
   runs:
     using: composite
     steps:
       - id: write
         uses: file
         with:
           path: /etc/nginx/conf.d/${{ inputs.domain }}.conf
           content: "server_name ${{ inputs.domain }};"
       - uses: service
         with: { name: nginx, state: reloaded }
   ```

   referenced from a workflow as:

   ```yaml
   - uses: nginx-vhost            # resolved by the configured Resolver
     with: { domain: example.com }
   ```

Where composite and scripted actions come from is the consumer's decision, via a
**`Resolver`** (`engine.Options.Resolver`), which returns the manifest **and the
action package's files** (`fs.FS`, so a scripted action's `main` and its
siblings can be read). The library ships:

- `DirResolver{Root}` — loads `<root>/<ref>/action.yaml` (or `<ref>.yaml`) and
  exposes the package directory, all **confined to Root** (`../` escapes
  rejected).
- `MapResolver` — an in-memory catalog (already-loaded/verified actions).

The engine itself does **no network or registry I/O** — a reference only runs if
a Resolver explicitly makes it resolvable, and a script only runs if a
`ScriptRuntime` is supplied. Remote fetching / signature verification belongs in
the consumer (`go-tangra-executor`), which can implement `Resolver` over a signed
bundle store. Composite nesting is bounded (`MaxActionDepth`, default 16) and
cycles are detected.

## CLI

A small runner is included for trying workflows from the shell:

```bash
make cli                                  # builds ./bin/tangra-actions
./bin/tangra-actions examples/hello.yaml
./bin/tangra-actions -input who=tangra examples/hello.yaml
./bin/tangra-actions -actions ./examples/actions ./examples/js-demo.yaml   # runs a JS action
```

```
  -input k=v     workflow input (repeatable)
  -secret VALUE  value to mask in output (repeatable)
  -actions DIR   directory of external action packages (enables `uses:`)
  -confine ROOT  restrict file actions to this directory
  -no-js         disable the built-in JavaScript runtime
```

It prints each job/step outcome (`✓` success, `✗` failure, `∅` skipped) and
exits non-zero if the workflow failed. The CLI wires in a goja-backed
JavaScript runtime (the `jsruntime` package) by default, so **scripted JS actions
run out of the box**; pass `-no-js` to disable it. The `jsruntime` import is what
pulls in goja — the **core library stays engine-free**, so consumers that only
import `engine`/`workflow`/`action` don't compile a JS engine. A host such as
`go-tangra-client` can use `jsruntime` or implement `engine.ScriptRuntime` over
its own JS/Lua VMs.

## Security

Security is a first-class concern (see `secure/`):

- **One OS boundary** (`system.System`) — every side effect goes through it; the
  real impl is the only code that touches `os/exec` and the filesystem.
- **No shell unless asked** — structured actions never invoke a shell. Package
  and service names are validated against a strict allowlist (no shell
  metacharacters) before reaching `apt`/`dnf`/`systemctl`.
- **Path confinement** — `file` actions are confined to a configurable root;
  `..` traversal and absolute escapes are rejected.
- **Secret masking** — registered secrets are masked in step output and logs.
- **Sandboxed expressions** — CEL only; no arbitrary code, whitelisted
  functions, evaluated against typed contexts.
- **Resource bounds** — per-step timeouts; context cancellation is honoured.

## Layout

| Package | Responsibility |
|---|---|
| `workflow` | model + YAML parsing + strict validation |
| `expr` | CEL condition engine, contexts, status functions, `${{ }}` interpolation |
| `system` | the OS boundary (`real` impl + `fake` for tests) |
| `secure` | secret masking, path confinement, name/argument validation |
| `action` | `Action` interface + registry + builtins (`run`, `package`, `file`, `file_line`, `service`, `service_status`, `hostname`, `timezone`) |
| `engine` | the runner: job/step orchestration, condition evaluation, status tracking, composite-action expansion + `Resolver`s |

## Status

v0 — library core. No persistence or transport; that lives in the consumers.
