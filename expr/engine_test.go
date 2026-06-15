package expr

import (
	"strings"
	"testing"
)

func newEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func TestEval_EmptyIsTrue(t *testing.T) {
	e := newEngine(t)
	got, err := e.Eval("", Context{})
	if err != nil || !got {
		t.Fatalf("Eval(\"\") = %v, %v; want true, nil", got, err)
	}
}

func TestEval_Conditions(t *testing.T) {
	e := newEngine(t)
	ctx := Context{
		Env:    map[string]string{"STAGE": "prod"},
		Inputs: map[string]string{"domain": "example.com"},
		Steps: map[string]StepResult{
			"build": {Outcome: StatusSuccess, Conclusion: StatusSuccess, Outputs: map[string]string{"sha": "abc123"}},
			"lint":  {Outcome: StatusFailure, Conclusion: StatusFailure},
		},
		Needs: map[string]NeedResult{
			"setup": {Result: StatusSuccess, Outputs: map[string]string{"token": "t"}},
		},
		RunnerOS:   "linux",
		RunnerArch: "amd64",
	}

	tests := []struct {
		expr string
		want bool
	}{
		{`env.STAGE == 'prod'`, true},
		{`env.STAGE == 'dev'`, false},
		{`inputs.domain == 'example.com'`, true},
		{`inputs.domain.endsWith('.com')`, true},
		{`inputs.domain.startsWith('x')`, false},
		{`inputs.domain.contains('ample')`, true},
		{`steps.build.outcome == 'success'`, true},
		{`steps.lint.outcome == 'failure'`, true},
		{`steps.build.outputs.sha == 'abc123'`, true},
		{`needs.setup.result == 'success'`, true},
		{`needs.setup.outputs.token == 't'`, true},
		{`runner.os == 'linux'`, true},
		{`runner.arch == 'arm64'`, false},
		{`env.STAGE == 'prod' && steps.build.outcome == 'success'`, true},
		{`env.STAGE == 'dev' || steps.lint.outcome == 'failure'`, true},
		{`!(steps.build.outcome == 'failure')`, true},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got, err := e.Eval(tt.expr, ctx)
			if err != nil {
				t.Fatalf("Eval(%q): %v", tt.expr, err)
			}
			if got != tt.want {
				t.Errorf("Eval(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEval_StatusFunctions(t *testing.T) {
	e := newEngine(t)
	tests := []struct {
		name      string
		expr      string
		jobStatus string
		cancelled bool
		want      bool
	}{
		{"success when success", "success()", StatusSuccess, false, true},
		{"success default empty", "success()", "", false, true},
		{"success false on failure", "success()", StatusFailure, false, false},
		{"success false when cancelled", "success()", StatusSuccess, true, false},
		{"failure true on failure", "failure()", StatusFailure, false, true},
		{"failure false on success", "failure()", StatusSuccess, false, false},
		{"always true on success", "always()", StatusSuccess, false, true},
		{"always true on failure", "always()", StatusFailure, false, true},
		{"always true when cancelled", "always()", StatusSuccess, true, true},
		{"cancelled true", "cancelled()", StatusSuccess, true, true},
		{"cancelled false", "cancelled()", StatusSuccess, false, false},
		{"combined failure or always", "failure() || always()", StatusFailure, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.Eval(tt.expr, Context{JobStatus: tt.jobStatus, Cancelled: tt.cancelled})
			if err != nil {
				t.Fatalf("Eval(%q): %v", tt.expr, err)
			}
			if got != tt.want {
				t.Errorf("Eval(%q) [status=%q cancelled=%v] = %v, want %v",
					tt.expr, tt.jobStatus, tt.cancelled, got, tt.want)
			}
		})
	}
}

func TestEval_StatusIsolatedAcrossCalls(t *testing.T) {
	// Status state must not leak between evaluations.
	en := newEngine(t)
	if got, _ := en.Eval("failure()", Context{JobStatus: StatusFailure}); !got {
		t.Fatal("expected failure() true")
	}
	if got, _ := en.Eval("success()", Context{JobStatus: StatusSuccess}); !got {
		t.Fatal("expected success() true after a prior failure() eval (state leaked)")
	}
}

func TestEval_Errors(t *testing.T) {
	en := newEngine(t)
	tests := []struct {
		name string
		expr string
	}{
		{"non-boolean result", `env.STAGE`},
		{"syntax error", `env.STAGE ==`},
		{"unknown identifier", `bogusvar == 1`},
		{"missing step key", `steps.nope.outcome == 'success'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := en.Eval(tt.expr, Context{Env: map[string]string{"STAGE": "prod"}})
			if err == nil {
				t.Errorf("Eval(%q) = nil error, want error", tt.expr)
			}
		})
	}
}

func TestEval_MissingContextKeyIsEmptyNotError(t *testing.T) {
	// GitHub Actions semantics: an undefined env/inputs/matrix key reads as ""
	// rather than raising an error, so the condition is simply false.
	en := newEngine(t)
	ctx := Context{Env: map[string]string{"SET": "yes"}}
	tests := []struct {
		expr string
		want bool
	}{
		{`env.UNSET == 'true'`, false},
		{`env.UNSET == ''`, true},
		{`env.SET == 'yes'`, true},
		{`inputs.nope == ''`, true},
		{`matrix.absent == ''`, true},
		// `in` stays honest about real presence.
		{`'UNSET' in env`, false},
		{`'SET' in env`, true},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got, err := en.Eval(tt.expr, ctx)
			if err != nil {
				t.Fatalf("Eval(%q): %v", tt.expr, err)
			}
			if got != tt.want {
				t.Errorf("Eval(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEval_NoCodeInjectionFromContextValues(t *testing.T) {
	// A malicious value placed in a context field must be treated purely as
	// data — never compiled as part of the expression.
	en := newEngine(t)
	ctx := Context{Env: map[string]string{
		"EVIL": `' == '' || '1' == '1`, // would flip logic if concatenated into source
	}}
	got, err := en.Eval(`env.EVIL == 'prod'`, ctx)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got {
		t.Error("context value was interpreted as code (injection!)")
	}
	// And the literal comparison against the exact payload is just a data match.
	got, err = en.Eval(`env.EVIL == "' == '' || '1' == '1"`, ctx)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if !got {
		t.Error("expected literal data comparison to hold")
	}
}

func TestValidate(t *testing.T) {
	en := newEngine(t)
	if err := en.Validate(""); err != nil {
		t.Errorf("Validate(\"\") = %v", err)
	}
	if err := en.Validate("success() && env.X == 'y'"); err != nil {
		t.Errorf("Validate valid expr = %v", err)
	}
	if err := en.Validate("=="); err == nil {
		t.Error("Validate invalid expr = nil, want error")
	}
}

func TestProgramCache(t *testing.T) {
	en := newEngine(t)
	for range 5 {
		if _, err := en.Eval("always()", Context{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(en.progCache) != 1 {
		t.Errorf("progCache size = %d, want 1 (cached)", len(en.progCache))
	}
}

func TestInterpolate(t *testing.T) {
	en := newEngine(t)
	ctx := Context{
		Env:    map[string]string{"STAGE": "prod"},
		Inputs: map[string]string{"domain": "example.com", "count": "3"},
		Steps:  map[string]StepResult{"b": {Outputs: map[string]string{"sha": "deadbeef"}}},
	}
	tests := []struct {
		in   string
		want string
	}{
		{"no expression here", "no expression here"},
		{"server_name ${{ inputs.domain }};", "server_name example.com;"},
		{"${{ env.STAGE }}", "prod"},
		{"a=${{ inputs.domain }} b=${{ env.STAGE }}", "a=example.com b=prod"},
		{"sha=${{ steps.b.outputs.sha }}", "sha=deadbeef"},
		{"eq=${{ env.STAGE == 'prod' }}", "eq=true"},
		{"sum=${{ 1 + 2 }}", "sum=3"},
		{"concat=${{ 'a' + 'b' }}", "concat=ab"},
		{"upper=${{ inputs.domain.startsWith('ex') }}", "upper=true"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := en.Interpolate(tt.in, ctx)
			if err != nil {
				t.Fatalf("Interpolate(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Interpolate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestInterpolate_Errors(t *testing.T) {
	en := newEngine(t)
	tests := []struct {
		name string
		in   string
	}{
		{"unterminated", "x ${{ inputs.domain"},
		{"empty expr", "x ${{   }} y"},
		{"bad expr", "x ${{ == }} y"},
		{"composite result", "x ${{ steps }} y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := en.Interpolate(tt.in, Context{Inputs: map[string]string{"domain": "d"}})
			if err == nil {
				t.Errorf("Interpolate(%q) = nil error, want error", tt.in)
			}
		})
	}
}

func TestInterpolate_TooMany(t *testing.T) {
	en := newEngine(t)
	in := strings.Repeat("${{ 1 }}", maxInterpolations+1)
	if _, err := en.Interpolate(in, Context{}); err == nil {
		t.Error("expected too-many-interpolations error")
	}
}
