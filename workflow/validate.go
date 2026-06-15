package workflow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// identRe matches job/step ids and input names. Ids may contain dashes (CEL
// addresses them via bracket indexing, e.g. steps["my-step"]); they must start
// with a letter or underscore.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// envNameRe matches environment variable names (no dashes — these become real
// process env keys and CEL identifiers `env.<KEY>`).
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// inputNameRe matches workflow input names, addressed as `inputs.<name>`.
var inputNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidationError aggregates every problem found in a workflow so the caller
// sees all of them at once instead of one-at-a-time.
type ValidationError struct {
	Errors []string
}

func (e *ValidationError) Error() string {
	if len(e.Errors) == 1 {
		return "invalid workflow: " + e.Errors[0]
	}
	return fmt.Sprintf("invalid workflow (%d problems):\n  - %s",
		len(e.Errors), strings.Join(e.Errors, "\n  - "))
}

// Validate checks structural and referential integrity: jobs/steps exist, ids
// are well-formed and unique, each step is exactly one of run/uses, env/input
// names are legal, and `needs` references resolve without cycles. It returns a
// *ValidationError listing every problem, or nil.
func (wf *Workflow) Validate() error {
	var errs []string

	for name := range wf.Inputs {
		if !inputNameRe.MatchString(name) {
			errs = append(errs, fmt.Sprintf("input %q: invalid name", name))
		}
	}
	for k := range wf.Env {
		if !envNameRe.MatchString(k) {
			errs = append(errs, fmt.Sprintf("env %q: invalid environment variable name", k))
		}
	}

	if len(wf.Jobs) == 0 {
		errs = append(errs, "must define at least one job")
		return finish(errs)
	}

	for _, jobID := range sortedKeys(wf.Jobs) {
		job := wf.Jobs[jobID]
		errs = append(errs, validateJob(jobID, job)...)
	}
	errs = append(errs, validateNeeds(wf)...)

	return finish(errs)
}

func validateJob(jobID string, job Job) []string {
	var errs []string
	prefix := "job " + jobID

	if !identRe.MatchString(jobID) {
		errs = append(errs, prefix+": invalid job id")
	}
	for k := range job.Env {
		if !envNameRe.MatchString(k) {
			errs = append(errs, fmt.Sprintf("%s: env %q: invalid environment variable name", prefix, k))
		}
	}
	if len(job.Steps) == 0 {
		errs = append(errs, prefix+": must define at least one step")
	}

	seenStepID := map[string]bool{}
	for i, step := range job.Steps {
		errs = append(errs, validateStep(prefix, i, step, seenStepID)...)
	}
	return errs
}

func validateStep(jobPrefix string, idx int, step Step, seenStepID map[string]bool) []string {
	var errs []string
	prefix := fmt.Sprintf("%s, step %d", jobPrefix, idx)
	if step.Name != "" {
		prefix = fmt.Sprintf("%s (%s)", prefix, step.Name)
	}

	switch {
	case step.Run == "" && step.Uses == "":
		errs = append(errs, prefix+": must set either `run` or `uses`")
	case step.Run != "" && step.Uses != "":
		errs = append(errs, prefix+": `run` and `uses` are mutually exclusive")
	}
	if step.Run == "" && step.Shell != "" {
		errs = append(errs, prefix+": `shell` is only valid with `run`")
	}
	if step.TimeoutSeconds < 0 {
		errs = append(errs, prefix+": timeout-seconds must not be negative")
	}

	if step.ID != "" {
		if !identRe.MatchString(step.ID) {
			errs = append(errs, prefix+": invalid step id "+quote(step.ID))
		}
		if seenStepID[step.ID] {
			errs = append(errs, prefix+": duplicate step id "+quote(step.ID))
		}
		seenStepID[step.ID] = true
	}
	for k := range step.Env {
		if !envNameRe.MatchString(k) {
			errs = append(errs, fmt.Sprintf("%s: env %q: invalid environment variable name", prefix, k))
		}
	}
	return errs
}

// validateNeeds verifies every `needs` reference resolves to a real job, no job
// depends on itself, and the dependency graph is acyclic.
func validateNeeds(wf *Workflow) []string {
	var errs []string
	for _, jobID := range sortedKeys(wf.Jobs) {
		job := wf.Jobs[jobID]
		for _, dep := range job.Needs {
			if dep == jobID {
				errs = append(errs, "job "+jobID+": cannot depend on itself")
				continue
			}
			if _, ok := wf.Jobs[dep]; !ok {
				errs = append(errs, fmt.Sprintf("job %s: needs unknown job %q", jobID, dep))
			}
		}
	}
	if len(errs) == 0 {
		if cycle := findCycle(wf); len(cycle) > 0 {
			errs = append(errs, "dependency cycle: "+strings.Join(cycle, " -> "))
		}
	}
	return errs
}

// findCycle returns a job-id path describing a cycle in the `needs` graph, or
// nil if the graph is acyclic.
func findCycle(wf *Workflow) []string {
	const (
		white = 0 // unvisited
		gray  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := map[string]int{}
	var path []string

	var dfs func(string) []string
	dfs = func(id string) []string {
		color[id] = gray
		path = append(path, id)
		for _, dep := range wf.Jobs[id].Needs {
			if _, ok := wf.Jobs[dep]; !ok {
				continue
			}
			switch color[dep] {
			case white:
				if c := dfs(dep); c != nil {
					return c
				}
			case gray:
				return append(append([]string{}, path...), dep)
			}
		}
		path = path[:len(path)-1]
		color[id] = black
		return nil
	}

	for _, id := range sortedKeys(wf.Jobs) {
		if color[id] == white {
			if c := dfs(id); c != nil {
				return c
			}
		}
	}
	return nil
}

// TopoOrder returns job ids in a deterministic execution order honouring
// `needs` (dependencies first), with ties broken by id. Duplicate `needs`
// entries are collapsed, so a job is emitted exactly once. It assumes the
// workflow has been validated (acyclic); on an unexpected cycle it returns the
// jobs it could order plus an error. Runs in O(jobs + edges) via Kahn's
// algorithm.
func (wf *Workflow) TopoOrder() ([]string, error) {
	indeg := make(map[string]int, len(wf.Jobs))
	dependents := make(map[string][]string, len(wf.Jobs))
	for id := range wf.Jobs {
		indeg[id] = 0
	}
	for _, id := range sortedKeys(wf.Jobs) {
		seen := map[string]bool{}
		for _, dep := range wf.Jobs[id].Needs {
			if _, ok := wf.Jobs[dep]; !ok || seen[dep] {
				continue // unknown or duplicate edge
			}
			seen[dep] = true
			indeg[id]++
			dependents[dep] = append(dependents[dep], id)
		}
	}

	var ready []string
	for _, id := range sortedKeys(wf.Jobs) {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}

	order := make([]string, 0, len(wf.Jobs))
	for len(ready) > 0 {
		sort.Strings(ready)
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, dep := range dependents[id] {
			indeg[dep]--
			if indeg[dep] == 0 {
				ready = append(ready, dep)
			}
		}
	}

	if len(order) != len(wf.Jobs) {
		return order, fmt.Errorf("workflow has a dependency cycle")
	}
	return order, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func quote(s string) string { return "\"" + s + "\"" }

func finish(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: errs}
}
