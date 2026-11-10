package rundir

import (
	"os"
	"path/filepath"
	"testing"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestNewDirMode(t *testing.T) {
	base := t.TempDir()
	d, err := New(base, "run1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Path != filepath.Join(base, "run1") || mode(t, d.Path) != 0o700 {
		t.Errorf("Path = %q, mode %o", d.Path, mode(t, d.Path))
	}
	if _, err := New(base, "run1"); err == nil {
		t.Error("a second New for the same run reused the directory")
	}
	for _, id := range []string{"", ".", "..", "a/b", "../x"} {
		if _, err := New(base, id); err == nil {
			t.Errorf("New accepted run id %q", id)
		}
	}
}

func TestWriteSecretMode(t *testing.T) {
	d, err := New(t.TempDir(), "run1")
	if err != nil {
		t.Fatal(err)
	}
	path, err := d.WriteSecret("known_hosts", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(d.Path, "known_hosts") || mode(t, path) != 0o600 {
		t.Errorf("path = %q, mode %o", path, mode(t, path))
	}
	if got, _ := os.ReadFile(path); string(got) != "content" {
		t.Errorf("content = %q", got)
	}
	if _, err := d.WriteSecret("known_hosts", []byte("again")); err == nil {
		t.Error("WriteSecret overwrote an existing file")
	}
}

func TestWriteSecretRejectsPath(t *testing.T) {
	base := t.TempDir()
	d, err := New(base, "run1")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../x", "a/b", "", ".", "..", "/abs"} {
		if _, err := d.WriteSecret(name, []byte("x")); err == nil {
			t.Errorf("WriteSecret accepted %q", name)
		}
	}
	if entries, _ := os.ReadDir(base); len(entries) != 1 {
		t.Errorf("something was written outside the run directory: %v", entries)
	}
}

func TestCloseRemovesEverything(t *testing.T) {
	d, err := New(t.TempDir(), "run1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteSecret("s", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.Path); !os.IsNotExist(err) {
		t.Errorf("directory still exists: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestSweepRemovesLeftovers(t *testing.T) {
	base := t.TempDir()
	for _, sub := range []string{"old1", "old2"} {
		if err := os.MkdirAll(filepath.Join(base, sub, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, sub, "agent.sock"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Sweep(base); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("base is gone: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}
