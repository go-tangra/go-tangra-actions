package system

import (
	"context"
	"errors"
	"io/fs"
	"runtime"
	"testing"
	"time"
)

func TestReal_Exec_DirectNoShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix only")
	}
	r := NewReal()
	// No shell: metacharacters are literal arguments, never interpreted.
	res, err := r.Exec(context.Background(), ExecRequest{
		Name: "printf", Args: []string{"%s", "a;b|c$(whoami)"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if res.Stdout != "a;b|c$(whoami)" {
		t.Errorf("stdout = %q, want the literal arg (no shell expansion)", res.Stdout)
	}
}

func TestReal_Exec_NonZeroExitIsNotError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix only")
	}
	r := NewReal()
	res, err := r.Exec(context.Background(), ExecRequest{Shell: "sh", Name: "exit 7"})
	if err != nil {
		t.Fatalf("Exec returned error for non-zero exit: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit = %d, want 7", res.ExitCode)
	}
}

func TestReal_Exec_Stdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix only")
	}
	r := NewReal()
	res, err := r.Exec(context.Background(), ExecRequest{Name: "cat", Input: "hello stdin"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.Stdout != "hello stdin" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

func TestReal_Exec_EnvAndCombine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix only")
	}
	r := NewReal()
	res, err := r.Exec(context.Background(), ExecRequest{
		Shell: "sh", Name: "echo $MY_VAR; echo errline 1>&2",
		Env: []string{"MY_VAR=xyz"}, Combine: true,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if want := "xyz\nerrline\n"; res.Stdout != want {
		t.Errorf("combined stdout = %q, want %q", res.Stdout, want)
	}
}

func TestReal_Exec_ContextCancelKills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix only")
	}
	r := NewReal()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := r.Exec(ctx, ExecRequest{Shell: "sh", Name: "sleep 5"})
	if err == nil {
		t.Fatal("expected error from killed process")
	}
}

func TestReal_Exec_BinaryNotFound(t *testing.T) {
	r := NewReal()
	_, err := r.Exec(context.Background(), ExecRequest{Name: "this-binary-does-not-exist-xyz"})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestReal_FilesystemRoundTrip(t *testing.T) {
	r := NewReal()
	dir := t.TempDir()
	p := dir + "/sub/file.txt"

	if err := r.Mkdir(dir+"/sub", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := r.WriteFile(p, []byte("data"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fi, err := r.Stat(p)
	if err != nil || !fi.Exists {
		t.Fatalf("Stat: %v exists=%v", err, fi.Exists)
	}
	if fi.Mode != 0o640 {
		t.Errorf("mode = %o, want 640", fi.Mode)
	}
	got, err := r.ReadFile(p)
	if err != nil || string(got) != "data" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if err := r.Chmod(p, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if fi, _ := r.Stat(p); fi.Mode != 0o600 {
		t.Errorf("mode after chmod = %o", fi.Mode)
	}
	if err := r.Remove(dir + "/sub"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if fi, _ := r.Stat(p); fi.Exists {
		t.Error("file should be gone after Remove")
	}
}

func TestReal_StatMissingNotError(t *testing.T) {
	r := NewReal()
	fi, err := r.Stat("/no/such/path/really")
	if err != nil {
		t.Fatalf("Stat missing: %v", err)
	}
	if fi.Exists {
		t.Error("Exists should be false")
	}
}

func TestReal_RemoveMissingNotError(t *testing.T) {
	r := NewReal()
	if err := r.Remove("/no/such/path/at/all"); err != nil {
		t.Errorf("Remove missing: %v", err)
	}
}

func TestReal_LookPathHost(t *testing.T) {
	r := NewReal()
	if _, ok := r.LookPath("definitely-not-a-real-binary-zzz"); ok {
		t.Error("LookPath should fail for missing binary")
	}
	h := r.Host()
	if h.OS != runtime.GOOS || h.Arch != runtime.GOARCH {
		t.Errorf("Host = %+v", h)
	}
}

func TestFake_ExecRecordsAndResponds(t *testing.T) {
	f := NewFake()
	f.ExecFunc = func(_ context.Context, req ExecRequest) (ExecResult, error) {
		if req.Name == "apt-get" {
			return ExecResult{Stdout: "ok", ExitCode: 0}, nil
		}
		return ExecResult{ExitCode: 1}, nil
	}
	res, err := f.Exec(context.Background(), ExecRequest{Name: "apt-get", Args: []string{"install"}})
	if err != nil || res.Stdout != "ok" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Name != "apt-get" || calls[0].Args[0] != "install" {
		t.Errorf("calls = %+v", calls)
	}
}

func TestFake_ExecContextCancelled(t *testing.T) {
	f := NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Exec(ctx, ExecRequest{Name: "x"}); err == nil {
		t.Fatal("expected context error")
	}
}

func TestFake_Filesystem(t *testing.T) {
	f := NewFake()
	if err := f.Mkdir("/etc/app", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := f.WriteFile("/etc/app/conf", []byte("x=1"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fi, _ := f.Stat("/etc/app/conf")
	if !fi.Exists || fi.IsDir || fi.Mode != 0o644 || fi.Size != 3 {
		t.Errorf("stat = %+v", fi)
	}
	if fi, _ := f.Stat("/etc/app"); !fi.IsDir {
		t.Error("/etc/app should be a dir")
	}
	data, err := f.ReadFile("/etc/app/conf")
	if err != nil || string(data) != "x=1" {
		t.Fatalf("ReadFile = %q %v", data, err)
	}
	// Writing over a directory fails.
	if err := f.WriteFile("/etc/app", []byte("nope"), 0o644); err == nil {
		t.Error("writing over a directory should fail")
	}
	// Remove directory tree: /etc/app and its children go, parent /etc remains.
	if err := f.Remove("/etc/app"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if fi, _ := f.Stat("/etc/app/conf"); fi.Exists {
		t.Error("/etc/app/conf should be gone")
	}
	if fi, _ := f.Stat("/etc/app"); fi.Exists {
		t.Error("/etc/app should be gone")
	}
	if fi, _ := f.Stat("/etc"); !fi.Exists {
		t.Error("/etc (parent) should remain")
	}
}

func TestFake_ReadMissing(t *testing.T) {
	f := NewFake()
	_, err := f.ReadFile("/nope")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want ErrNotExist", err)
	}
}

func TestFake_LookPath(t *testing.T) {
	f := NewFake().AddPath("dnf")
	if _, ok := f.LookPath("dnf"); !ok {
		t.Error("dnf should be found")
	}
	if _, ok := f.LookPath("apt"); ok {
		t.Error("apt should not be found")
	}
}

func TestFake_Helpers(t *testing.T) {
	f := NewFake().SetHost(HostInfo{OS: "darwin", Arch: "arm64"})
	if h := f.Host(); h.OS != "darwin" || h.Arch != "arm64" {
		t.Errorf("Host = %+v", h)
	}

	_ = f.WriteFile("/a", []byte("x"), 0o644)
	_ = f.WriteFile("/b", []byte("y"), 0o600)
	if names := f.Filenames(); len(names) != 2 || names[0] != "/a" || names[1] != "/b" {
		t.Errorf("Filenames = %v, want [/a /b]", names)
	}

	if err := f.Chmod("/a", 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if fi, _ := f.Stat("/a"); fi.Mode != 0o600 {
		t.Errorf("mode after chmod = %o", fi.Mode)
	}
	if err := f.Chmod("/missing", 0o600); err == nil {
		t.Error("Chmod on missing path should error")
	}

	// Mkdir over an existing regular file is an error.
	if err := f.Mkdir("/a", 0o755); err == nil {
		t.Error("Mkdir over a file should error")
	}
}
