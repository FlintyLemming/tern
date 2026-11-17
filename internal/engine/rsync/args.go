// Package rsync turns the folder pairs of an rsync-over-SSH sync task into
// rsync child processes. It implements synccore.Planner; the mirror guards
// and the dry run live in synccore.
package rsync

import (
	"fmt"
	"strings"

	"tern/internal/engine"
	"tern/internal/model"
	"tern/internal/sshx"
)

// dir ends a path with exactly one "/": rsync then syncs the directory's
// contents rather than the directory itself.
func dir(path string) string { return strings.TrimRight(path, "/") + "/" }

// remote names a directory on the task's connection.
func remote(path string) string { return sshx.Alias + ":" + dir(path) }

// shell is rsync's -e value: ssh with the run's generated config.
func shell(sshConfig string) string { return fmt.Sprintf(`ssh -F "%s"`, sshConfig) }

// rsyncPath starts the remote rsync through sh, which first reports its
// PID on stderr (pidPrefix) and then becomes rsync. When a link dies the
// remote end keeps running until TCP gives up, holding the file it was
// receiving; knowing the PID lets the next attempt stop it first.
const rsyncPath = `sh -c 'echo tern-pid:$$ >&2; exec rsync "$@"' rsync`

const pidPrefix = "tern-pid:"

// Args is the rsync call that syncs one folder pair. A mirror always gets
// --delete (a dry run has to list the deletions); maxDelete >= 0 adds the
// limit. excludeFile is "" when the task has no exclude rules. Itemized
// output (--out-format) is only requested for dry runs so large transfers do
// not flood the run log with one line per file.
func Args(spec model.SyncSpec, p model.FolderPair, sshConfig, excludeFile string, dryRun bool, maxDelete int) []string {
	// -s keeps the remote shell from splitting or expanding the remote path.
	args := []string{"-aHs", "--no-inc-recursive", "--partial-dir=.tern-partial", "--timeout=60", "--info=progress2", "--stats",
		"-e", shell(sshConfig), "--rsync-path=" + rsyncPath}
	if spec.DeleteMode == "mirror" {
		args = append(args, "--delete")
		if maxDelete >= 0 {
			args = append(args, fmt.Sprintf("--max-delete=%d", maxDelete))
		}
	}
	if spec.LimitKiB > 0 {
		args = append(args, fmt.Sprintf("--bwlimit=%d", spec.LimitKiB))
	}
	if spec.Compare == "checksum" {
		args = append(args, "--checksum")
	}
	if excludeFile != "" {
		args = append(args, "--exclude-from="+excludeFile)
	}
	if dryRun {
		args = append(args, "--dry-run", "--out-format=%i %n%L")
	}
	src, dst := dir(p.Local), remote(p.Remote)
	if spec.Direction == "pull" {
		src, dst = dst, src
	}
	return append(args, src, dst)
}

// ExitKind sorts a finished rsync call. kind is "" when it did not fail;
// warning is true when files vanished from the source while it ran.
func ExitKind(code int, stderrTail string) (kind model.ErrKind, warning bool) {
	if code == 0 {
		return "", false
	}
	if kind, ok := engine.ClassifyStderr(stderrTail); ok {
		switch kind {
		case model.KindHostKey, model.KindAuth, model.KindConfig:
			return kind, false
		}
	}
	switch code {
	case 24: // source files vanished
		return "", true
	case 23: // some files could not be transferred
		return model.KindData, false
	case 25: // --max-delete stopped deletions
		return model.KindGuard, false
	case 1, 2, 3, 4, 5, 6: // syntax, protocol, paths, unsupported, startup
		return model.KindConfig, false
	case 10, 12, 30, 35, 255: // socket, data stream, timeouts, ssh
		return model.KindNetwork, false
	}
	return model.KindUnknown, false
}
