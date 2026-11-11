package mounts

import (
	"path/filepath"
	"testing"
)

func TestNearestExisting(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ path, want string }{
		{dir, dir},
		{filepath.Join(dir, "a", "b", "c"), dir},
		{"/", "/"},
	} {
		if got, err := NearestExisting(c.path); err != nil || got != c.want {
			t.Errorf("NearestExisting(%q) = %q, %v; want %q", c.path, got, err, c.want)
		}
	}
	if _, err := NearestExisting("relative/path"); err == nil {
		t.Error("a relative path was accepted")
	}
}
