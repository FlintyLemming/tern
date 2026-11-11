package restic

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"tern/internal/engine"
	"tern/internal/engine/filter"
	"tern/internal/model"
)

const unitBackup = "backup"

type backupEngine struct{ *session }

func (e *backupEngine) Units() []string { return []string{unitBackup} }

// checkSources refuses to back up a source that is missing or empty. An
// empty directory is what is left when the host's share is not mounted;
// backing that up would produce an empty snapshot, and the retention
// policy would then rotate the good ones away.
func checkSources(sources []string) error {
	for _, src := range sources {
		fi, err := os.Stat(src)
		if err != nil || !fi.IsDir() {
			return configErr("source missing: %s", src)
		}
	}
	for _, src := range sources {
		f, err := os.Open(src)
		if err != nil {
			return configErr("source not readable: %s", src)
		}
		names, err := f.Readdirnames(1)
		f.Close()
		if len(names) == 0 {
			if err != nil && err != io.EOF {
				return configErr("source not readable: %s", src)
			}
			return configErr("source is empty: %s (is the share mounted?)", src)
		}
	}
	return nil
}

func (e *backupEngine) Preflight(ctx context.Context, env engine.Env) error {
	if err := checkSources(e.spec().Sources); err != nil {
		return err
	}
	if err := e.check(ctx); err != nil {
		return err
	}
	if err := e.ensureOpen(ctx, env); err != nil {
		return err
	}
	// Proves the repository is there and the password opens it. The
	// output is the repository's configuration: not for the log.
	res, err := e.exec(ctx, env, nil, "cat", "config")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return e.failure(env, "cat config", res)
	}
	return nil
}

func (e *backupEngine) RunUnit(ctx context.Context, env engine.Env, _ int) (model.UnitResult, error) {
	spec := e.spec()
	out := model.UnitResult{Name: unitBackup, Status: model.StatusSuccess}

	if err := e.unlock(ctx, env); err != nil {
		if ctx.Err() != nil {
			return out, err
		}
		fmt.Fprintf(env.Log, "removing stale locks failed, going on: %v\n", err)
	}

	excludes := filepath.Join(env.Dir.Path, "excludes")
	lines := filter.ToRestic(spec.Excludes, spec.Sources)
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(excludes, []byte(content), 0o600); err != nil {
		return out, fmt.Errorf("restic: write the exclude file: %w", err)
	}

	args := []string{"backup", "--json", "--host", spec.Host, "--tag", Tag(e.task.ID), "--exclude-file", excludes}
	if spec.LimitUploadKiB > 0 {
		args = append(args, "--limit-upload", strconv.Itoa(spec.LimitUploadKiB))
	}
	args = append(args, spec.Sources...)

	parser := NewParser(
		func(p model.Progress) {
			p.Unit = unitBackup
			if env.Progress != nil {
				env.Progress(p)
			}
		},
		func(item, msg string) { fmt.Fprintf(env.Log, "not backed up: %s: %s\n", item, msg) },
	)
	res, err := e.exec(ctx, env, parser.Line, args...)
	if err != nil {
		return out, err
	}
	kind, warning := ExitKind(res.ExitCode, res.StderrTail)
	if kind != "" {
		return out, e.failure(env, "backup", res)
	}
	if sum, ok := parser.Summary(); ok {
		out.Bytes, out.Files = sum.DataAdded, sum.FilesNew+sum.FilesChanged
		fmt.Fprintf(env.Log, "snapshot %s: %d new, %d changed, %d unmodified files; %d bytes added to the repository\n",
			sum.SnapshotID, sum.FilesNew, sum.FilesChanged, sum.FilesUnmodified, sum.DataAdded)
	}
	if warning {
		out.Status = model.StatusWarning
		fmt.Fprintln(env.Log, "the backup is complete, but some source files could not be read")
	}

	if keep := keepArgs(spec.Keep); len(keep) > 0 {
		// Only this task's snapshots: the repository may hold others.
		forget := append([]string{"forget", "--host", spec.Host, "--tag", Tag(e.task.ID), "--group-by", "host,tags"}, keep...)
		if err := e.call(ctx, env, forget...); err != nil {
			// The snapshot exists; failing to thin out old ones does not
			// make the backup a failure.
			out.Status = model.StatusWarning
			fmt.Fprintf(env.Log, "applying the retention policy failed: %v\n", err)
		}
	}
	return out, nil
}

func keepArgs(k model.KeepPolicy) []string {
	var args []string
	for _, kv := range []struct {
		flag string
		n    int
	}{{"--keep-last", k.Last}, {"--keep-daily", k.Daily}, {"--keep-weekly", k.Weekly}, {"--keep-monthly", k.Monthly}} {
		if kv.n > 0 {
			args = append(args, kv.flag, strconv.Itoa(kv.n))
		}
	}
	return args
}

// exportLines are the two lines that point a plain restic at the task's
// repository. The password is a placeholder: Describe never unseals.
func (s *session) exportLines() []string {
	return []string{
		"export RESTIC_REPOSITORY=" + singleQuote(RecoveryRepository(s.conn, s.spec().RepoPath)),
		"export RESTIC_PASSWORD='<repository password>'",
	}
}

func (e *backupEngine) Describe() []string {
	spec := e.spec()
	cmd := []string{"restic", "backup", "--host", shQuote(spec.Host), "--tag", Tag(e.task.ID)}
	for _, src := range spec.Sources {
		cmd = append(cmd, shQuote(src))
	}
	return append(e.exportLines(), strings.Join(cmd, " "))
}
