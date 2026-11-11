package restic

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"tern/internal/model"
	"tern/internal/proc"
)

func testInvocation(t *testing.T) Invocation {
	t.Helper()
	dir := newRunDir(t)
	return Invocation{SSHConfig: filepath.Join(dir.Path, "ssh_config"), Dir: dir, DataDir: "/data", RepoPath: "/srv/repo", Connections: 5}
}

func TestCmdGlobals(t *testing.T) {
	iv := testInvocation(t)
	c := iv.Cmd("snapshots")
	want := []string{"-r", "sftp:tern-target:/srv/repo", "-o", `sftp.command=ssh -F "` + iv.Dir.Path + `/ssh_config" tern-target -s sftp`,
		"-o", "sftp.connections=5", "snapshots"}
	if !reflect.DeepEqual(c.Args, want) {
		t.Errorf("Args = %q\nwant   %q", c.Args, want)
	}
	if c.Path != "restic" {
		t.Errorf("Path = %q, want the default restic", c.Path)
	}
	iv.Bin = "/opt/restic"
	if got := iv.Cmd("check", "--json"); got.Path != "/opt/restic" || !reflect.DeepEqual(got.Args[len(got.Args)-2:], []string{"check", "--json"}) {
		t.Errorf("Cmd = %+v", got)
	}
}

func TestCmdEnv(t *testing.T) {
	iv := testInvocation(t)
	got := append([]string(nil), iv.Cmd("snapshots").Env...)
	sort.Strings(got)
	want := []string{"RESTIC_CACHE_DIR=/data/restic-cache", "RESTIC_PASSWORD_FILE=" + iv.Dir.Path + "/repo-password", "RESTIC_PROGRESS_FPS=1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Env = %q, want %q", got, want)
	}
}

func TestPasswordFile(t *testing.T) {
	iv := testInvocation(t)
	pw := []byte("p@$$'w\"ö密码 x")
	if err := iv.WritePassword(pw); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(iv.Dir.Path, "repo-password")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(pw) {
		t.Errorf("content = %q, want %q", got, pw)
	}
}

func TestNoPasswordInCmd(t *testing.T) {
	iv := testInvocation(t)
	const pw = "sup3r-s3cret-repo-pw"
	if err := iv.WritePassword([]byte(pw)); err != nil {
		t.Fatal(err)
	}
	c := iv.Cmd("backup", "/sources/photos")
	for _, s := range append(append([]string{c.Path}, c.Args...), c.Env...) {
		if strings.Contains(s, pw) {
			t.Errorf("password in %q", s)
		}
	}
}

func TestExitKind(t *testing.T) {
	cases := []struct {
		code int
		tail string
		kind model.ErrKind
		warn bool
	}{
		{0, "", "", false},
		{3, "", "", true},
		{10, "", model.KindConfig, false},
		{11, "", model.KindUnknown, false},
		{12, "", model.KindAuth, false},
		{12, "Connection reset by peer", model.KindAuth, false}, // the code wins
		{1, "Connection reset by peer", model.KindNetwork, false},
		{1, "Host key verification failed.", model.KindHostKey, false},
		{1, "", model.KindUnknown, false},
		{-1, "", model.KindUnknown, false},
	}
	for _, c := range cases {
		if kind, warn := ExitKind(c.code, c.tail); kind != c.kind || warn != c.warn {
			t.Errorf("ExitKind(%d, %q) = %q, %v; want %q, %v", c.code, c.tail, kind, warn, c.kind, c.warn)
		}
	}
}

// The fake restic itself: what it records and plays back.
func TestFakeRestic(t *testing.T) {
	bin, calls := fakeRestic(t, fakeStep{Stdout: "one\n", Exit: 3}, fakeStep{Stderr: "boom\n", Exit: 12})
	iv := testInvocation(t)
	iv.Bin = bin
	if err := iv.WritePassword([]byte("pw")); err != nil {
		t.Fatal(err)
	}
	var out []string
	c := iv.Cmd("backup", "/a b")
	c.OnStdout = func(l []byte) { out = append(out, string(l)) }
	res, err := proc.Run(context.Background(), c)
	if err != nil || res.ExitCode != 3 || !reflect.DeepEqual(out, []string{"one"}) {
		t.Fatalf("first call: %+v, %v, stdout %q", res, err, out)
	}
	res, _ = proc.Run(context.Background(), iv.Cmd("check"))
	if res.ExitCode != 12 || res.StderrTail != "boom\n" {
		t.Fatalf("second call: %+v", res)
	}
	res, _ = proc.Run(context.Background(), iv.Cmd("unlock"))
	if res.ExitCode != 0 {
		t.Fatalf("unscripted call: %+v", res)
	}
	got := calls()
	if !reflect.DeepEqual(subs(got), []string{"backup", "check", "unlock"}) {
		t.Fatalf("subs = %q", subs(got))
	}
	first := got[0]
	if first.Args[len(first.Args)-1] != "/a b" || first.Env["RESTIC_PROGRESS_FPS"] != "1" || first.Password != "pw" || first.PasswordMode != 0o600 {
		t.Errorf("first call recorded as %+v", first)
	}
}
