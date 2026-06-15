package action

import (
	"context"
	"testing"

	"github.com/go-tangra/go-tangra-actions/system"
)

func runFileLine(t *testing.T, f *system.Fake, with map[string]string) Result {
	t.Helper()
	res, err := (&FileLine{}).Run(context.Background(), Input{With: with, System: f})
	if err != nil {
		t.Fatalf("FileLine.Run(%v): %v", with, err)
	}
	return res
}

func TestFileLine_ReplaceMatchThenIdempotent(t *testing.T) {
	f := system.NewFake()
	_ = f.WriteFile("/etc/default/grub", []byte("GRUB_TIMEOUT_STYLE=hidden\nGRUB_TIMEOUT=0\n"), 0o644)

	res := runFileLine(t, f, map[string]string{
		"path":  "/etc/default/grub",
		"match": "^GRUB_TIMEOUT_STYLE=",
		"line":  "GRUB_TIMEOUT_STYLE=menu",
	})
	if !res.Changed {
		t.Fatal("replacing the matched line should report changed")
	}
	got, _ := f.ReadFile("/etc/default/grub")
	want := "GRUB_TIMEOUT_STYLE=menu\nGRUB_TIMEOUT=0\n"
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}

	// Second run: nothing matches the old value and the line already exists -> no-op.
	res = runFileLine(t, f, map[string]string{
		"path":  "/etc/default/grub",
		"match": "^GRUB_TIMEOUT_STYLE=",
		"line":  "GRUB_TIMEOUT_STYLE=menu",
	})
	if res.Changed {
		t.Error("second run should be idempotent (no change)")
	}
	if res.Outputs["changed"] != "false" {
		t.Errorf("changed output = %q, want false", res.Outputs["changed"])
	}
}

func TestFileLine_AppendsWhenNoMatch(t *testing.T) {
	f := system.NewFake()
	_ = f.WriteFile("/f", []byte("alpha\nbeta\n"), 0o644)
	res := runFileLine(t, f, map[string]string{
		"path":  "/f",
		"match": "^gamma=",
		"line":  "gamma=1",
	})
	if !res.Changed {
		t.Fatal("expected append to change the file")
	}
	got, _ := f.ReadFile("/f")
	if string(got) != "alpha\nbeta\ngamma=1\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestFileLine_AppendWithoutMatch(t *testing.T) {
	f := system.NewFake()
	_ = f.WriteFile("/f", []byte("a\n"), 0o644)
	// No match: append if missing.
	if res := runFileLine(t, f, map[string]string{"path": "/f", "line": "b"}); !res.Changed {
		t.Error("appending a missing line should change")
	}
	// Already present: no-op.
	if res := runFileLine(t, f, map[string]string{"path": "/f", "line": "b"}); res.Changed {
		t.Error("appending an existing line should be a no-op")
	}
	got, _ := f.ReadFile("/f")
	if string(got) != "a\nb\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestFileLine_CreatesFileWhenMissing(t *testing.T) {
	f := system.NewFake()
	res := runFileLine(t, f, map[string]string{"path": "/new", "line": "hello", "mode": "0600"})
	if !res.Changed {
		t.Fatal("creating a file should change")
	}
	got, _ := f.ReadFile("/new")
	if string(got) != "hello\n" {
		t.Errorf("content = %q, want \"hello\\n\"", got)
	}
	if fi, _ := f.Stat("/new"); fi.Mode != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode)
	}
}

func TestFileLine_CreateFalseSkipsMissingFile(t *testing.T) {
	f := system.NewFake() // file does not exist
	res := runFileLine(t, f, map[string]string{
		"path":   "/etc/default/grub.d/50-cloudimg-settings.cfg",
		"create": "false",
		"match":  "^GRUB_TIMEOUT=",
		"line":   "GRUB_TIMEOUT=5",
	})
	if res.Changed {
		t.Error("create=false on a missing file must be a no-op")
	}
	if fi, _ := f.Stat("/etc/default/grub.d/50-cloudimg-settings.cfg"); fi.Exists {
		t.Error("create=false must not create the file")
	}
}

func TestFileLine_PreservesOtherLinesAndMode(t *testing.T) {
	f := system.NewFake()
	_ = f.WriteFile("/f", []byte("# header\nKEY=old\n# footer\n"), 0o640)
	runFileLine(t, f, map[string]string{"path": "/f", "match": "^KEY=", "line": "KEY=new"})
	got, _ := f.ReadFile("/f")
	if string(got) != "# header\nKEY=new\n# footer\n" {
		t.Fatalf("content = %q", got)
	}
	if fi, _ := f.Stat("/f"); fi.Mode != 0o640 {
		t.Errorf("mode = %o, want 640 (preserved)", fi.Mode)
	}
}

func TestFileLine_Absent(t *testing.T) {
	f := system.NewFake()
	_ = f.WriteFile("/f", []byte("keep\nremove-me\nkeep2\n"), 0o644)
	res := runFileLine(t, f, map[string]string{"path": "/f", "state": "absent", "line": "remove-me"})
	if !res.Changed {
		t.Fatal("removing an existing line should change")
	}
	got, _ := f.ReadFile("/f")
	if string(got) != "keep\nkeep2\n" {
		t.Fatalf("content = %q", got)
	}
	// Idempotent: line already gone.
	if res := runFileLine(t, f, map[string]string{"path": "/f", "state": "absent", "line": "remove-me"}); res.Changed {
		t.Error("removing an absent line should be a no-op")
	}
}

func TestFileLine_Validation(t *testing.T) {
	f := system.NewFake()
	cases := []map[string]string{
		{"path": "/f"},                                // present without line
		{"path": "/f", "state": "absent"},             // absent without line or match
		{"path": "/f", "line": "x", "match": "("},     // bad regex
		{"path": "/f", "line": "a\nb"},                // newline in line
		{"path": "/f", "line": "x", "state": "weird"}, // bad state
	}
	for _, with := range cases {
		if _, err := (&FileLine{}).Run(context.Background(), Input{With: with, System: f}); err == nil {
			t.Errorf("FileLine(%v) should error", with)
		}
	}
}

func TestFileLine_ConfineBlocksTraversal(t *testing.T) {
	f := system.NewFake()
	_, err := (&FileLine{}).Run(context.Background(), Input{
		With:        map[string]string{"path": "../../etc/shadow", "line": "x"},
		System:      f,
		ConfineRoot: "/srv/work",
	})
	if err == nil {
		t.Fatal("a path escaping the confine root must be rejected")
	}
}
