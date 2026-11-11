package rclone

import (
	"context"
	"os"
	"strings"
	"testing"

	"tern/internal/access"
	"tern/internal/engine/faketool"
	"tern/internal/model"
	"tern/internal/rundir"
)

func newRunDir(t *testing.T) *rundir.Dir {
	t.Helper()
	dir, err := rundir.New(t.TempDir(), "run1")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s has mode %o, want 600", path, fi.Mode().Perm())
	}
	return string(raw)
}

func TestConfigSFTP(t *testing.T) {
	dir := newRunDir(t)
	a := access.Access{Kind: model.ConnSSH, SSHConfig: dir.Path + "/ssh_config"}
	path, err := WriteConfig(context.Background(), dir, a, "rclone-not-called")
	if err != nil {
		t.Fatal(err)
	}
	if path != dir.Path+"/rclone.conf" {
		t.Errorf("path = %q", path)
	}
	want := "[remote]\ntype = sftp\nssh = ssh -F \"" + dir.Path + "/ssh_config\" tern-target\n"
	if got := readConfig(t, path); got != want {
		t.Errorf("rclone.conf =\n%s\nwant\n%s", got, want)
	}
}

func TestConfigWebDAV(t *testing.T) {
	const pass = `s3cr3t "pass" ö`
	fake := faketool.New(t, "rclone", faketool.Step{Stdout: "OBSCURED\n"})
	dir := newRunDir(t)
	a := access.Access{Kind: model.ConnWebDAV, URL: "https://dav.example/remote.php/dav", User: "anna@example.com", Pass: pass}
	path, err := WriteConfig(context.Background(), dir, a, fake.Bin)
	if err != nil {
		t.Fatal(err)
	}
	got := readConfig(t, path)
	want := "[remote]\ntype = webdav\nurl = https://dav.example/remote.php/dav\nvendor = other\nuser = anna@example.com\npass = OBSCURED\n"
	if got != want {
		t.Errorf("rclone.conf =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, pass) {
		t.Error("the password is in rclone.conf")
	}
	calls := fake.Calls()
	if len(calls) != 1 || strings.Join(calls[0].Args, " ") != "obscure -" {
		t.Fatalf("rclone calls = %+v", calls)
	}
	if calls[0].Stdin != pass {
		t.Errorf("stdin = %q, want the password", calls[0].Stdin)
	}
	for _, arg := range calls[0].Args {
		if strings.Contains(arg, pass) {
			t.Errorf("the password is in argv: %q", calls[0].Args)
		}
	}
}

// A WebDAV share without a password gets no pass line; rclone is not run.
func TestConfigWebDAVNoPassword(t *testing.T) {
	fake := faketool.New(t, "rclone")
	dir := newRunDir(t)
	a := access.Access{Kind: model.ConnWebDAV, URL: "http://dav.example/", User: ""}
	path, err := WriteConfig(context.Background(), dir, a, fake.Bin)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := readConfig(t, path), "[remote]\ntype = webdav\nurl = http://dav.example/\nvendor = other\nuser = \n"; got != want {
		t.Errorf("rclone.conf =\n%s\nwant\n%s", got, want)
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("rclone called %d times", n)
	}
}

// A value that would end its line early or be trimmed by the INI parser
// never reaches the file.
func TestConfigRefusesUnsafeValues(t *testing.T) {
	fake := faketool.New(t, "rclone", faketool.Step{Stdout: "OBSCURED\n"})
	for _, a := range []access.Access{
		{Kind: model.ConnWebDAV, URL: "http://dav.example/", User: "anna\ntype = local"},
		{Kind: model.ConnWebDAV, URL: "http://dav.example/", User: " anna"},
		{Kind: model.ConnWebDAV, URL: "http://dav.example/\n[x]", User: "anna"},
	} {
		if _, err := WriteConfig(context.Background(), newRunDir(t), a, fake.Bin); err == nil {
			t.Errorf("WriteConfig(%q, %q) succeeded", a.URL, a.User)
		}
	}
}
