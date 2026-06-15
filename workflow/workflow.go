// Package workflow defines the data model for a go-tangra-actions workflow and
// parses/validates it from YAML. A workflow is a set of jobs; each job is an
// ordered list of steps. The model mirrors a deliberately small subset of
// GitHub Actions: per-step/per-job conditions, an action reference (`uses`) with
// inputs (`with`), or an inline command (`run`).
//
// The model is pure data — it performs no execution and imports nothing beyond
// the standard library and a YAML parser, so it is cheap to test and safe to
// share across the engine, the agent, and the orchestrator.
package workflow

// Workflow is the top-level unit submitted for execution.
type Workflow struct {
	// Name is a human-readable label (optional).
	Name string `yaml:"name"`
	// Inputs are run-time parameters, addressable as `inputs.<name>` in
	// conditions and `${{ inputs.<name> }}` interpolation.
	Inputs map[string]Input `yaml:"inputs,omitempty"`
	// Env holds workflow-wide environment variables, inherited by every job and
	// step (step/job env override on key collision).
	Env map[string]string `yaml:"env,omitempty"`
	// Jobs are keyed by job id. Execution order is the topological order implied
	// by `needs`; ties are broken by job id for determinism.
	Jobs map[string]Job `yaml:"jobs"`
}

// Input declares a single workflow parameter.
type Input struct {
	Description string `yaml:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
	Default     string `yaml:"default,omitempty"`
}

// Job is an ordered list of steps with optional dependencies and a condition.
type Job struct {
	Name string `yaml:"name,omitempty"`
	// Needs lists job ids that must complete before this job starts. Referenced
	// as `needs.<id>.result` / `needs.<id>.outputs.<name>` in conditions.
	Needs []string `yaml:"needs,omitempty"`
	// If is a CEL condition gating the whole job. Empty means "run".
	If string `yaml:"if,omitempty"`
	// Env holds job-level environment, layered over workflow env.
	Env   map[string]string `yaml:"env,omitempty"`
	Steps []Step            `yaml:"steps"`
}

// Step is a single unit of work. Exactly one of Uses or Run must be set.
type Step struct {
	// ID is optional but required to reference this step's outcome/outputs from
	// a later condition (`steps.<id>....`).
	ID   string `yaml:"id,omitempty"`
	Name string `yaml:"name,omitempty"`
	// If is a CEL condition. Empty defaults to `success()` (run only if no
	// earlier step in the job has failed).
	If string `yaml:"if,omitempty"`
	// Uses names a registered action (e.g. "package", "file", "service").
	Uses string `yaml:"uses,omitempty"`
	// With supplies inputs to the action named by Uses. Values may contain
	// `${{ }}` interpolation.
	With map[string]string `yaml:"with,omitempty"`
	// Run is an inline shell command; shorthand for the builtin "run" action.
	// Mutually exclusive with Uses.
	Run string `yaml:"run,omitempty"`
	// Shell selects the interpreter for Run (e.g. "bash", "sh"). Default "sh".
	Shell string `yaml:"shell,omitempty"`
	// Env holds step-level environment, layered over job and workflow env.
	Env map[string]string `yaml:"env,omitempty"`
	// ContinueOnError lets the job proceed (and reports the step's conclusion as
	// success) even when the step fails.
	ContinueOnError bool `yaml:"continue-on-error,omitempty"`
	// TimeoutSeconds bounds the step's execution. 0 means the engine default.
	TimeoutSeconds int `yaml:"timeout-seconds,omitempty"`
}

// IsRun reports whether the step is an inline command rather than a named
// action.
func (s Step) IsRun() bool { return s.Run != "" }
