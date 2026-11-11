package rclone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"tern/internal/engine"
	"tern/internal/engine/filter"
	"tern/internal/engine/synccore"
	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/redact"
)

const filterFile = "rclone-filter"

// Deps is what the rclone planner needs from the rest of the process.
type Deps struct {
	Access engine.AccessOpener
	Bin    string // default "rclone"
}

type planner struct {
	d    Deps
	spec model.SyncSpec
	conn model.Connection

	mu       sync.Mutex
	prepared bool
	conf     string
	filter   string // "" without exclude rules
	release  func()
}

// NewPlanner returns the planner for an SFTP or WebDAV sync task. conn is
// only used to describe the equivalent command line.
func NewPlanner(t model.Task, conn model.Connection, d Deps) synccore.Planner {
	if d.Bin == "" {
		d.Bin = "rclone"
	}
	return &planner{d: d, spec: *t.Sync, conn: conn}
}

func (p *planner) Prepare(ctx context.Context, env engine.Env) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prepared {
		return nil
	}
	acc, release, err := p.d.Access.Open(ctx, p.spec.ConnectionID, env.Dir, env.Redact)
	if err != nil {
		return err
	}
	conf, err := WriteConfig(ctx, env.Dir, acc, p.d.Bin)
	if err != nil {
		release()
		return err
	}
	filterPath := ""
	if len(p.spec.Excludes) > 0 {
		lines := filter.ToRclone(p.spec.Excludes)
		if filterPath, err = env.Dir.WriteSecret(filterFile, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
			release()
			return fmt.Errorf("rclone: write the filter file: %w", err)
		}
	}
	p.conf, p.filter, p.release, p.prepared = conf, filterPath, release, true
	return nil
}

func (p *planner) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release != nil {
		p.release()
		p.release = nil
	}
	return nil
}

// exec runs one rclone call. stderr lines go through onStderr, which
// returns what to write to the run log.
func (p *planner) exec(ctx context.Context, env engine.Env, args []string, onStdout func([]byte), onStderr func([]byte) string) (proc.Result, error) {
	fmt.Fprintf(env.Log, "$ rclone %s\n", strings.Join(args, " "))
	res, err := proc.Run(ctx, proc.Cmd{
		Path:     p.d.Bin,
		Args:     args,
		OnStdout: onStdout,
		OnStderr: func(line []byte) {
			if text := onStderr(line); text != "" {
				fmt.Fprintln(env.Log, text)
			}
		},
	})
	if err != nil {
		return res, &model.KindError{Kind: model.KindConfig, Msg: "cannot start rclone", Err: err}
	}
	if res.Signaled {
		return res, &model.KindError{Kind: model.KindUnknown, Msg: "rclone " + args[0] + " was stopped", Err: context.Cause(ctx)}
	}
	return res, nil
}

// reason is the last error rclone reported, as one redacted line: it ends
// up in a run record, which is not behind the log's redactor.
func reason(red *redact.Redactor, tail string) string {
	lines := strings.Split(strings.TrimSpace(tail), "\n")
	line := ""
	for i := len(lines) - 1; i >= 0 && line == ""; i-- {
		var e entry
		if json.Unmarshal([]byte(lines[i]), &e) != nil {
			line = strings.TrimSpace(lines[i])
			continue
		}
		if e.Level == "error" || e.Level == "critical" {
			line = e.Msg
			if e.Object != "" {
				line = e.Object + ": " + line
			}
		}
	}
	if line == "" {
		line = strings.TrimSpace(lines[len(lines)-1])
	}
	if len(line) > 300 {
		line = line[:300]
	}
	if red == nil {
		return line
	}
	var buf bytes.Buffer
	w := red.Writer(&buf)
	fmt.Fprintln(w, line)
	w.Close()
	return strings.TrimSpace(buf.String())
}

func failure(env engine.Env, sub string, res proc.Result) error {
	msg := fmt.Sprintf("rclone %s failed (exit %d)", sub, res.ExitCode)
	if r := reason(env.Redact, res.StderrTail); r != "" {
		msg += ": " + r
	}
	return &model.KindError{Kind: ExitKind(res.ExitCode, res.StderrTail), Msg: msg}
}

func (p *planner) RemoteState(ctx context.Context, env engine.Env, fp model.FolderPair) (synccore.SourceState, error) {
	args := []string{"lsf", remote(fp.Remote), "--config", p.conf, "--max-depth", "1", "--timeout", "60s", "--contimeout", "15s"}
	listed := false
	res, err := p.exec(ctx, env, args, func([]byte) { listed = true }, func(b []byte) string { return string(b) })
	if err != nil {
		return 0, err
	}
	switch {
	case res.ExitCode == exitDirNotFound:
		return synccore.SourceMissing, nil
	case res.ExitCode != 0:
		return 0, failure(env, "lsf", res)
	case !listed:
		return synccore.SourceEmpty, nil
	}
	return synccore.SourceOK, nil
}

func (p *planner) Plan(ctx context.Context, env engine.Env, fp model.FolderPair) (synccore.Plan, error) {
	args := Args(p.spec, fp, p.conf, p.filter, true, -1)
	parser := NewParser(nil)
	res, err := p.exec(ctx, env, args, nil, parser.Line)
	if err != nil {
		return synccore.Plan{}, err
	}
	if res.ExitCode != 0 {
		return synccore.Plan{}, failure(env, args[0]+" --dry-run", res)
	}
	return parser.Plan(), nil
}

func (p *planner) Transfer(ctx context.Context, env engine.Env, fp model.FolderPair, maxDelete int) (model.UnitResult, error) {
	args := Args(p.spec, fp, p.conf, p.filter, false, maxDelete)
	parser := NewParser(env.Progress)
	res, err := p.exec(ctx, env, args, nil, parser.Line)
	out := model.UnitResult{}
	out.Bytes, out.Files = parser.Totals()
	if err != nil {
		return out, err
	}
	if res.ExitCode != 0 {
		return out, failure(env, args[0], res)
	}
	out.Status = model.StatusSuccess
	return out, nil
}

func (p *planner) Describe(fp model.FolderPair) string { return "" }
