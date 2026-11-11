package engine

import (
	"strings"

	"tern/internal/model"
)

// stderrSigns maps what ssh, rsync, rclone and restic print when they fail
// to the kind of failure. Order matters: a refused host key or key pair is
// usually followed by "connection closed", which must not win.
var stderrSigns = []struct {
	kind  model.ErrKind
	signs []string
}{
	{model.KindHostKey, []string{"host key verification failed", "remote host identification has changed"}},
	{model.KindAuth, []string{"permission denied (publickey", "too many authentication failures", "401 unauthorized", "403 forbidden"}},
	{model.KindConfig, []string{"rsync: command not found", "rsync: not found"}},
	{model.KindNetwork, []string{"connection timed out", "connection refused", "connection reset", "connection closed", "broken pipe",
		"no route to host", "network is unreachable", "could not resolve hostname", "temporary failure in name resolution",
		"not responding", "i/o timeout", "unexpected eof", "connection lost"}},
}

// ClassifyStderr reads the kind of a failure from the end of a tool's
// stderr. ok is false when nothing in it is recognized.
func ClassifyStderr(tail string) (kind model.ErrKind, ok bool) {
	tail = strings.ToLower(tail)
	for _, group := range stderrSigns {
		for _, sign := range group.signs {
			if strings.Contains(tail, sign) {
				return group.kind, true
			}
		}
	}
	return "", false
}
