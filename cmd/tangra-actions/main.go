// Command tangra-actions runs a go-tangra-actions workflow file against the
// local host. It is a thin CLI around the engine: parse the YAML, run it, print
// each job/step outcome, and exit non-zero if anything failed.
//
//	tangra-actions [flags] <workflow.yaml>
//	  -input k=v     workflow input (repeatable)
//	  -actions DIR   directory of external action packages (enables `uses:`)
//	  -confine ROOT  restrict file actions to this directory
//	  -secret VALUE  value to mask in output (repeatable)
//
// Builtin actions (run/package/file/file_line/service/hostname/timezone) and composite actions work out of
// the box. Scripted (JS/Lua) actions need a ScriptRuntime, which this binary
// does not embed — they will report "no script runtime". A host that has a
// script engine (e.g. go-tangra-client) wires one in via engine.Options.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/go-tangra/go-tangra-actions/engine"
	"github.com/go-tangra/go-tangra-actions/jsruntime"
	"github.com/go-tangra/go-tangra-actions/workflow"
)

func main() {
	cfg, path, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		usage()
		os.Exit(2)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fatal("read workflow:", err)
	}
	wf, err := workflow.Parse(data)
	if err != nil {
		fatal("parse workflow:", err)
	}

	opts := engine.Options{ConfineRoot: cfg.confine, Secrets: cfg.secrets}
	if cfg.actionsDir != "" {
		opts.Resolver = engine.DirResolver{Root: cfg.actionsDir}
	}
	if !cfg.noJS {
		opts.ScriptRuntime = jsruntime.New()
	}
	runner := engine.New(opts)

	// Cancel the run on Ctrl-C / SIGTERM so in-flight actions are interrupted.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, err := runner.Run(ctx, wf, cfg.inputs)
	if err != nil {
		fatal("run:", err)
	}

	printResult(result)
	if !result.Success {
		os.Exit(1)
	}
}

type config struct {
	inputs     map[string]string
	secrets    []string
	actionsDir string
	confine    string
	noJS       bool
}

// parseArgs is a tiny hand-rolled parser so repeated -input/-secret flags and a
// single positional workflow path are easy to express.
func parseArgs(args []string) (config, string, error) {
	cfg := config{inputs: map[string]string{}}
	var path string

	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", a)
			}
			i++
			return args[i], nil
		}
		switch a {
		case "-h", "--help":
			usage()
			os.Exit(0)
		case "-input", "--input", "-i":
			v, err := next()
			if err != nil {
				return cfg, "", err
			}
			k, val, ok := strings.Cut(v, "=")
			if !ok || k == "" {
				return cfg, "", fmt.Errorf("input %q must be key=value", v)
			}
			cfg.inputs[k] = val
		case "-secret", "--secret":
			v, err := next()
			if err != nil {
				return cfg, "", err
			}
			cfg.secrets = append(cfg.secrets, v)
		case "-actions", "--actions":
			v, err := next()
			if err != nil {
				return cfg, "", err
			}
			cfg.actionsDir = v
		case "-confine", "--confine":
			v, err := next()
			if err != nil {
				return cfg, "", err
			}
			cfg.confine = v
		case "-no-js", "--no-js":
			cfg.noJS = true
		default:
			if strings.HasPrefix(a, "-") {
				return cfg, "", fmt.Errorf("unknown flag %q", a)
			}
			if path != "" {
				return cfg, "", fmt.Errorf("multiple workflow files given (%q and %q)", path, a)
			}
			path = a
		}
	}
	if path == "" {
		return cfg, "", fmt.Errorf("no workflow file given")
	}
	return cfg, path, nil
}

func printResult(r *engine.RunResult) {
	for _, jobID := range r.JobOrder {
		job := r.Jobs[jobID]
		fmt.Printf("%s job %s\n", mark(job.Result), jobID)
		printSteps(job.Steps, "  ")
	}
	fmt.Println()
	if r.Success {
		fmt.Println("✓ workflow succeeded")
	} else {
		fmt.Println("✗ workflow failed")
	}
}

func printSteps(steps []engine.StepReport, indent string) {
	for _, s := range steps {
		name := s.Name
		if name == "" {
			name = s.ID
		}
		if name == "" {
			name = s.Uses
		}
		if name == "" {
			name = "(step)"
		}
		fmt.Printf("%s%s %s\n", indent, mark(s.Outcome), name)
		if s.Err != "" {
			fmt.Printf("%s    error: %s\n", indent, s.Err)
		}
		for _, k := range sortedKeys(s.Outputs) {
			v := strings.TrimRight(s.Outputs[k], "\n")
			if v == "" {
				continue // omit empty outputs (e.g. empty stderr)
			}
			if strings.Contains(v, "\n") {
				v = strings.ReplaceAll(v, "\n", "\n"+indent+"      ")
			}
			fmt.Printf("%s    %s: %s\n", indent, k, v)
		}
		if len(s.Steps) > 0 {
			printSteps(s.Steps, indent+"  ")
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mark(status string) string {
	switch status {
	case engine.StatusSuccess:
		return "✓"
	case engine.StatusFailure:
		return "✗"
	case engine.StatusSkipped:
		return "∅"
	default:
		return "?"
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: tangra-actions [flags] <workflow.yaml>

flags:
  -input k=v     workflow input (repeatable)
  -secret VALUE  value to mask in output (repeatable)
  -actions DIR   directory of external action packages (enables `+"`uses:`"+`)
  -confine ROOT  restrict file actions to this directory
  -no-js         disable the built-in JavaScript runtime for scripted actions

example:
  tangra-actions -input service=nginx -actions ./examples/actions ./examples/healthcheck.yaml
`)
}

func fatal(prefix string, err error) {
	fmt.Fprintln(os.Stderr, "error:", prefix, err)
	os.Exit(1)
}
