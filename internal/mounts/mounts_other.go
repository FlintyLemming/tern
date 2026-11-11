//go:build !linux

package mounts

// IsMounted is always true off Linux: tern only runs in production as a
// Linux container, and development machines have no volumes to tell apart.
func IsMounted(path string) (bool, error) { return true, nil }
