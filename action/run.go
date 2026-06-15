package action

import (
	"context"
	"fmt"

	"github.com/go-tangra/go-tangra-actions/secure"
	"github.com/go-tangra/go-tangra-actions/system"
)

// Run executes an inline shell command. It is the one builtin that intentionally
// invokes a shell — the workflow author is explicitly asking to run a command —
// so the command string is passed to `<shell> -c`. All other actions are
// structured and never use a shell.
//
// Inputs (with):
//
//	command  (required) the command line to execute
//	shell    interpreter, default "sh" (allowlisted: sh, bash, dash, ash, zsh)
//	workdir  working directory (optional; confined to ConfineRoot when set)
//
// Outputs: stdout, stderr, exit_code.
type Run struct{}

func (*Run) Name() string { return "run" }

// allowedShells restricts the interpreter to a small known set, by bare name
// only. This prevents a step from selecting an arbitrary binary (or absolute
// path) as the "shell" and having it executed with `-c <command>`.
var allowedShells = map[string]bool{
	"sh": true, "bash": true, "dash": true, "ash": true, "zsh": true,
}

func (*Run) Run(ctx context.Context, in Input) (Result, error) {
	a := args(in.With)
	command, err := a.required("command")
	if err != nil {
		return Result{}, err
	}

	shell := a.withDefault("shell", "sh")
	if !allowedShells[shell] {
		return Result{}, fmt.Errorf("run: shell %q is not permitted (allowed: sh, bash, dash, ash, zsh)", shell)
	}

	// A working directory, if given, is subject to the same confinement as file
	// paths so it cannot anchor relative command operations outside the root.
	var dir string
	if wd := a.str("workdir"); wd != "" {
		dir, err = secure.Confine(in.ConfineRoot, wd)
		if err != nil {
			return Result{}, fmt.Errorf("run: workdir: %w", err)
		}
	}

	res, err := in.System.Exec(ctx, system.ExecRequest{
		Name:  command,
		Shell: shell,
		Env:   in.Env,
		Dir:   dir,
	})
	if err != nil {
		return Result{Stdout: res.Stdout, Stderr: res.Stderr}, fmt.Errorf("run: %w", err)
	}

	result := Result{
		Changed: true,
		Stdout:  res.Stdout,
		Stderr:  res.Stderr,
		Outputs: map[string]string{
			"stdout":    res.Stdout,
			"stderr":    res.Stderr,
			"exit_code": fmt.Sprintf("%d", res.ExitCode),
		},
	}
	if res.ExitCode != 0 {
		return result, fmt.Errorf("run: command exited with code %d", res.ExitCode)
	}
	return result, nil
}
