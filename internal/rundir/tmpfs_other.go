//go:build !linux

package rundir

// VerifyTmpfs cannot check anything off Linux; tern is deployed on Linux only
// and other systems are development machines.
func VerifyTmpfs(dir string) error { return nil }
