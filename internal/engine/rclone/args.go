package rclone

import (
	"fmt"
	"strconv"
	"strings"

	"tern/internal/engine"
	"tern/internal/model"
)

// fixed are the options of every rclone sync call, in this order.
// rclone keeps its operation-level retries (--low-level-retries, spec §8.3)
// but does not start the whole sync over itself (--retries 1): that is the
// task-level retry's job, with its delays, its retry_wait state and only
// the failed folder pairs. Three operation retries, each a new ssh with a
// 15 s connect timeout, let rclone give up within about two minutes of an
// outage (the SSH keepalive notices it after about one).
func fixed(conf string) []string {
	return []string{"--config", conf, "--use-json-log", "--log-level", "INFO", "--stats", "2s",
		"--timeout", "60s", "--contimeout", "15s", "--retries", "1", "--low-level-retries", "3",
		"--skip-links", "--create-empty-src-dirs"}
}

// remote names a path on the task's connection.
func remote(path string) string { return "remote:" + path }

// Args is the rclone call that syncs one folder pair. keep copies, mirror
// syncs; maxDelete >= 0 caps the deletions of a mirror. filterFile is ""
// when the task has no exclude rules.
func Args(spec model.SyncSpec, p model.FolderPair, conf, filterFile string, dryRun bool, maxDelete int) []string {
	sub := "copy"
	if spec.DeleteMode == "mirror" {
		sub = "sync"
	}
	src, dst := p.Local, remote(p.Remote)
	if spec.Direction == "pull" {
		src, dst = dst, src
	}
	args := append([]string{sub, src, dst}, fixed(conf)...)
	args = append(args, "--transfers", strconv.Itoa(spec.Transfers))
	if spec.LimitKiB > 0 {
		args = append(args, "--bwlimit", fmt.Sprintf("%dKi", spec.LimitKiB))
	}
	if spec.Compare == "checksum" {
		args = append(args, "--checksum")
	}
	if filterFile != "" {
		args = append(args, "--filter-from", filterFile)
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if sub == "sync" && maxDelete >= 0 {
		args = append(args, "--max-delete", strconv.Itoa(maxDelete))
	}
	return args
}

// rclone's exit codes.
const (
	exitOK          = 0
	exitUsage       = 2 // syntax or usage error
	exitDirNotFound = 3
	exitNotFound    = 4 // file not found
	exitTemporary   = 5 // temporary error: more retries might fix it
	exitNoRetry     = 6 // less serious errors, such as files that failed
)

// maxDeleteSign is what rclone says when its own --max-delete stops a
// sync: the second gate of the mirror guard (it exits 7, fatal).
const maxDeleteSign = "--max-delete threshold reached"

// ExitKind sorts a finished rclone call; "" means it did not fail.
func ExitKind(code int, stderrTail string) model.ErrKind {
	switch code {
	case exitOK:
		return ""
	case exitUsage, exitDirNotFound, exitNotFound:
		return model.KindConfig
	case exitTemporary:
		return model.KindNetwork
	case exitNoRetry:
		return model.KindData
	}
	if strings.Contains(stderrTail, maxDeleteSign) {
		return model.KindGuard
	}
	if kind, ok := engine.ClassifyStderr(stderrTail); ok {
		return kind
	}
	return model.KindUnknown
}
