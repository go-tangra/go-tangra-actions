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

// OutputEvent is a chunk of live output produced by a step as it runs. Data is a
// raw output chunk (not necessarily a whole line); secrets are NOT masked here,
// so a sink that persists/forwards output should apply masking if needed (the
// engine masks the buffered StepReport.Stdout/Stderr).
type OutputEvent struct {
	Job    string
	Step   string // step id, or display name when it has no id
	Stream OutputStream
	Data   []byte
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
