package workflow

import (
	"errors"
	"strings"
	"testing"
)

func TestParse_Valid(t *testing.T) {
	src := `
name: provision
inputs:
  domain:
    default: example.com
    required: true
env:
  STAGE: prod
jobs:
  build:
    steps:
      - id: pkg
        name: install
        uses: package
        with: { name: nginx, state: present }
  deploy:
    needs: [build]
    if: success()
    steps:
      - name: restart
        if: steps.pkg.outcome == 'success'
        uses: service
        with: { name: nginx, state: restarted }
      - name: log
        run: echo done
        shell: bash
`
	wf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if wf.Name != "provision" {
		t.Errorf("Name = %q, want provision", wf.Name)
	}
	if got := wf.Inputs["domain"].Default; got != "example.com" {
		t.Errorf("inputs.domain.default = %q", got)
	}
	if !wf.Inputs["domain"].Required {
		t.Error("inputs.domain.required = false, want true")
	}
	if wf.Env["STAGE"] != "prod" {
		t.Errorf("env.STAGE = %q", wf.Env["STAGE"])
	}
	if len(wf.Jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2", len(wf.Jobs))
	}
	if got := wf.Jobs["deploy"].Needs; len(got) != 1 || got[0] != "build" {
		t.Errorf("deploy.needs = %v", got)
	}
	if !wf.Jobs["deploy"].Steps[1].IsRun() {
		t.Error("deploy step 1 should be a run step")
	}
	if wf.Jobs["deploy"].Steps[1].Shell != "bash" {
		t.Errorf("shell = %q, want bash", wf.Jobs["deploy"].Steps[1].Shell)
	}
}

func TestParse_StrictUnknownField(t *testing.T) {
	src := `
jobs:
  build:
    steps:
      - run: echo hi
        bogus: nope
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should mention unknown field: %v", err)
	}
}

func TestParse_MalformedYAML(t *testing.T) {
	_, err := Parse([]byte("jobs: [unbalanced"))
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestValidate_Errors(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "no jobs",
			src:     "name: empty\n",
			wantSub: "at least one job",
		},
		{
			name: "job with no steps",
			src: `
jobs:
  a:
    steps: []
`,
			wantSub: "at least one step",
		},
		{
			name: "step neither run nor uses",
			src: `
jobs:
  a:
    steps:
      - name: nothing
`,
			wantSub: "either `run` or `uses`",
		},
		{
			name: "step both run and uses",
			src: `
jobs:
  a:
    steps:
      - run: echo hi
        uses: package
`,
			wantSub: "mutually exclusive",
		},
		{
			name: "shell without run",
			src: `
jobs:
  a:
    steps:
      - uses: package
        with: { name: x }
        shell: bash
`,
			wantSub: "`shell` is only valid with `run`",
		},
		{
			name: "duplicate step id",
			src: `
jobs:
  a:
    steps:
      - id: s
        run: echo 1
      - id: s
        run: echo 2
`,
			wantSub: "duplicate step id",
		},
		{
			name: "invalid env name",
			src: `
env:
  "bad-key": value
jobs:
  a:
    steps:
      - run: echo hi
`,
			wantSub: "invalid environment variable name",
		},
		{
			name: "invalid input name",
			src: `
inputs:
  "bad name":
    default: x
jobs:
  a:
    steps:
      - run: echo hi
`,
			wantSub: "invalid name",
		},
		{
			name: "needs unknown job",
			src: `
jobs:
  a:
    needs: [ghost]
    steps:
      - run: echo hi
`,
			wantSub: `needs unknown job "ghost"`,
		},
		{
			name: "self dependency",
			src: `
jobs:
  a:
    needs: [a]
    steps:
      - run: echo hi
`,
			wantSub: "cannot depend on itself",
		},
		{
			name: "negative timeout",
			src: `
jobs:
  a:
    steps:
      - run: echo hi
        timeout-seconds: -5
`,
			wantSub: "timeout-seconds must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.src))
			if err == nil {
				t.Fatalf("expected validation error containing %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantSub)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("error type = %T, want *ValidationError", err)
			}
		})
	}
}

func TestValidate_CycleDetection(t *testing.T) {
	src := `
jobs:
  a:
    needs: [c]
    steps: [{run: echo a}]
  b:
    needs: [a]
    steps: [{run: echo b}]
  c:
    needs: [b]
    steps: [{run: echo c}]
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error = %q, want cycle", err.Error())
	}
}

func TestValidate_AggregatesMultiple(t *testing.T) {
	src := `
jobs:
  a:
    steps:
      - name: bad
`
	// step with neither run nor uses; one job ok otherwise.
	_, err := Parse([]byte(src))
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	if len(ve.Errors) < 1 {
		t.Errorf("expected at least one aggregated error")
	}
}

func TestTopoOrder(t *testing.T) {
	src := `
jobs:
  deploy:
    needs: [build, test]
    steps: [{run: echo deploy}]
  build:
    steps: [{run: echo build}]
  test:
    needs: [build]
    steps: [{run: echo test}]
`
	wf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	order, err := wf.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if pos["build"] > pos["test"] {
		t.Errorf("build should precede test: %v", order)
	}
	if pos["test"] > pos["deploy"] || pos["build"] > pos["deploy"] {
		t.Errorf("deploy should be last: %v", order)
	}
	// Determinism: build has no deps and sorts first.
	if order[0] != "build" {
		t.Errorf("order[0] = %q, want build (deterministic tie-break): %v", order[0], order)
	}
}

func TestTopoOrder_DuplicateNeedsEmittedOnce(t *testing.T) {
	// Duplicate `needs` entries must not cause the dependent job to be emitted
	// (and later executed) more than once.
	src := `
jobs:
  build:
    steps: [{run: echo build}]
  deploy:
    needs: [build, build]
    steps: [{run: echo deploy}]
`
	wf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	order, err := wf.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	if len(order) != 2 {
		t.Fatalf("order = %v, want exactly 2 jobs (no duplicates)", order)
	}
	seen := map[string]int{}
	for _, id := range order {
		seen[id]++
	}
	if seen["deploy"] != 1 || seen["build"] != 1 {
		t.Errorf("each job should appear once: %v", order)
	}
}

func TestTopoOrder_Deterministic(t *testing.T) {
	src := `
jobs:
  z:
    steps: [{run: echo z}]
  a:
    steps: [{run: echo a}]
  m:
    steps: [{run: echo m}]
`
	wf, _ := Parse([]byte(src))
	first, _ := wf.TopoOrder()
	for range 20 {
		got, _ := wf.TopoOrder()
		if strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("non-deterministic order: %v vs %v", first, got)
		}
	}
	if strings.Join(first, ",") != "a,m,z" {
		t.Errorf("order = %v, want [a m z]", first)
	}
}
