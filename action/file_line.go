package action

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/go-tangra/go-tangra-actions/secure"
)

// FileLine ensures a single line is present in (or absent from) a text file —
// the analogue of Puppet's file_line / Ansible's lineinfile. Paths are resolved
// through secure.Confine. The action is idempotent: it reports Changed only when
// the file content actually changes.
//
// With `match` (a regex), every line matching it is replaced by `line`; if no
// line matches, `line` is appended (unless already present). Without `match`,
// `line` is appended when missing. This gives the standard idempotent
// "set KEY=value" behaviour: a `match` of `^KEY=` rewrites whatever value is
// there, and a second run is a no-op.
//
// Inputs (with):
//
//	path    (required) target file
//	line    the exact line to ensure (required for state=present)
//	match   (optional) regex; matching lines are replaced by `line`
//	state   present | absent  (default present)
//	create  true | false — create the file if missing (default true). With
//	        create=false a missing file is a no-op, for editing a file that only
//	        exists on some hosts (e.g. an Ubuntu cloud-init grub drop-in).
//	mode    octal mode for a newly created file (default 0644)
//
// Outputs: path, state, changed.
type FileLine struct{}

func (*FileLine) Name() string { return "file_line" }

func (*FileLine) Run(_ context.Context, in Input) (Result, error) {
	a := args(in.With)

	rawPath, err := a.required("path")
	if err != nil {
		return Result{}, err
	}
	path, err := secure.Confine(in.ConfineRoot, rawPath)
	if err != nil {
		return Result{}, fmt.Errorf("file_line: %w", err)
	}

	state := a.withDefault("state", "present")
	if state != "present" && state != "absent" {
		return Result{}, fmt.Errorf("file_line: invalid state %q (want present|absent)", state)
	}
	create, err := a.boolValue("create", true)
	if err != nil {
		return Result{}, fmt.Errorf("file_line: %w", err)
	}
	mode, modeSet, err := parseMode(a.str("mode"))
	if err != nil {
		return Result{}, fmt.Errorf("file_line: %w", err)
	}

	var matchRe *regexp.Regexp
	if m := a.str("match"); m != "" {
		matchRe, err = regexp.Compile(m)
		if err != nil {
			return Result{}, fmt.Errorf("file_line: invalid match regex %q: %w", m, err)
		}
	}

	line := a.str("line")
	if strings.ContainsRune(line, '\n') {
		return Result{}, fmt.Errorf("file_line: line must not contain a newline")
	}
	switch state {
	case "present":
		if line == "" {
			return Result{}, fmt.Errorf("file_line: state=present requires `line`")
		}
	case "absent":
		if line == "" && matchRe == nil {
			return Result{}, fmt.Errorf("file_line: state=absent requires `line` or `match`")
		}
	}

	result := Result{Outputs: map[string]string{"path": path, "state": state}}

	sys := in.System
	fi, err := sys.Stat(path)
	if err != nil {
		return result, fmt.Errorf("file_line: %w", err)
	}
	if fi.Exists && fi.IsDir {
		return result, fmt.Errorf("file_line: %q is a directory", path)
	}

	var existing string
	if fi.Exists {
		data, err := sys.ReadFile(path)
		if err != nil {
			return result, fmt.Errorf("file_line: %w", err)
		}
		existing = string(data)
	} else if !create || state == "absent" {
		// Missing file we won't create (or removing a line from a file that isn't
		// there): nothing to do.
		result.Outputs["changed"] = "false"
		return result, nil
	}

	updated := applyFileLine(existing, line, matchRe, state)
	changed := updated != existing
	if changed {
		wantMode := defaultFileMode
		if modeSet {
			wantMode = mode
		} else if fi.Exists {
			wantMode = fi.Mode
		}
		if err := sys.WriteFile(path, []byte(updated), wantMode); err != nil {
			return result, fmt.Errorf("file_line: %w", err)
		}
	}

	result.Changed = changed
	result.Outputs["changed"] = fmt.Sprintf("%t", changed)
	return result, nil
}

// applyFileLine returns the file content after ensuring `line` is present
// (replacing lines matching matchRe, else appending) or absent. The original
// trailing-newline style is preserved; a newly created file gets one.
func applyFileLine(content, line string, matchRe *regexp.Regexp, state string) string {
	addTrailingNL := content == "" || strings.HasSuffix(content, "\n")
	body := strings.TrimSuffix(content, "\n")

	var lines []string
	if body != "" {
		lines = strings.Split(body, "\n")
	}

	switch state {
	case "absent":
		out := make([]string, 0, len(lines))
		for _, l := range lines {
			drop := false
			if matchRe != nil {
				drop = matchRe.MatchString(l)
			} else {
				drop = l == line
			}
			if !drop {
				out = append(out, l)
			}
		}
		lines = out
	default: // present
		if matchRe != nil {
			out := make([]string, len(lines))
			replaced := false
			for i, l := range lines {
				if matchRe.MatchString(l) {
					out[i] = line
					replaced = true
				} else {
					out[i] = l
				}
			}
			lines = out
			if !replaced && !slices.Contains(out, line) {
				lines = append(lines, line)
			}
		} else if !slices.Contains(lines, line) {
			lines = append(lines, line)
		}
	}

	if len(lines) == 0 {
		return ""
	}
	result := strings.Join(lines, "\n")
	if addTrailingNL {
		result += "\n"
	}
	return result
}
