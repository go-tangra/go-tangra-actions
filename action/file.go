package action

import (
	"bytes"
	"context"
	"fmt"

	"github.com/go-tangra/go-tangra-actions/secure"
)

// File manages a file or directory on disk. Paths are resolved through
// secure.Confine against Input.ConfineRoot, so when a root is configured no
// step can read or write outside it (".." traversal and absolute escapes are
// rejected). The action is idempotent: it reports Changed only when it actually
// altered content, mode, or existence.
//
// Inputs (with):
//
//	path     (required) target path
//	state    present | absent | directory   (default present)
//	content  file contents for state=present (default empty)
//	mode     octal permission bits, e.g. "0644" (optional)
//
// Outputs: path, state, changed.
type File struct{}

func (*File) Name() string { return "file" }

const (
	defaultFileMode uint32 = 0o644
	defaultDirMode  uint32 = 0o755
)

func (*File) Run(_ context.Context, in Input) (Result, error) {
	a := args(in.With)

	rawPath, err := a.required("path")
	if err != nil {
		return Result{}, err
	}
	path, err := secure.Confine(in.ConfineRoot, rawPath)
	if err != nil {
		return Result{}, fmt.Errorf("file: %w", err)
	}

	mode, modeSet, err := parseMode(a.str("mode"))
	if err != nil {
		return Result{}, fmt.Errorf("file: %w", err)
	}

	state := a.withDefault("state", "present")
	result := Result{Outputs: map[string]string{"path": path, "state": state}}

	var changed bool
	switch state {
	case "present":
		changed, err = ensureFile(in, path, []byte(a.str("content")), mode, modeSet)
	case "directory":
		changed, err = ensureDir(in, path, mode, modeSet)
	case "absent":
		changed, err = ensureAbsent(in, path)
	default:
		return result, fmt.Errorf("file: invalid state %q (want present|absent|directory)", state)
	}
	if err != nil {
		return result, fmt.Errorf("file: %w", err)
	}

	result.Changed = changed
	result.Outputs["changed"] = fmt.Sprintf("%t", changed)
	return result, nil
}

func ensureFile(in Input, path string, content []byte, mode uint32, modeSet bool) (bool, error) {
	sys := in.System
	fi, err := sys.Stat(path)
	if err != nil {
		return false, err
	}
	if fi.Exists && fi.IsDir {
		return false, fmt.Errorf("%q exists and is a directory", path)
	}

	changed := false
	contentDiffers := true
	if fi.Exists {
		existing, err := sys.ReadFile(path)
		if err != nil {
			return false, err
		}
		contentDiffers = !bytes.Equal(existing, content)
	}

	wantMode := mode
	if !modeSet {
		wantMode = defaultFileMode
	}

	if !fi.Exists || contentDiffers {
		if err := sys.WriteFile(path, content, wantMode); err != nil {
			return false, err
		}
		changed = true
	} else if modeSet && fi.Mode != mode {
		if err := sys.Chmod(path, mode); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func ensureDir(in Input, path string, mode uint32, modeSet bool) (bool, error) {
	sys := in.System
	fi, err := sys.Stat(path)
	if err != nil {
		return false, err
	}
	if fi.Exists && !fi.IsDir {
		return false, fmt.Errorf("%q exists and is not a directory", path)
	}

	wantMode := mode
	if !modeSet {
		wantMode = defaultDirMode
	}

	if !fi.Exists {
		if err := sys.Mkdir(path, wantMode); err != nil {
			return false, err
		}
		return true, nil
	}
	if modeSet && fi.Mode != mode {
		if err := sys.Chmod(path, mode); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func ensureAbsent(in Input, path string) (bool, error) {
	fi, err := in.System.Stat(path)
	if err != nil {
		return false, err
	}
	if !fi.Exists {
		return false, nil
	}
	if err := in.System.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}
