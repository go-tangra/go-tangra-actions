package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/go-tangra/go-tangra-actions/secure"
	"github.com/go-tangra/go-tangra-actions/workflow"
)

// ResolvedAction is what a Resolver returns: the parsed manifest plus the
// action package's files. Files lets a scripted action's `main` (and any
// sibling files it requires) be read; it may be nil for a composite action,
// which needs no files.
type ResolvedAction struct {
	Def   *workflow.ActionDef
	Files fs.FS
}

// Resolver turns a step's `uses` reference into a resolved action. It is the
// trust boundary for external actions: the engine itself performs no network or
// filesystem discovery, so a consumer decides exactly which actions are runnable
// (a local directory, an in-memory catalog, a signed bundle store, …) by
// supplying a Resolver. A reference that does not resolve must return an error.
type Resolver interface {
	Resolve(ctx context.Context, ref string) (*ResolvedAction, error)
}

// ErrActionNotFound is returned by resolvers when a reference is unknown.
type ErrActionNotFound struct{ Ref string }

func (e *ErrActionNotFound) Error() string { return fmt.Sprintf("action %q not found", e.Ref) }

// MapResolver resolves references from an in-memory catalog. Useful for tests
// and for a consumer that has already loaded/verified action definitions.
type MapResolver map[string]*ResolvedAction

// Resolve returns the action registered under ref.
func (m MapResolver) Resolve(_ context.Context, ref string) (*ResolvedAction, error) {
	if a, ok := m[ref]; ok {
		return a, nil
	}
	return nil, &ErrActionNotFound{Ref: ref}
}

// DirResolver loads action packages from a directory tree rooted at Root. A
// reference "foo/bar" resolves to the package directory "<Root>/foo/bar" (with
// an "action.yaml"/"action.yml" manifest) or the flat manifest
// "<Root>/foo/bar.yaml". The reference is confined to Root — "../" escapes are
// rejected — so a workflow cannot load an action from outside the configured
// root. Files is rooted at the package directory, so a scripted action's `main`
// resolves relative to it.
type DirResolver struct {
	Root string
}

// Resolve loads and parses the manifest for ref and returns its package files.
func (d DirResolver) Resolve(_ context.Context, ref string) (*ResolvedAction, error) {
	base, err := secure.Confine(d.Root, ref)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", ref, err)
	}

	// Package-directory form: <base>/action.y[a]ml — Files rooted at <base>.
	for _, name := range []string{"action.yaml", "action.yml"} {
		path := filepath.Join(base, name)
		def, err := readActionManifest(path, ref)
		if err != nil {
			return nil, err
		}
		if def != nil {
			return &ResolvedAction{Def: def, Files: os.DirFS(base)}, nil
		}
	}

	// Flat form: <base>.y[a]ml — Files rooted at the manifest's directory.
	for _, path := range []string{base + ".yaml", base + ".yml"} {
		def, err := readActionManifest(path, ref)
		if err != nil {
			return nil, err
		}
		if def != nil {
			return &ResolvedAction{Def: def, Files: os.DirFS(filepath.Dir(path))}, nil
		}
	}

	return nil, &ErrActionNotFound{Ref: ref}
}

// readActionManifest returns the parsed manifest at path, (nil, nil) if the file
// does not exist, or an error for a read/parse failure.
func readActionManifest(path, ref string) (*workflow.ActionDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve %q: %w", ref, err)
	}
	def, err := workflow.ParseAction(data)
	if err != nil {
		return nil, fmt.Errorf("resolve %q (%s): %w", ref, path, err)
	}
	return def, nil
}
