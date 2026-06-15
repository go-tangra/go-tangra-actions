package expr

// Status values for steps, jobs and needs results. They mirror GitHub Actions
// vocabulary so conditions read the same way.
const (
	StatusSuccess   = "success"
	StatusFailure   = "failure"
	StatusSkipped   = "skipped"
	StatusCancelled = "cancelled"
)

// StepResult is what a previous step contributes to `steps.<id>`.
type StepResult struct {
	// Outcome is the raw result of the step (success|failure|skipped), before
	// continue-on-error is applied.
	Outcome string
	// Conclusion is the result after continue-on-error (a failed step with
	// continue-on-error has Conclusion == success).
	Conclusion string
	// Outputs are key/value strings the step published.
	Outputs map[string]string
}

// NeedResult is what an upstream job contributes to `needs.<job>`.
type NeedResult struct {
	Result  string
	Outputs map[string]string
}

// Context is the full set of data a condition or interpolation is evaluated
// against. The engine rebuilds it per step; it is never mutated during
// evaluation.
type Context struct {
	Env    map[string]string
	Inputs map[string]string
	Steps  map[string]StepResult
	Needs  map[string]NeedResult
	Matrix map[string]string

	// JobStatus is the cumulative status of the current job (success|failure|
	// cancelled); it backs success()/failure(). Empty is treated as success.
	JobStatus string
	// Cancelled backs cancelled(); set when the run's context was cancelled.
	Cancelled bool

	RunnerOS   string
	RunnerArch string
}

// activation converts the context into the variable bindings CEL evaluates
// against. Nested results are expressed as map[string]any so CEL's dyn handling
// can index them (steps.<id>.outcome, needs.<id>.outputs.<k>, ...).
func (c Context) activation() map[string]any {
	// outputs maps are lenient: referencing an output a step/job did not set
	// reads as "" (GitHub Actions semantics) rather than raising a runtime error.
	steps := make(map[string]any, len(c.Steps))
	for id, r := range c.Steps {
		steps[id] = map[string]any{
			"outcome":    r.Outcome,
			"conclusion": r.Conclusion,
			"outputs":    newTolerantMap(nonNil(r.Outputs)),
		}
	}
	needs := make(map[string]any, len(c.Needs))
	for id, r := range c.Needs {
		needs[id] = map[string]any{
			"result":  r.Result,
			"outputs": newTolerantMap(nonNil(r.Outputs)),
		}
	}
	return map[string]any{
		// env/inputs/matrix are lenient: a missing key reads as "" (GitHub
		// Actions semantics) instead of raising a runtime error.
		"env":    newTolerantMap(nonNil(c.Env)),
		"inputs": newTolerantMap(nonNil(c.Inputs)),
		"matrix": newTolerantMap(nonNil(c.Matrix)),
		"steps":  steps,
		"needs":  needs,
		"job":    map[string]string{"status": orDefault(c.JobStatus, StatusSuccess)},
		"runner": map[string]string{"os": c.RunnerOS, "arch": c.RunnerArch},
	}
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
