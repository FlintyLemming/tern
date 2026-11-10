//go:build linux

package rundir

import (
	"os"
	"strings"
	"testing"
)

func TestVerifyTmpfs(t *testing.T) {
	if err := VerifyTmpfs("/dev/shm"); err != nil {
		t.Errorf("/dev/shm: %v", err)
	}
	// t.TempDir() honours TMPDIR, which may itself be tmpfs; /var/tmp is disk.
	disk, err := os.MkdirTemp("/var/tmp", "tern")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(disk)
	if err := VerifyTmpfs(disk); err == nil || !strings.Contains(err.Error(), "tmpfs") {
		t.Errorf("disk directory: err = %v", err)
	}
}
