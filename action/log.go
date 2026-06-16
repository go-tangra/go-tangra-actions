package action

import (
	"context"
	"io"
)

// Log writes a message to the step's output — the code-free, no-shell equivalent
// of `run: echo ...`. It is the way to emit a line into the live log/stream from
// a restricted (native-actions-only) workflow, where `run:` shell steps are not
// permitted. The message is taken verbatim from the `message` input (already
// `${{ }}`-interpolated by the engine), so it can reference prior step outputs.
//
// Inputs (with):
//
//	message  the text to log (optional; empty prints a blank line)
//
// The line is written to the live output writer when one is wired (so it streams
// in real time) and is also returned as the step's Stdout.
type Log struct{}

func (*Log) Name() string { return "log" }

func (*Log) Run(_ context.Context, in Input) (Result, error) {
	line := args(in.With).str("message") + "\n"
	if in.Stdout != nil {
		_, _ = io.WriteString(in.Stdout, line)
	}
	return Result{Stdout: line}, nil
}
