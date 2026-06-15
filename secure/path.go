package secure

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrPathEscape is returned when a target path resolves outside the confinement
// root.
var ErrPathEscape = errors.New("path escapes confinement root")

// ErrEmptyPath is returned for an empty target path.
var ErrEmptyPath = errors.New("empty path")

// ErrNullByte is returned for a path containing a NUL byte (a classic
// truncation/injection vector for syscalls).
var ErrNullByte = errors.New("path contains NUL byte")

// Confine resolves target against an optional confinement root and guarantees
// the result does not escape it.
//
//   - root == "" disables confinement: the cleaned absolute/relative target is
//     returned as-is. This is the deliberate "trusted agent" mode where actions
//     may legitimately touch system paths such as /etc.
//   - root != "" requires the result to be inside root. A relative target is
//     joined onto root; an absolute target must already live under root. Any
//     attempt to climb out with ".." is rejected.
//
// Confinement is lexical (it does not resolve symlinks, since the target may not
// exist yet); callers needing symlink-safe behaviour must run with appropriate
// OS privileges and/or pre-resolve the root.
func Confine(root, target string) (string, error) {
	if strings.ContainsRune(target, 0) {
		return "", ErrNullByte
	}
	if strings.TrimSpace(target) == "" {
		return "", ErrEmptyPath
	}

	if root == "" {
		return filepath.Clean(target), nil
	}

	root = filepath.Clean(root)
	var joined string
	if filepath.IsAbs(target) {
		joined = filepath.Clean(target)
	} else {
		joined = filepath.Clean(filepath.Join(root, target))
	}

	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPathEscape, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q is outside %q", ErrPathEscape, target, root)
	}
	return joined, nil
}
