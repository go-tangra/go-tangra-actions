package engine

import (
	"maps"
	"sort"
)

// mergeEnv layers environment maps left-to-right: later maps override earlier
// keys. It returns a fresh map and never mutates its inputs.
func mergeEnv(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, layer := range layers {
		maps.Copy(out, layer)
	}
	return out
}

// envSlice renders an env map as sorted KEY=VALUE entries for system.ExecRequest.
// Sorting makes the output deterministic (and tests stable).
func envSlice(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
