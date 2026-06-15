package secure

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestMasker_Basic(t *testing.T) {
	m := NewMasker("s3cr3t", "hunter2")
	in := "token=s3cr3t pass=hunter2 ok"
	got := m.Mask(in)
	if strings.Contains(got, "s3cr3t") || strings.Contains(got, "hunter2") {
		t.Fatalf("secret leaked: %q", got)
	}
	if got != "token=*** pass=*** ok" {
		t.Errorf("got %q", got)
	}
}

func TestMasker_OverlappingLongestFirst(t *testing.T) {
	// A shorter secret is a substring of a longer one. Masking longest-first
	// must not leave a dangling fragment of the longer secret.
	m := NewMasker("abc", "abcdef")
	got := m.Mask("value=abcdef end")
	if strings.Contains(got, "def") {
		t.Fatalf("fragment of longer secret leaked: %q", got)
	}
	if got != "value=*** end" {
		t.Errorf("got %q", got)
	}
}

func TestMasker_IgnoresBlankSecrets(t *testing.T) {
	m := NewMasker("", "   ", "real")
	// Blank secrets must not turn into a mask-between-every-char bug.
	got := m.Mask("a b c real")
	if got != "a b c ***" {
		t.Errorf("got %q", got)
	}
}

func TestMasker_NilAndEmpty(t *testing.T) {
	var m *Masker
	if got := m.Mask("anything"); got != "anything" {
		t.Errorf("nil masker changed text: %q", got)
	}
	if m.HasSecrets() {
		t.Error("nil masker should report no secrets")
	}
	empty := NewMasker()
	if empty.HasSecrets() {
		t.Error("empty masker should report no secrets")
	}
	if got := string(NewMasker("x").MaskBytes([]byte("axb"))); got != "a***b" {
		t.Errorf("MaskBytes = %q", got)
	}
}

func TestMasker_Multiline(t *testing.T) {
	m := NewMasker("TOPSECRET")
	in := "line1 TOPSECRET\nline2 TOPSECRET\n"
	got := m.Mask(in)
	if strings.Contains(got, "TOPSECRET") {
		t.Errorf("leaked across lines: %q", got)
	}
}

func TestConfine_Unconfined(t *testing.T) {
	// Empty root: trusted mode, returns cleaned path, allows system paths.
	got, err := Confine("", "/etc/nginx/../nginx/nginx.conf")
	if err != nil {
		t.Fatalf("Confine: %v", err)
	}
	if got != "/etc/nginx/nginx.conf" {
		t.Errorf("got %q", got)
	}
}

func TestConfine_RelativeJoinedToRoot(t *testing.T) {
	got, err := Confine("/srv/work", "sub/file.txt")
	if err != nil {
		t.Fatalf("Confine: %v", err)
	}
	if got != filepath.Clean("/srv/work/sub/file.txt") {
		t.Errorf("got %q", got)
	}
}

func TestConfine_AbsoluteInsideRoot(t *testing.T) {
	got, err := Confine("/srv/work", "/srv/work/a/b")
	if err != nil {
		t.Fatalf("Confine: %v", err)
	}
	if got != "/srv/work/a/b" {
		t.Errorf("got %q", got)
	}
}

func TestConfine_RejectsEscapes(t *testing.T) {
	escapes := []string{
		"../etc/passwd",
		"a/../../etc/passwd",
		"../../../../../../etc/shadow",
		"/etc/passwd",       // absolute, outside root
		"sub/../../outside", // climbs out after descending
		"./../../x",
	}
	for _, e := range escapes {
		t.Run(e, func(t *testing.T) {
			_, err := Confine("/srv/work", e)
			if !errors.Is(err, ErrPathEscape) {
				t.Errorf("Confine(%q) err = %v, want ErrPathEscape", e, err)
			}
		})
	}
}

func TestConfine_RootItself(t *testing.T) {
	got, err := Confine("/srv/work", ".")
	if err != nil {
		t.Fatalf("Confine('.'): %v", err)
	}
	if got != "/srv/work" {
		t.Errorf("got %q", got)
	}
}

func TestConfine_RejectsEmptyAndNull(t *testing.T) {
	if _, err := Confine("/root", ""); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("empty: err = %v", err)
	}
	if _, err := Confine("/root", "  "); !errors.Is(err, ErrEmptyPath) {
		t.Errorf("blank: err = %v", err)
	}
	if _, err := Confine("/root", "a\x00b"); !errors.Is(err, ErrNullByte) {
		t.Errorf("null: err = %v", err)
	}
	// NUL is rejected even in unconfined mode.
	if _, err := Confine("", "a\x00b"); !errors.Is(err, ErrNullByte) {
		t.Errorf("null unconfined: err = %v", err)
	}
}

func TestValidatePackageName(t *testing.T) {
	good := []string{"nginx", "libxmlb2", "nginx=1.2.3", "nginx:amd64", "g++", "python3.11", "lib-foo_bar", "ca-certificates"}
	for _, s := range good {
		if err := ValidatePackageName(s); err != nil {
			t.Errorf("ValidatePackageName(%q) = %v, want ok", s, err)
		}
	}
	bad := []string{
		"",
		"-rf",             // looks like a flag
		"--force-yes",     // flag injection
		"nginx; rm -rf /", // shell metachars
		"nginx && reboot",
		"foo\nbar", // newline
		"foo bar",  // space => second token
		"foo`whoami`",
		"foo$(id)",
		"foo|bar",
		"foo\x00bar", // NUL
	}
	for _, s := range bad {
		if err := ValidatePackageName(s); err == nil {
			t.Errorf("ValidatePackageName(%q) = nil, want error", s)
		}
	}
}

func TestValidateServiceName(t *testing.T) {
	good := []string{"nginx", "nginx.service", "getty@tty1", "user@1000.service", "foo-bar.socket"}
	for _, s := range good {
		if err := ValidateServiceName(s); err != nil {
			t.Errorf("ValidateServiceName(%q) = %v, want ok", s, err)
		}
	}
	bad := []string{"", "-x", "a b", "a;b", "a/b", "foo\nbar", "$(x)"}
	for _, s := range bad {
		if err := ValidateServiceName(s); err == nil {
			t.Errorf("ValidateServiceName(%q) = nil, want error", s)
		}
	}
}

func TestValidateEnvName(t *testing.T) {
	for _, s := range []string{"PATH", "MY_VAR", "_x", "A1"} {
		if err := ValidateEnvName(s); err != nil {
			t.Errorf("ValidateEnvName(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"", "1ABC", "a-b", "a b", "a.b"} {
		if err := ValidateEnvName(s); err == nil {
			t.Errorf("ValidateEnvName(%q) = nil, want error", s)
		}
	}
}

func TestValidateHostname(t *testing.T) {
	good := []string{"web-01", "host", "a", "web-01.example.com", "a1.b2.c3", "node1"}
	for _, s := range good {
		if err := ValidateHostname(s); err != nil {
			t.Errorf("ValidateHostname(%q) = %v, want ok", s, err)
		}
	}
	bad := []string{
		"", "-x", "x-", "a b", "a;b", "a/b", "foo\nbar", "$(x)",
		"host_underscore", "-flag", ".leadingdot", "trailingdot.",
		"double..dot", strings.Repeat("a", 254),
	}
	for _, s := range bad {
		if err := ValidateHostname(s); err == nil {
			t.Errorf("ValidateHostname(%q) = nil, want error", s)
		}
	}
}

func TestValidateTimezone(t *testing.T) {
	good := []string{"UTC", "Etc/UTC", "Europe/Sofia", "America/New_York", "Etc/GMT+5", "America/Argentina/Buenos_Aires"}
	for _, s := range good {
		if err := ValidateTimezone(s); err != nil {
			t.Errorf("ValidateTimezone(%q) = %v, want ok", s, err)
		}
	}
	bad := []string{
		"", "..", "../../etc/shadow", "/etc/passwd", "-flag", "a;b",
		"Europe/Sofia\n", "a b", "Europe//Sofia", "/leading", "trailing/",
	}
	for _, s := range bad {
		if err := ValidateTimezone(s); err == nil {
			t.Errorf("ValidateTimezone(%q) = nil, want error", s)
		}
	}
}

func TestValidate_TooLong(t *testing.T) {
	long := strings.Repeat("a", maxTokenLen+1)
	if err := ValidatePackageName(long); err == nil {
		t.Error("over-long name should be rejected")
	}
}
