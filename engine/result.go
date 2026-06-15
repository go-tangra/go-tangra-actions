package engine

import "github.com/go-tangra/go-tangra-actions/expr"

// Result statuses, re-exported from expr so callers need not import both.
const (
	StatusSuccess = expr.StatusSuccess
	StatusFailure = expr.StatusFailure
	StatusSkipped = expr.StatusSkipped
)

// StepReport records the outcome of a single executed (or skipped) step.
type StepReport struct {
	ID   string
	Name string
	// Outcome is the raw result: success | failure | skipped.
	Outcome string
	// Conclusion is the result after continue-on-error: a failed step with
	// continue-on-error reports Conclusion == success.
	Conclusion string
	// Outputs are the (secret-masked) key/values the action published.
	Outputs map[string]string
	Stdout  string
	Stderr  string
	// Err is the (secret-masked) error message when the step failed, else "".
	Err string
	// Uses is the action reference this step invoked (empty for an inline `run`).
	Uses string
	// Steps holds the reports of a composite action's internal steps, when this
	// step invoked one; nil for native actions and inline runs.
	Steps []StepReport
}

// JobReport records the outcome of a job and its steps.
type JobReport struct {
	ID string
	// Result is success | failure | skipped for the job as a whole.
	Result string
	Steps  []StepReport
	// Outputs aggregates the outputs published by the job's steps, addressable
	// downstream as needs.<job>.outputs.<key>.
	Outputs map[string]string
}

// RunResult is the outcome of executing a whole workflow.
type RunResult struct {
	// Jobs is keyed by job id; JobOrder gives the execution order.
	Jobs     map[string]*JobReport
	JobOrder []string
	// Success is true when no job failed.
	Success bool
}
