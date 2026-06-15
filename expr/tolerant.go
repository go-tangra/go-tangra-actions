package expr

import (
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

// tolerantMap wraps a CEL string map so that selecting a missing key yields an
// empty string rather than a "no such key" runtime error. This matches GitHub
// Actions, where an undefined context value (e.g. env.UNSET) is the empty
// string — so a condition like `env.DEPLOY == 'true'` is simply false when
// DEPLOY is unset, instead of failing the step.
//
// Only key *selection* (Find/Get) is made lenient. Contains is left to the
// embedded map, so the `in` operator still reports real key presence
// (`'DEPLOY' in env` is honestly false when unset).
type tolerantMap struct {
	traits.Mapper
}

// newTolerantMap builds a lenient view over a plain string map.
func newTolerantMap(m map[string]string) tolerantMap {
	return tolerantMap{types.NewStringStringMap(types.DefaultTypeAdapter, m)}
}

// Find returns the value for key, or an empty string (reported as found) when
// the key is absent.
func (t tolerantMap) Find(key ref.Val) (ref.Val, bool) {
	if v, ok := t.Mapper.Find(key); ok {
		return v, true
	}
	return types.String(""), true
}

// Get mirrors Find for index expressions (env['KEY']).
func (t tolerantMap) Get(index ref.Val) ref.Val {
	v, _ := t.Find(index)
	return v
}
