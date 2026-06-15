package expr

import "testing"

// FuzzInterpolate ensures arbitrary input never panics the interpolator; it may
// return an error, but must not crash or hang.
func FuzzInterpolate(f *testing.F) {
	seeds := []string{
		"",
		"plain text",
		"${{ inputs.x }}",
		"${{",
		"}}",
		"${{ }}",
		"${{ ${{ }} }}",
		"${{ 1 + 1 }}${{ env.X }}",
		"a ${{ 'b' }} c ${{",
		"${{ steps }}",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	en, err := NewEngine()
	if err != nil {
		f.Fatalf("NewEngine: %v", err)
	}
	ctx := Context{
		Env:    map[string]string{"X": "v"},
		Inputs: map[string]string{"x": "y"},
	}
	f.Fuzz(func(t *testing.T, s string) {
		// Bound input length so the fuzzer doesn't chase pathological sizes.
		if len(s) > 4096 {
			return
		}
		_, _ = en.Interpolate(s, ctx)
	})
}

// FuzzEval ensures arbitrary expression text never panics the evaluator.
func FuzzEval(f *testing.F) {
	for _, s := range []string{"", "true", "success()", "env.X == 'v'", "1 +", "((", "steps.a.b.c"} {
		f.Add(s)
	}
	en, err := NewEngine()
	if err != nil {
		f.Fatalf("NewEngine: %v", err)
	}
	ctx := Context{Env: map[string]string{"X": "v"}}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 1024 {
			return
		}
		_, _ = en.Eval(s, ctx)
	})
}
