package engine

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/go-tangra/go-tangra-actions/system"
)

// TestRunner_StepLifecycleEvents verifies the sink receives StepStarted /
// StepFinished(outcome) boundary events around a step's output.
func TestRunner_StepLifecycleEvents(t *testing.T) {
	f := system.NewFake()
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		if req.StdoutWriter != nil {
			_, _ = io.WriteString(req.StdoutWriter, "ok\n")
		}
		return system.ExecResult{Stdout: "ok\n", ExitCode: 0}, nil
	}
	var seq []string
	r := New(Options{System: f, Output: func(ev OutputEvent) {
		switch ev.Kind {
		case KindStepStarted:
			seq = append(seq, "start:"+ev.Name)
		case KindStepFinished:
			seq = append(seq, "end:"+ev.Name+":"+ev.Outcome)
		case KindOutput:
			seq = append(seq, "out")
		}
	}})
	res, err := r.Run(context.Background(), mustParse(t, `
jobs:
  build:
    steps:
      - name: Greet
        run: echo ok
        shell: bash
`), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Success {
		t.Fatal("workflow failed")
	}
	got := strings.Join(seq, ",")
	if got != "start:Greet,out,end:Greet:success" {
		t.Errorf("event sequence = %q, want start:Greet,out,end:Greet:success", got)
	}
}

// TestRunner_LiveOutputStreaming verifies that Options.Output receives a step's
// output live (tagged with job/step/stream) while the buffered StepReport is
// still populated.
func TestRunner_LiveOutputStreaming(t *testing.T) {
	f := system.NewFake()
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		// Simulate a process emitting output live to the engine-provided writer.
		if req.StdoutWriter != nil {
			_, _ = io.WriteString(req.StdoutWriter, "hello\n")
		}
		return system.ExecResult{Stdout: "hello\n", ExitCode: 0}, nil
	}

	var mu sync.Mutex
	var events []OutputEvent
	r := New(Options{
		System: f,
		Output: func(ev OutputEvent) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})

	wf := mustParse(t, `
jobs:
  build:
    steps:
      - id: greet
        run: echo hello
        shell: bash
`)
	res, err := r.Run(context.Background(), wf, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Success {
		t.Fatalf("workflow failed: %s", collectErrs(res.Jobs["build"].Steps))
	}

	// Live sink saw the output, tagged with job + step + stream.
	var live string
	for _, ev := range events {
		if ev.Job == "build" && ev.Step == "greet" && ev.Stream == StreamStdout {
			live += string(ev.Data)
		}
	}
	if live != "hello\n" {
		t.Errorf("live output = %q, want %q", live, "hello\n")
	}

	// Buffered result is still populated.
	if got := res.Jobs["build"].Steps[0].Stdout; got != "hello\n" {
		t.Errorf("buffered stdout = %q, want %q", got, "hello\n")
	}
}

// TestRunner_NoOutputSink confirms steps still run when no sink is configured
// (the teeing System is only inserted when Output is set).
func TestRunner_NoOutputSink(t *testing.T) {
	f := system.NewFake()
	f.ExecFunc = func(_ context.Context, req system.ExecRequest) (system.ExecResult, error) {
		if req.StdoutWriter != nil {
			t.Error("StdoutWriter should be nil when Output sink is not configured")
		}
		return system.ExecResult{Stdout: "ok\n", ExitCode: 0}, nil
	}
	r := New(Options{System: f}) // no Output
	res, err := r.Run(context.Background(), mustParse(t, `
jobs:
  build:
    steps:
      - run: echo ok
        shell: bash
`), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Success {
		t.Fatal("workflow failed")
	}
}
