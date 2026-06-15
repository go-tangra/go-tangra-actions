// Entry script for the `service-health` action (runs.main: index.js).
//
// It runs against the sandboxed host API the ScriptRuntime binds as the global
// `tangra` — a thin JS surface over engine.ScriptHost + the action's inputs and
// outputs. The script has NO direct OS access: every side effect below goes
// through that bridge, so secret masking and path confinement still apply.
//
// Host API contract (what go-tangra-client's runtime must bind):
//   tangra.getInput(name)            -> string        (action inputs)
//   tangra.setOutput(name, value)    -> void          (-> ScriptResult.Outputs)
//   tangra.exec(name, args)          -> {stdout, stderr, code}   (ScriptHost.Exec)
//   tangra.readFile(path)            -> string        (ScriptHost.ReadFile, confined)
//   tangra.writeFile(path, data, mode?) -> void       (ScriptHost.WriteFile, confined)
//   tangra.log(line)                 -> void          (ScriptHost.Log -> step stdout)
//   tangra.env[name]                 -> string        (merged env)

function main() {
  const service = tangra.getInput("service");
  tangra.log("checking service: " + service);

  // `systemctl is-active <unit>` exits non-zero when not active and prints the
  // state word on stdout either way. No shell is used — args are passed through.
  const res = tangra.exec("systemctl", ["is-active", service]);
  const state = (res.stdout || "").trim() || "unknown";
  const active = res.code === 0;

  tangra.log("state=" + state + " active=" + active);
  tangra.setOutput("state", state);
  tangra.setOutput("active", active ? "true" : "false");

  // Optionally drop a one-line report. The path is confined by the host, so a
  // value like "../../etc/passwd" is rejected before any write happens.
  const reportPath = tangra.getInput("report_path");
  if (reportPath) {
    tangra.writeFile(reportPath, service + " " + state + "\n", "0644");
    tangra.log("wrote report to " + reportPath);
  }
}

main();
