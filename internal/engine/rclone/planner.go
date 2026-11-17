package rclone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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

// mkdirFirst reports whether a transfer creates the remote folder in a
// call of its own first. rclone's WebDAV backend makes a missing folder
// from every goroutine that needs it: with --create-empty-src-dirs the
// directory pass MKCOLs each new subfolder, and every transfer MKCOLs its
// file's parent. Subfolders are safe, since the directory pass makes one
// before it lists what goes into it, but the pair's own remote folder is
// the parent of the first transfers while the directory pass makes it too.
// Servers that lock a path for each request (golang.org/x/net/webdav, so
// rclone serve webdav and the servers built on it; ownCloud) answer all
// but one of those concurrent MKCOLs 423 Locked. rclone means to take a
// 423 on MKCOL as "being created" but misses it once its own retries are
// spent (the error is wrapped by then), and the directory pass, unlike a
// transfer, is not retried: the pair fails after every file arrived.
// The SFTP backend locks each directory it makes within the process.
func (p *planner) mkdirFirst() bool {
	return p.spec.Protocol == "webdav" && p.spec.Direction == "push"
}

func (p *planner) Transfer(ctx context.Context, env engine.Env, fp model.FolderPair, maxDelete int) (model.UnitResult, error) {
	if p.mkdirFirst() {
		args := MkdirArgs(fp, p.conf)
		res, err := p.exec(ctx, env, args, nil, func(b []byte) string { return string(b) })
		if err != nil {
			return model.UnitResult{}, err
		}
		if res.ExitCode != 0 {
			return model.UnitResult{}, failure(env, args[0], res)
		}
	}
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

// connValue quotes a value of an rclone connection string when it has
// characters that would end it.
func connValue(v string) string {
	if strings.ContainsAny(v, `,:"'= `) {
		return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
	}
	return v
}

// Describe is the rclone command line that syncs the pair without tern:
// the remote as a connection string instead of tern's generated config,
// and a placeholder where the WebDAV password goes.
func (p *planner) Describe(fp model.FolderPair) string {
	sub := "copy"
	if p.spec.DeleteMode == "mirror" {
		sub = "sync"
	}
	var target string
	var opts []string
	switch p.spec.Protocol {
	case "webdav":
		target = fmt.Sprintf(":webdav,url=%s,user=%s:%s", connValue(p.conn.URL), connValue(p.conn.Username), fp.Remote)
		opts = append(opts, "--webdav-pass", `"$(rclone obscure '<WebDAV 密码>')"`)
	default:
		target = fmt.Sprintf(":sftp,host=%s,user=%s,port=%d:%s", connValue(p.conn.Host), connValue(p.conn.Username), p.conn.Port, fp.Remote)
	}
	src, dst := engine.ShellQuote(fp.Local), engine.ShellQuote(target)
	if p.spec.Direction == "pull" {
		src, dst = dst, src
	}
	parts := append([]string{"rclone", sub, src, dst}, opts...)
	parts = append(parts, "--transfers", strconv.Itoa(p.spec.Transfers))
	if p.spec.LimitKiB > 0 {
		parts = append(parts, "--bwlimit", fmt.Sprintf("%dKi", p.spec.LimitKiB))
	}
	if p.spec.Compare == "checksum" {
		parts = append(parts, "--checksum")
	}
	for _, line := range filter.ToRclone(p.spec.Excludes) {
		parts = append(parts, "--filter", engine.ShellQuote(line))
	}
	return strings.Join(parts, " ")
}
