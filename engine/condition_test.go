package engine

import "testing"

func TestReferencesStatusFunc(t *testing.T) {
	yes := []string{
		"success()",
		"failure()",
		"always()",
		"cancelled()",
		"failure() || always()",
		"env.X == '1' && success()",
		"  success ()", // space before paren
		"!cancelled()",
	}
	for _, e := range yes {
		if !referencesStatusFunc(e) {
			t.Errorf("referencesStatusFunc(%q) = false, want true", e)
		}
	}

	no := []string{
		"",
		"env.X == '1'",
		"steps.a.outcome == 'success'",          // 'success' is a string literal
		"inputs.mode == 'failure'",              // literal
		"env.MSG == 'always do this'",           // literal containing always
		"mysuccess()",                           // not the identifier
		"success_flag == 'true'",                // identifier, not a call
		"steps.always.outputs.x == 'cancelled'", // 'always' is a step id, 'cancelled' a literal
	}
	for _, e := range no {
		if referencesStatusFunc(e) {
			t.Errorf("referencesStatusFunc(%q) = true, want false", e)
		}
	}
}

func TestBlankStringLiterals(t *testing.T) {
	in := `a == 'success()' && b == "failure()"`
	got := blankStringLiterals(in)
	if referencesStatusFunc(got) {
		t.Errorf("status words inside literals should be blanked: %q", got)
	}
	// Length is preserved so positions are stable.
	if len(got) != len(in) {
		t.Errorf("length changed: %d vs %d", len(got), len(in))
	}
}
