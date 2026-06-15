package expr

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/cel-go/common/types/ref"
)

const (
	openDelim  = "${{"
	closeDelim = "}}"
	// maxInterpolations bounds how many expressions a single string may contain,
	// a cheap guard against a pathological template.
	maxInterpolations = 256
	// maxOutputBytes bounds the rendered result so a small template that
	// amplifies a large context value (e.g. a big input repeated many times)
	// cannot blow up memory.
	maxOutputBytes = 1 << 20 // 1 MiB
)

// Interpolate replaces every `${{ expr }}` span in s with the string form of
// the evaluated expression. Text outside the delimiters is copied verbatim.
// The expressions see the same context as Eval.
//
// Returns an error if a delimiter is unbalanced, an expression fails to
// compile/evaluate, or the count exceeds maxInterpolations.
func (e *Engine) Interpolate(s string, ctx Context) (string, error) {
	if !strings.Contains(s, openDelim) {
		return s, nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	var b strings.Builder
	rest := s
	count := 0
	for {
		i := strings.Index(rest, openDelim)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		after := rest[i+len(openDelim):]
		inner, tail, found := strings.Cut(after, closeDelim)
		if !found {
			return "", fmt.Errorf("unterminated %s in expression", openDelim)
		}
		exprText := strings.TrimSpace(inner)
		if exprText == "" {
			return "", fmt.Errorf("empty %s %s expression", openDelim, closeDelim)
		}

		count++
		if count > maxInterpolations {
			return "", fmt.Errorf("too many interpolations (> %d)", maxInterpolations)
		}

		out, err := e.evalLocked(exprText, ctx)
		if err != nil {
			return "", err
		}
		str, err := stringify(out)
		if err != nil {
			return "", fmt.Errorf("interpolate %q: %w", exprText, err)
		}
		b.WriteString(str)
		if b.Len() > maxOutputBytes {
			return "", fmt.Errorf("interpolation output exceeds %d bytes", maxOutputBytes)
		}

		rest = tail
	}
	return b.String(), nil
}

// stringify renders a CEL result as the string that gets substituted. Strings
// pass through; bools and integers use their canonical text; doubles drop a
// trailing ".0" so 1.0 reads as "1" like GitHub Actions. Composite types are
// rejected — interpolating a map/list into a command is almost always a mistake.
func stringify(v ref.Val) (string, error) {
	switch val := v.Value().(type) {
	case string:
		return val, nil
	case bool:
		return strconv.FormatBool(val), nil
	case int64:
		return strconv.FormatInt(val, 10), nil
	case uint64:
		return strconv.FormatUint(val, 10), nil
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64), nil
	case []byte:
		return string(val), nil
	default:
		return "", fmt.Errorf("expression result of type %T is not interpolatable", val)
	}
}
