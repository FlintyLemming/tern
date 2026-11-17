//go:build !linux

package mounts

import "io/fs"

// Device never knows off Linux, where tern runs only for development.
func Device(fs.FileInfo) (dev uint64, ok bool) { return 0, false }
