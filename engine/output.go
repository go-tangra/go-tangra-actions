package engine

import (
	"context"
	"io"

	"github.com/go-tangra/go-tangra-actions/system"
)

// OutputStream identifies which stream a chunk of live output came from.
type OutputStream int

const (
	StreamStdout OutputStream = iota
	StreamStderr
)

func (s OutputStream) String() string {
	if s == StreamStderr {
		return "stderr"
	}
	return "stdout"
}

// OutputKind distinguishes a live-output event: a step boundary (started/
// finished) or a chunk of step output. This lets a consumer render
// GitHub-Actions-style grouped logs with per-step results.
type OutputKind int

const (
	KindOutput       OutputKind = iota // a chunk of step output (Stream/Data)
	KindStepStarted                    // a step began (Name/Uses)
	KindStepFinished                   // a step ended (Outcome)
)

// OutputEvent is a live event produced as a workflow runs: a step starting, a
// chunk of that step's output, or a step finishing. For KindOutput, Data is a
// raw output chunk (not necessarily a whole line) and is NOT secret-masked, so a
// sink that persists/forwards output should mask if needed (the engine masks the
// buffered StepReport.Stdout/Stderr).
type OutputEvent struct {
	Kind   OutputKind
	Job    string
	Step   string // step id, or display name when it has no id
	Name   string // step display name (KindStepStarted/Finished)
	Uses   string // action reference, if any (KindStepStarted)
	Stream OutputStream
	Data   []byte
	// Outcome is the step result for KindStepFinished: "success", "failure",
	// or "skipped".
	Outcome string
}

// OutputSink receives live output as steps run. It is optional (engine.Options)
// and enables streaming logs in real time, GitHub-Actions style. It may be
// called from the goroutine running the step and must be safe for that use.
type OutputSink func(OutputEvent)

// sinkWriter is an io.Writer bound to a job/step/stream that forwards each write
// to an OutputSink. Data is copied because the caller may reuse the buffer.
type sinkWriter struct {
	sink   OutputSink
	job    string
	step   string
	stream OutputStream
}

func (w sinkWriter) Write(p []byte) (int, error) {
	if w.sink != nil && len(p) > 0 {
		b := make([]byte, len(p))
		copy(b, p)
		w.sink(OutputEvent{Job: w.job, Step: w.step, Stream: w.stream, Data: b})
	}
	return len(p), nil
}

// outputSystem wraps a System and tees Exec output to per-step writers (unless
// the request already carries its own). Every other System method delegates
// unchanged.
type outputSystem struct {
	system.System
	stdout io.Writer
	stderr io.Writer
}

func (o outputSystem) Exec(ctx context.Context, req system.ExecRequest) (system.ExecResult, error) {
	if req.StdoutWriter == nil {
		req.StdoutWriter = o.stdout
	}
	if req.StderrWriter == nil {
		req.StderrWriter = o.stderr
	}
	return o.System.Exec(ctx, req)
}
