//go:build linux

package mounts

import (
	"io/fs"
	"syscall"
)

// Device is the number of the device the file fi describes lies on; ok is
// false when the platform does not say. A directory on another device than
// its parent is a mount point.
func Device(fi fs.FileInfo) (dev uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}
