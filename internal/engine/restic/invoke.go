// Package restic turns backup tasks into restic child processes: backup,
// retention, prune and check, snapshot listing and restore.
package restic

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tern/internal/proc"
	"tern/internal/rundir"
	"tern/internal/sshx"
)

const passwordFile = "repo-password"

// Invocation is what every restic call of one run has in common. SSH goes
// through the run's generated ssh_config; the repository password reaches
// restic as a file in the tmpfs run directory, never on the command line.
type Invocation struct {
	Bin         string // default "restic"
	SSHConfig   string
	Dir         *rundir.Dir
	DataDir     string
	RepoPath    string
	Connections int
}

// Cmd builds a restic call: the fixed global options first, then args.
func (iv Invocation) Cmd(args ...string) proc.Cmd {
	bin := iv.Bin
	if bin == "" {
		bin = "restic"
	}
	global := []string{
		"-r", "sftp:" + sshx.Alias + ":" + iv.RepoPath,
		"-o", csvField(fmt.Sprintf(`sftp.command=ssh -F "%s" %s -s sftp`, iv.SSHConfig, sshx.Alias)),
		"-o", fmt.Sprintf("sftp.connections=%d", iv.Connections),
	}
	return proc.Cmd{
		Path: bin,
		Args: append(global, args...),
		Env: []string{
			"RESTIC_PASSWORD_FILE=" + filepath.Join(iv.Dir.Path, passwordFile),
			// On the data volume: without the cache every run downloads the
			// index again, which is slow on a weak link.
			"RESTIC_CACHE_DIR=" + filepath.Join(iv.DataDir, "restic-cache"),
			"TMPDIR=" + TempDir(iv.DataDir),
			"RESTIC_PROGRESS_FPS=1",
		},
	}
}

// TempDir is where restic writes the packs it is about to upload: 16 MiB
// each, one per connection plus a few being filled, so about 150 MiB at the
// default five connections. Not /tmp: on QTS that is a 64 MiB RAM disk.
// restic unlinks each file as soon as it creates it, so a crash leaves
// nothing behind; the directory itself must exist (PrepareTempDir).
func TempDir(dataDir string) string {
	return filepath.Join(dataDir, "restic-tmp")
}

// PrepareTempDir creates TempDir under dataDir.
func PrepareTempDir(dataDir string) error {
	if err := os.MkdirAll(TempDir(dataDir), 0o700); err != nil {
		return fmt.Errorf("create restic's temporary directory: %w", err)
	}
	return nil
}

// csvField quotes v for restic's -o flag, which is parsed as a CSV record:
// unquoted, a quote in the value is a syntax error and a comma splits it.
func csvField(v string) string {
	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
}

// WritePassword puts the repository password where Cmd points restic to,
// byte for byte. It is written once per run directory.
func (iv Invocation) WritePassword(pw []byte) error {
	_, err := iv.Dir.WriteSecret(passwordFile, pw)
	return err
}
