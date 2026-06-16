package action

import (
	"bytes"
	"context"
	"testing"
)

func TestLog(t *testing.T) {
	// Writes the message (plus newline) to the live writer and to Result.Stdout.
	var live bytes.Buffer
	res, err := (&Log{}).Run(context.Background(), Input{
		With:   map[string]string{"message": "system upgraded via apt (12 packages)"},
		Stdout: &live,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "system upgraded via apt (12 packages)\n"
	if live.String() != want {
		t.Errorf("live = %q, want %q", live.String(), want)
	}
	if res.Stdout != want {
		t.Errorf("Result.Stdout = %q, want %q", res.Stdout, want)
	}
	if res.Changed {
		t.Error("log must not report Changed")
	}
}

func TestLog_NoLiveWriter(t *testing.T) {
	// With no live writer wired (e.g. no output sink), it still returns Stdout.
	res, err := (&Log{}).Run(context.Background(), Input{
		With: map[string]string{"message": "hello"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "hello\n" {
		t.Errorf("Result.Stdout = %q, want %q", res.Stdout, "hello\n")
	}
}
