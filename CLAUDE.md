# go-tangra-actions

Pure Go **library** (no gRPC/ent/DB) implementing a GitHub-Actions-style engine
for **local host operations**. Consumed by `go-tangra-client` (executes actions
on the host) and, later, `go-tangra-executor` (orchestration). See `README.md`.

## Design invariants

- **Library only.** No Kratos server, no proto, no database. The public API is
  Go packages. Keep dependencies minimal (`cel-go`, `yaml.v3`, stdlib).
- **Single OS boundary.** All side effects go through `system.System`. Nothing
  outside `system/real.go` may import `os/exec` or write the filesystem. Tests
  use `system.Fake`.
- **Actions are extensible**, three ways: (1) native Go actions (`action.Action`
  + `Registry.Register`); (2) **scripted** JS/Lua actions (`runs.using:
  javascript|lua` + `main`) executed by a consumer-injected
  `engine.ScriptRuntime` against a sandboxed `engine.ScriptHost` (Exec/ReadFile/
  WriteFile/Log routed through `system`+`secure`); (3) composite YAML actions
  (`runs.using: composite`). External actions are obtained via a pluggable
  `engine.Resolver` that returns the manifest **and package files** (`fs.FS`).
  The engine does **no network/registry I/O and embeds no script engine** — only
  what a Resolver exposes is runnable and scripts run only if a ScriptRuntime is
  supplied; remote/signed fetching + the JS/Lua VM live in the consumer
  (go-tangra-client/executor). Composite nesting is depth- and cycle-guarded. No
  Docker/container action runtime.
- **CEL for conditions** (`expr/`), exposing GitHub-Actions contexts
  (`env`,`inputs`,`steps`,`job`,`needs`,`runner`) and status functions
  (`success`,`failure`,`always`,`cancelled`). Default step condition is
  `success()`. Status functions read per-eval state held on the `expr.Engine`
  (one Engine per run; `Eval` is mutex-guarded, not reentrant).
- **Security is tested, not assumed.** Every security control in `secure/` has
  adversarial tests (injection, traversal, secret leakage). Aim 80%+ coverage.
- **Immutability** at boundaries: parsing/eval return new values; contexts are
  rebuilt per step, never mutated in place.

## Build / test

```bash
make build        # go build ./...
make test         # go test -race -cover ./...
make vet          # go vet ./...
make cover        # coverage report
```

Offline: deps are in the module cache; `GOFLAGS=-mod=mod`.

## Conventions

- Module path `github.com/go-tangra/go-tangra-actions`, Go 1.25.4.
- Small files (<400 lines), table-driven tests, `gofmt`/`goimports` clean.
- Builtin action names: `run`, `package`, `file`, `file_line`, `service`, `hostname`, `timezone`.
- CEL injection-safety mirrors `go-tangra-ticket/internal/rules` (whitelist
  fields, emit literals, never interpolate user text into expression source).
