//go:build linux

package mounts

import "syscall"

// IsMounted reports whether path is on a mounted volume: its nearest
// existing ancestor is on another device than "/". A path that does not
// exist yet is judged by where it would be created.
func IsMounted(path string) (bool, error) {
	dir, err := NearestExisting(path)
	if err != nil {
		return false, err
	}
	var at, root syscall.Stat_t
	if err := syscall.Stat(dir, &at); err != nil {
		return false, err
	}
	if err := syscall.Stat("/", &root); err != nil {
		return false, err
	}
	return at.Dev != root.Dev, nil
}
