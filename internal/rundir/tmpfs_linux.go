//go:build linux

package rundir

import (
	"fmt"
	"syscall"
)

const tmpfsMagic = 0x01021994

// VerifyTmpfs fails unless dir is on tmpfs, i.e. backed by memory, not disk.
func VerifyTmpfs(dir string) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return fmt.Errorf("rundir: statfs %s: %w", dir, err)
	}
	if int64(st.Type) != tmpfsMagic {
		return fmt.Errorf("rundir: %s is not on tmpfs (filesystem type %#x); credentials handed to child processes must never touch a disk — mount a tmpfs there or point TERN_RUNTIME_DIR at one", dir, int64(st.Type))
	}
	return nil
}
