// install-fzf: install the fzf fuzzy finder and enable its bash key bindings
// and completion, following the fzf documentation.
//
// fzf >= 0.48 ships the `fzf --bash` one-liner that the current docs recommend.
// Older packaged builds (e.g. Debian/Ubuntu 0.44) instead install integration
// scripts under /usr/share. The drop-in this action writes handles BOTH at shell
// startup, so it is correct regardless of which fzf the package manager provides.
//
// Everything runs through the sandboxed `tangra` host bridge — no direct OS
// access, so secret masking and path confinement still apply. See
// service-health/index.js for the full bridge contract.

var MANAGERS = [
  { name: "apt",    bin: "apt-get", update: ["update"], install: ["install", "-y", "fzf"] },
  { name: "dnf",    bin: "dnf",     update: null,       install: ["install", "-y", "fzf"] },
  { name: "yum",    bin: "yum",     update: null,       install: ["install", "-y", "fzf"] },
  { name: "apk",    bin: "apk",     update: ["update"], install: ["add", "fzf"] },
  { name: "pacman", bin: "pacman",  update: null,       install: ["-S", "--noconfirm", "--needed", "fzf"] }
];

// Bash integration drop-in. Branches at shell startup so a single file is
// correct for both modern (>= 0.48) and packaged (< 0.48) fzf.
var INTEGRATION = [
  "# fzf bash integration - managed by go-tangra-actions install-fzf.",
  "if command -v fzf >/dev/null 2>&1; then",
  "  if fzf --bash >/dev/null 2>&1; then",
  "    eval \"$(fzf --bash)\"          # fzf >= 0.48",
  "  else",
  "    # Packaged fzf (< 0.48): source the integration files it ships.",
  "    if [ -f /usr/share/doc/fzf/examples/key-bindings.bash ]; then",
  "      . /usr/share/doc/fzf/examples/key-bindings.bash",
  "    fi",
  "    if [ -f /usr/share/bash-completion/completions/fzf ]; then",
  "      . /usr/share/bash-completion/completions/fzf",
  "    fi",
  "  fi",
  "fi",
  ""
].join("\n");

// have reports whether a binary is runnable (exec throws when it cannot start).
function have(bin) {
  try {
    tangra.exec(bin, ["--version"]);
    return true;
  } catch (e) {
    return false;
  }
}

function pickManager() {
  var override = tangra.getInput("manager");
  if (override) {
    for (var i = 0; i < MANAGERS.length; i++) {
      if (MANAGERS[i].name === override) return MANAGERS[i];
    }
    throw new Error("unsupported manager '" + override + "' (apt|dnf|yum|apk|pacman)");
  }
  for (var j = 0; j < MANAGERS.length; j++) {
    if (have(MANAGERS[j].bin)) return MANAGERS[j];
  }
  throw new Error("no supported package manager found (apt/dnf/yum/apk/pacman)");
}

function run(bin, args) {
  var res = tangra.exec(bin, args);
  if (res.code !== 0) {
    throw new Error(bin + " " + args.join(" ") + " exited with code " + res.code +
      (res.stderr ? ": " + res.stderr.trim() : ""));
  }
  return res;
}

function main() {
  var mgr = pickManager();
  tangra.log("installing fzf with " + mgr.name);

  if (mgr.update) {
    run(mgr.bin, mgr.update); // refresh the index so a pristine host can install
  }
  run(mgr.bin, mgr.install);
  tangra.setOutput("manager", mgr.name);
  tangra.setOutput("installed", "true");

  // Enable in bash via a profile.d drop-in. Idempotent: only write when the
  // content differs from what is already there.
  var path = tangra.getInput("profile_path") || "/etc/profile.d/fzf.sh";
  var current = "";
  try { current = tangra.readFile(path); } catch (e) { current = ""; }

  if (current === INTEGRATION) {
    tangra.log("bash integration already up to date at " + path);
    tangra.setOutput("integration", "unchanged");
  } else {
    tangra.writeFile(path, INTEGRATION, "0644");
    tangra.log("wrote bash integration to " + path);
    tangra.setOutput("integration", "written");
  }
  tangra.setOutput("integration_path", path);
}

main();
