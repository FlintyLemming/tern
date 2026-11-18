package rsync

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"tern/internal/engine"
	"tern/internal/engine/filter"
	"tern/internal/engine/synccore"
	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/redact"
	"tern/internal/sshx"
)

const excludeFile = "rsync-excludes"

// Deps is what the rsync planner needs from the rest of the process.
type Deps struct {
	Access engine.AccessOpener
	Bin    string // default "rsync"
	SSHBin string // default "ssh"; stops the remote end of a dead transfer
}

type planner struct {
	d    Deps
	spec model.SyncSpec
	conn model.Connection

	mu        sync.Mutex
	prepared  bool
	sshConfig string
	excludes  string // "" without exclude rules
	release   func()
	// dirDeletes is what the latest dry run of each pair would delete in
	// directories: rsync counts those against --max-delete too.
	dirDeletes map[model.FolderPair]int
	// remotePID is the remote rsync of each pair's latest transfer that
	// did not end normally: it may still be running.
	remotePID map[model.FolderPair]string
}

// NewPlanner returns the planner for an rsync sync task. conn is only
// used to describe the equivalent command line.
func NewPlanner(t model.Task, conn model.Connection, d Deps) synccore.Planner {
	if d.Bin == "" {
		d.Bin = "rsync"
	}
	if d.SSHBin == "" {
		d.SSHBin = "ssh"
	}
	return &planner{d: d, spec: *t.Sync, conn: conn, dirDeletes: map[model.FolderPair]int{}, remotePID: map[model.FolderPair]string{}}
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
	path := ""
	if len(p.spec.Excludes) > 0 {
		lines := filter.ToRsync(p.spec.Excludes)
		if path, err = env.Dir.WriteSecret(excludeFile, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
			release()
			return fmt.Errorf("rsync: write the exclude file: %w", err)
		}
	}
	p.sshConfig, p.excludes, p.release, p.prepared = acc.SSHConfig, path, release, true
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

// exec runs one rsync call. stdout lines go through onStdout, which
// returns what to write to the run log; stderr goes to the log as is,
// except the remote end's PID line, which goes to onPID when that is set.
func (p *planner) exec(ctx context.Context, env engine.Env, args []string, onStdout func([]byte) string, onPID func(string)) (proc.Result, error) {
	fmt.Fprintf(env.Log, "$ rsync %s\n", strings.Join(args, " "))
	res, err := proc.Run(ctx, proc.Cmd{
		Path: p.d.Bin,
		Args: args,
		OnStdout: func(line []byte) {
			if text := onStdout(line); text != "" {
				fmt.Fprintln(env.Log, text)
			}
		},
		OnStderr: func(line []byte) {
			if pid, ok := strings.CutPrefix(string(line), pidPrefix); ok && pidRE.MatchString(pid) {
				if onPID != nil {
					onPID(pid)
				}
				return
			}
			fmt.Fprintf(env.Log, "%s\n", line)
		},
	})
	if err != nil {
		return res, &model.KindError{Kind: model.KindConfig, Msg: "cannot start rsync", Err: err}
	}
	if res.Signaled {
		return res, &model.KindError{Kind: model.KindUnknown, Msg: "rsync was stopped", Err: context.Cause(ctx)}
	}
	return res, nil
}

// reason is the most telling line of rsync's stderr, redacted: it ends up
// in a run record, which is not behind the log's redactor. rsync closes
// with a generic "rsync error: … (code N)" line; the one before it says
// what went wrong.
func reason(red *redact.Redactor, tail string) string {
	lines := strings.Split(strings.TrimSpace(tail), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "rsync error:") {
			line = l
			break
		}
	}
	if len(line) > 300 {
		line = line[:300]
	}
	var buf bytes.Buffer
	w := red.Writer(&buf)
	fmt.Fprintln(w, line)
	w.Close()
	return strings.TrimSpace(buf.String())
}

func failure(env engine.Env, what string, res proc.Result, kind model.ErrKind) error {
	msg := fmt.Sprintf("rsync failed (exit %d)", res.ExitCode)
	if what != "" {
		msg = fmt.Sprintf("rsync %s failed (exit %d)", what, res.ExitCode)
	}
	if r := reason(env.Redact, res.StderrTail); r != "" {
		msg += ": " + r
	}
	return &model.KindError{Kind: kind, Msg: msg}
}

// A --list-only line: mode, size, date, time, name.
var listRE = regexp.MustCompile(`^\S+\s+[0-9,]+\s+\S+\s+\S+\s(.*)$`)

var pidRE = regexp.MustCompile(`^[0-9]{1,10}$`)

func (p *planner) RemoteState(ctx context.Context, env engine.Env, fp model.FolderPair) (synccore.SourceState, error) {
	args := []string{"-s", "--list-only", timeoutArg, "-e", shell(p.sshConfig), remote(fp.Remote)}
	entries := 0
	res, err := p.exec(ctx, env, args, func(line []byte) string {
		if m := listRE.FindSubmatch(line); m != nil && string(m[1]) != "." {
			entries++
		}
		return "" // a listing is no news for the log
	}, nil)
	if err != nil {
		return 0, err
	}
	if res.ExitCode == 23 && strings.Contains(res.StderrTail, "No such file or directory") {
		return synccore.SourceMissing, nil
	}
	if kind, _ := ExitKind(res.ExitCode, res.StderrTail); res.ExitCode != 0 {
		if kind == "" { // a listing has no files to vanish: 24 is not fine here
			kind = model.KindUnknown
		}
		return 0, failure(env, "--list-only", res, kind)
	}
	if entries == 0 {
		return synccore.SourceEmpty, nil
	}
	return synccore.SourceOK, nil
}

func (p *planner) Plan(ctx context.Context, env engine.Env, fp model.FolderPair) (synccore.Plan, error) {
	parser := NewParser(env.Progress)
	parser.quiet, parser.planning = true, true
	res, err := p.exec(ctx, env, Args(p.spec, fp, p.sshConfig, p.excludes, true, -1), parser.Line, nil)
	if err != nil {
		return synccore.Plan{}, err
	}
	if kind, _ := ExitKind(res.ExitCode, res.StderrTail); kind != "" {
		return synccore.Plan{}, failure(env, "--dry-run", res, kind)
	}
	p.mu.Lock()
	p.dirDeletes[fp] = parser.DirDeletes()
	p.mu.Unlock()
	return parser.Plan(), nil
}

func (p *planner) Transfer(ctx context.Context, env engine.Env, fp model.FolderPair, maxDelete int) (model.UnitResult, error) {
	if maxDelete >= 0 {
		p.mu.Lock()
		maxDelete += p.dirDeletes[fp]
		p.mu.Unlock()
	}
	p.stopStale(ctx, env, fp)
	parser := NewParser(env.Progress)
	parser.quiet = true
	var remotePID string
	res, err := p.exec(ctx, env, Args(p.spec, fp, p.sshConfig, p.excludes, false, maxDelete), parser.Line,
		func(pid string) { remotePID = pid })
	out := model.UnitResult{}
	out.Bytes, out.Files = parser.Totals()
	kind, warning := ExitKind(res.ExitCode, res.StderrTail)
	if remotePID != "" && (err != nil || kind != "") {
		p.mu.Lock()
		p.remotePID[fp] = remotePID // it may outlive this transfer
		p.mu.Unlock()
	}
	if err != nil {
		return out, err
	}
	if kind != "" {
		return out, failure(env, "", res, kind)
	}
	out.Status = model.StatusSuccess
	if warning {
		out.Status = model.StatusWarning
		fmt.Fprintln(env.Log, "some source files vanished while rsync ran")
	}
	return out, nil
}

// Describe is the rsync command line that syncs the pair without tern:
// plain ssh with the connection's port instead of tern's generated config.
func (p *planner) Describe(fp model.FolderPair) string {
	parts := []string{"rsync", "-aH", "--partial-dir=.tern-partial", timeoutArg}
	if p.spec.DeleteMode == "mirror" {
		parts = append(parts, "--delete")
	}
	if p.spec.LimitKiB > 0 {
		parts = append(parts, fmt.Sprintf("--bwlimit=%d", p.spec.LimitKiB))
	}
	if p.spec.Compare == "checksum" {
		parts = append(parts, "--checksum")
	}
	for _, rule := range filter.ToRsync(p.spec.Excludes) {
		parts = append(parts, engine.ShellQuote("--exclude="+rule))
	}
	parts = append(parts, "-e", fmt.Sprintf(`"ssh -p %d"`, p.conn.Port))
	host := p.conn.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	// User and host are validated plain words; only the path needs quoting.
	src, dst := engine.ShellQuote(dir(fp.Local)), p.conn.Username+"@"+host+":"+engine.ShellQuote(dir(fp.Remote))
	if p.spec.Direction == "pull" {
		src, dst = dst, src
	}
	return strings.Join(append(parts, src, dst), " ")
}

// stopStale stops the remote rsync of the pair's previous transfer if that
// did not end normally. After a dead link it can run on for minutes, still
// holding the file it was receiving; on SIGTERM it moves that file to the
// partial directory, where this transfer picks it up. Best effort: when it
// cannot be done the transfer runs anyway.
func (p *planner) stopStale(ctx context.Context, env engine.Env, fp model.FolderPair) {
	p.mu.Lock()
	pid, ok := p.remotePID[fp]
	delete(p.remotePID, fp)
	p.mu.Unlock()
	if !ok {
		return
	}
	fmt.Fprintf(env.Log, "stopping the remote rsync of the previous attempt (pid %s) if it still runs\n", pid)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := proc.Run(ctx, proc.Cmd{
		Path:     p.d.SSHBin,
		Args:     []string{"-F", p.sshConfig, sshx.Alias, "sh", "-s"},
		Stdin:    strings.NewReader(stopScript(pid)),
		OnStderr: func(line []byte) { fmt.Fprintf(env.Log, "%s\n", line) },
	})
	if err != nil || res.ExitCode != 0 {
		fmt.Fprintf(env.Log, "could not stop it (exit %d, %v); going on\n", res.ExitCode, err)
	}
}

// stopScript runs in the remote shell. It signals pid and its children
// only while pid still is an rsync server (the number may have been
// reused), waits up to 20 seconds for them to exit, then kills them.
func stopScript(pid string) string {
	return "pid=" + pid + `
cmd=$(tr '\000' ' ' < /proc/$pid/cmdline 2>/dev/null) || exit 0
case "$cmd" in *rsync*--server*) ;; *) exit 0 ;; esac
kids=
for s in /proc/[0-9]*/stat; do
	set -- $(cat "$s" 2>/dev/null)
	[ "$4" = "$pid" ] && kids="$kids $1"
done
kill -TERM $pid $kids 2>/dev/null
i=0
while [ $i -lt 20 ]; do
	alive=
	for p in $pid $kids; do kill -0 $p 2>/dev/null && alive=1; done
	[ -z "$alive" ] && exit 0
	sleep 1
	i=$((i+1))
done
kill -KILL $pid $kids 2>/dev/null
exit 0
`
}
