package secure

import (
	"fmt"
	"regexp"
	"strings"
)

// These allowlists are intentionally strict. Structured actions never run a
// shell, so the residual risk is *argument* injection: a value beginning with
// "-" being treated as a flag by apt/systemctl, or a control character /
// newline smuggling a second token. Each validator rejects those classes up
// front, before the value can reach a process.

var (
	// packageNameRe covers Debian/RPM/Alpine/Arch names plus the version-pin and
	// architecture qualifiers callers commonly use (e.g. nginx=1.2, nginx:amd64,
	// libfoo-dev). It must start with an alphanumeric so it can never look like
	// an option.
	packageNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+:=~-]*$`)

	// serviceNameRe covers systemd unit names, including instances (foo@bar) and
	// an optional unit-type suffix.
	serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:-]*$`)

	// envNameRe is the POSIX-ish environment variable name shape.
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	// hostnameRe is an RFC 1123 host (or FQDN) name: dot-separated labels of
	// [A-Za-z0-9-], each 1–63 chars, none starting or ending with a hyphen. The
	// leading alphanumeric means a value can never be parsed as a flag.
	hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

	// timezoneRe is an IANA tz name: slash-separated components of
	// [A-Za-z0-9_+-], each starting alphanumeric (e.g. UTC, Europe/Sofia,
	// Etc/GMT+5, America/Argentina/Buenos_Aires). It contains no "." so it can
	// never express a ".." path-traversal segment when used to build a
	// /usr/share/zoneinfo path.
	timezoneRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_+-]*(/[A-Za-z0-9][A-Za-z0-9_+-]*)*$`)
)

const maxTokenLen = 256

// maxHostnameLen is the RFC 1035 limit on a full domain name.
const maxHostnameLen = 253

// ValidatePackageName checks a single package identifier.
func ValidatePackageName(s string) error {
	return validateToken("package name", s, packageNameRe)
}

// ValidateServiceName checks a single service/unit name.
func ValidateServiceName(s string) error {
	return validateToken("service name", s, serviceNameRe)
}

// ValidateEnvName checks an environment variable name.
func ValidateEnvName(s string) error {
	if !envNameRe.MatchString(s) {
		return fmt.Errorf("invalid environment variable name %q", s)
	}
	return nil
}

// ValidateHostname checks an RFC 1123 host (or FQDN) name before it is passed to
// hostnamectl/hostname as an argument.
func ValidateHostname(s string) error {
	switch {
	case s == "":
		return fmt.Errorf("empty hostname")
	case len(s) > maxHostnameLen:
		return fmt.Errorf("hostname too long (%d > %d)", len(s), maxHostnameLen)
	case hasControlChars(s):
		return fmt.Errorf("hostname %q contains a control character", s)
	case !hostnameRe.MatchString(s):
		return fmt.Errorf("invalid hostname %q", s)
	}
	return nil
}

// ValidateTimezone checks an IANA timezone name before it is passed to
// timedatectl or used to build a /usr/share/zoneinfo path. The strict shape
// (no ".") rules out path traversal.
func ValidateTimezone(s string) error {
	switch {
	case s == "":
		return fmt.Errorf("empty timezone")
	case len(s) > maxTokenLen:
		return fmt.Errorf("timezone too long (%d > %d)", len(s), maxTokenLen)
	case hasControlChars(s):
		return fmt.Errorf("timezone %q contains a control character", s)
	case !timezoneRe.MatchString(s):
		return fmt.Errorf("invalid timezone %q", s)
	}
	return nil
}

func validateToken(kind, s string, re *regexp.Regexp) error {
	switch {
	case s == "":
		return fmt.Errorf("empty %s", kind)
	case len(s) > maxTokenLen:
		return fmt.Errorf("%s too long (%d > %d)", kind, len(s), maxTokenLen)
	case strings.HasPrefix(s, "-"):
		// Belt-and-suspenders: the regex already forbids this, but spell out the
		// reason because it is the highest-value rejection.
		return fmt.Errorf("%s %q must not begin with '-' (would be parsed as a flag)", kind, s)
	case hasControlChars(s):
		return fmt.Errorf("%s %q contains a control character", kind, s)
	case !re.MatchString(s):
		return fmt.Errorf("invalid %s %q", kind, s)
	}
	return nil
}

// hasControlChars reports whether s contains any ASCII control character
// (including NUL, newline, tab, ESC), which has no place in a name and is a
// common injection vector.
func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
