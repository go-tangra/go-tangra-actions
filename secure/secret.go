// Package secure holds the cross-cutting security controls used by the engine
// and the builtin actions: secret masking, filesystem path confinement, and
// validation of values (package/service names, env keys) that would otherwise
// reach a process or the OS unchecked.
//
// Each control is deliberately small, dependency-free, and exhaustively tested
// against adversarial input — these are the functions an attacker would probe,
// so they are the ones worth proving.
package secure

import (
	"sort"
	"strings"
)

// redaction is what every secret value is replaced with in masked output.
const redaction = "***"

// Masker replaces registered secret values with a fixed redaction marker
// wherever they appear in text. It is used on everything surfaced back to the
// caller — step stdout/stderr and log lines — so a secret passed into an action
// cannot leak out through its output.
//
// A Masker is immutable after construction; build one with NewMasker. The zero
// value masks nothing, which is safe (it simply never redacts).
type Masker struct {
	// secrets are sorted longest-first so that overlapping secrets redact the
	// largest match (avoids leaving a suffix of a longer secret behind).
	secrets []string
}

// NewMasker returns a Masker for the given secret values. Empty and
// whitespace-only values are ignored — masking "" would replace between every
// character, and a blank secret carries no information to protect.
func NewMasker(secrets ...string) *Masker {
	cleaned := make([]string, 0, len(secrets))
	seen := map[string]bool{}
	for _, s := range secrets {
		if strings.TrimSpace(s) == "" || seen[s] {
			continue
		}
		seen[s] = true
		cleaned = append(cleaned, s)
	}
	sort.Slice(cleaned, func(i, j int) bool {
		return len(cleaned[i]) > len(cleaned[j])
	})
	return &Masker{secrets: cleaned}
}

// Mask returns s with every occurrence of every registered secret replaced by
// the redaction marker.
func (m *Masker) Mask(s string) string {
	if m == nil || len(m.secrets) == 0 || s == "" {
		return s
	}
	for _, secret := range m.secrets {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, redaction)
		}
	}
	return s
}

// MaskBytes is Mask over a byte slice.
func (m *Masker) MaskBytes(b []byte) []byte {
	if m == nil || len(m.secrets) == 0 || len(b) == 0 {
		return b
	}
	return []byte(m.Mask(string(b)))
}

// HasSecrets reports whether any secret is registered.
func (m *Masker) HasSecrets() bool { return m != nil && len(m.secrets) > 0 }
