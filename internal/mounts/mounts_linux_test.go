//go:build linux

package mounts

import (
	"os"
	"testing"
)

func TestIsMounted(t *testing.T) {
	// /dev/shm is a tmpfs mount; neither x nor y exists, so the answer
	// comes from the nearest ancestor that does.
	if ok, err := IsMounted("/dev/shm/x/y"); err != nil || !ok {
		t.Errorf("IsMounted(/dev/shm/x/y) = %v, %v; want true", ok, err)
	}
	// A directory on the root file system.
	dir, err := os.MkdirTemp("/var", "tern-mounts")
	if err != nil {
		t.Skipf("cannot create a directory on the root file system: %v", err)
	}
	defer os.RemoveAll(dir)
	if ok, err := IsMounted(dir); err != nil || ok {
		t.Errorf("IsMounted(%s) = %v, %v; want false", dir, ok, err)
	}
	if ok, err := IsMounted("/"); err != nil || ok {
		t.Errorf("IsMounted(/) = %v, %v; want false", ok, err)
	}
}
