package engine

import (
	"regexp"
	"strings"
)

// statusFuncCall matches a call to one of the status functions as a real
// identifier (preceded by a non-identifier char, followed by optional space and
// "("). It is applied to expression text with string literals blanked out, so a
// status word inside a quoted string does not count.
var statusFuncCall = regexp.MustCompile(`(^|[^A-Za-z0-9_])(success|failure|cancelled|always)[[:space:]]*\(`)

// referencesStatusFunc reports whether a condition calls success(), failure(),
// cancelled() or always(). GitHub Actions treats a condition that does NOT call
// one of these as implicitly guarded by success(): it is skipped once the job
// has failed/been cancelled. A condition that calls one is taking explicit
// control of that gating.
func referencesStatusFunc(expr string) bool {
	return statusFuncCall.MatchString(blankStringLiterals(expr))
}

// blankStringLiterals replaces the contents (and quotes) of single- and
// double-quoted CEL string literals with spaces, so identifier scanning never
// matches text inside a string. Backslash escapes are honoured.
func blankStringLiterals(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	var quote rune
	inStr, escaped := false, false
	for _, r := range s {
		switch {
		case inStr && escaped:
			escaped = false
			b.WriteRune(' ')
		case inStr && r == '\\':
			escaped = true
			b.WriteRune(' ')
		case inStr && r == quote:
			inStr = false
			b.WriteRune(' ')
		case inStr:
			b.WriteRune(' ')
		case r == '\'' || r == '"':
			inStr, quote = true, r
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
