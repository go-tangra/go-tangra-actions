package workflow

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ActionDef is a reusable, externally-defined action — the analogue of a
// GitHub Actions `action.yml`. It declares typed inputs and outputs and a
// `runs` block, which is either a composite (a list of steps reusing other
// actions) or a scripted action (a `main` entry script executed by a host
// ScriptRuntime, e.g. JavaScript or Lua). There is no container runtime.
type ActionDef struct {
	Name        string                  `yaml:"name,omitempty"`
	Description string                  `yaml:"description,omitempty"`
	Inputs      map[string]Input        `yaml:"inputs,omitempty"`
	Outputs     map[string]ActionOutput `yaml:"outputs,omitempty"`
	Runs        Runs                    `yaml:"runs"`
}

// ActionOutput declares an output the action publishes. For a composite action,
// Value is a `${{ }}` expression evaluated against the action's internal step
// context after the steps run (e.g. "${{ steps.install.outputs.packages }}") and
// is required. For a scripted action, outputs are set by the script at runtime
// (setOutput), so Value is optional and serves only as documentation.
type ActionOutput struct {
	Description string `yaml:"description,omitempty"`
	Value       string `yaml:"value,omitempty"`
}

// UsingComposite is the runner kind for composite (step-list) actions. Any
// other non-empty `using` denotes a scripted action whose `main` script is
// executed by a host-provided ScriptRuntime (the library does not fix the set
// of script languages — the consumer's runtime decides what it supports).
const UsingComposite = "composite"

// Runs describes how an action executes — either composite (Steps) or scripted
// (Main).
type Runs struct {
	// Using selects the runner kind: "composite", or a script language such as
	// "javascript"/"lua" understood by the configured ScriptRuntime.
	Using string `yaml:"using"`
	// Steps is the composite step list, executed in order like a job's steps.
	// Valid only when Using == composite.
	Steps []Step `yaml:"steps,omitempty"`
	// Main is the entry script path within the action package, relative to the
	// package root. Required for (and valid only for) a scripted action.
	Main string `yaml:"main,omitempty"`
}

// IsComposite reports whether the action is a composite (step-list) action.
func (r Runs) IsComposite() bool { return r.Using == UsingComposite }

// ParseAction decodes an action manifest from YAML and validates it. Unknown
// fields are rejected (strict decoding).
func ParseAction(data []byte) (*ActionDef, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var def ActionDef
	if err := dec.Decode(&def); err != nil {
		return nil, fmt.Errorf("parse action: %w", err)
	}
	if err := def.Validate(); err != nil {
		return nil, err
	}
	return &def, nil
}

// Validate checks the manifest: a supported runner, well-formed input/output
// names, and a non-empty, structurally-valid step list. It returns a
// *ValidationError listing every problem, or nil.
func (d *ActionDef) Validate() error {
	var errs []string

	for name := range d.Inputs {
		if !inputNameRe.MatchString(name) {
			errs = append(errs, fmt.Sprintf("input %q: invalid name", name))
		}
	}
	for name := range d.Outputs {
		if !inputNameRe.MatchString(name) {
			errs = append(errs, fmt.Sprintf("output %q: invalid name", name))
		}
	}

	switch {
	case d.Runs.Using == "":
		errs = append(errs, "runs.using is required")
	case d.Runs.IsComposite():
		// Composite: a step list, no script entry. Each output's value is a
		// `${{ }}` expression over the action's steps, so it must be present.
		if d.Runs.Main != "" {
			errs = append(errs, "runs.main is not valid for a composite action")
		}
		if len(d.Runs.Steps) == 0 {
			errs = append(errs, "runs.steps must define at least one step")
		}
		for name, out := range d.Outputs {
			if out.Value == "" {
				errs = append(errs, fmt.Sprintf("output %q: missing value", name))
			}
		}
		seenStepID := map[string]bool{}
		for i, step := range d.Runs.Steps {
			errs = append(errs, validateStep("action", i, step, seenStepID)...)
		}
	default:
		// Scripted: a main entry, no step list. Whether a runtime supports the
		// `using` language is a run-time concern (the ScriptRuntime decides).
		if d.Runs.Main == "" {
			errs = append(errs, fmt.Sprintf("runs.main is required for using=%q", d.Runs.Using))
		}
		if len(d.Runs.Steps) > 0 {
			errs = append(errs, "runs.steps is only valid for a composite action")
		}
	}

	return finish(errs)
}
