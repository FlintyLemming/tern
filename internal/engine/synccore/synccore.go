// Package synccore holds everything about a sync that does not depend on
// the protocol: one unit per folder pair, the mirror guards and the dry
// run. rclone and rsync each implement a Planner that only translates a
// folder pair into tool calls; the rules that keep a mirror from wiping a
// destination exist once, here.
package synccore

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"tern/internal/engine"
	"tern/internal/model"
)

// SourceState is what a pull finds at the remote source of a pair.
type SourceState int

const (
	SourceOK SourceState = iota
	SourceEmpty
	SourceMissing
)

// Planner is one protocol's tool, seen from the sync core.
type Planner interface {
	// Prepare opens the access material and writes the tool's config and
	// filter files. It is idempotent: it runs before every attempt.
	Prepare(ctx context.Context, env engine.Env) error
	// RemoteState looks at the remote source of a pull.
	RemoteState(ctx context.Context, env engine.Env, p model.FolderPair) (SourceState, error)
	// Plan is the tool's own dry run of the pair.
	Plan(ctx context.Context, env engine.Env, p model.FolderPair) (Plan, error)
	// Transfer syncs the pair. maxDelete >= 0 is passed to the tool as its
	// own delete limit; < 0 passes none (keep mode never deletes anyway).
	Transfer(ctx context.Context, env engine.Env, p model.FolderPair, maxDelete int) (model.UnitResult, error)
	// Describe is the equivalent command line, without credentials.
	Describe(p model.FolderPair) string
	Close() error
}

// Engine runs a sync task: one unit per folder pair, in order.
type Engine struct {
	spec    model.SyncSpec
	req     engine.Request
	p       Planner
	check   func(context.Context) error
	mounted func(string) (bool, error)
}

var (
	_ engine.Engine = (*Engine)(nil)
	_ io.Closer     = (*Engine)(nil)
)

// New makes the engine for one run of t. check is the connection
// preflight; mounted tells whether a local path is on a mounted volume.
func New(t model.Task, req engine.Request, p Planner, check func(context.Context) error, mounted func(string) (bool, error)) *Engine {
	return &Engine{spec: *t.Sync, req: req, p: p, check: check, mounted: mounted}
}

func (e *Engine) push() bool   { return e.spec.Direction == "push" }
func (e *Engine) mirror() bool { return e.spec.DeleteMode == "mirror" }

// limited reports whether a mirror counts its deletions before it
// transfers: a delete limit of 0 means none, and no dry run.
func (e *Engine) limited() bool { return e.mirror() && e.spec.DeleteLimit() > 0 }

func (e *Engine) unitName(p model.FolderPair) string {
	if e.push() {
		return p.Local + " → " + p.Remote
	}
	return p.Remote + " → " + p.Local
}

func (e *Engine) Units() []string {
	names := make([]string, len(e.spec.Pairs))
	for i, p := range e.spec.Pairs {
		names[i] = e.unitName(p)
	}
	return names
}

func (e *Engine) Preflight(ctx context.Context, env engine.Env) error {
	report(env, model.Progress{Phase: model.PhaseConnect})
	if err := e.check(ctx); err != nil {
		return err
	}
	return e.p.Prepare(ctx, env)
}

func (e *Engine) Cleanup(ctx context.Context, env engine.Env) error { return nil }

func (e *Engine) Close() error { return e.p.Close() }

func (e *Engine) Describe() []string {
	lines := make([]string, len(e.spec.Pairs))
	for i, p := range e.spec.Pairs {
		lines[i] = e.p.Describe(p)
	}
	return lines
}

func configErr(format string, args ...any) error {
	return &model.KindError{Kind: model.KindConfig, Msg: fmt.Sprintf(format, args...)}
}

func guardErr(format string, args ...any) error {
	return &model.KindError{Kind: model.KindGuard, Msg: fmt.Sprintf(format, args...)}
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// checkLocal checks the local end of a pair. A pull never creates its
// target: a missing one usually means the share is not mounted, and what
// tern wrote there would land in the container's own file system and be
// lost when it is recreated.
func (e *Engine) checkLocal(p model.FolderPair) error {
	if e.push() {
		if !isDir(p.Local) {
			return configErr("local source missing: %s", p.Local)
		}
		return nil
	}
	if !isDir(p.Local) {
		return configErr("local target missing or not on a mounted volume: %s", p.Local)
	}
	if ok, err := e.mounted(p.Local); err != nil || !ok {
		return configErr("local target missing or not on a mounted volume: %s", p.Local)
	}
	return nil
}

// sourceGone reports whether the source of a mirror is missing or empty:
// mirroring it would empty the destination.
func (e *Engine) sourceGone(ctx context.Context, env engine.Env, p model.FolderPair) (bool, error) {
	if !e.push() {
		state, err := e.p.RemoteState(ctx, env, p)
		if err != nil {
			return false, err
		}
		return state != SourceOK, nil
	}
	f, err := os.Open(p.Local)
	if err != nil {
		return false, configErr("local source not readable: %s", p.Local)
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if len(names) > 0 {
		return false, nil
	}
	if err != nil && err != io.EOF {
		return false, configErr("local source not readable: %s", p.Local)
	}
	return true, nil
}

// printable keeps one path on one log line: a name with control
// characters (a newline is legal in a file name) is written quoted.
func printable(path string) string {
	if strings.IndexFunc(path, unicode.IsControl) < 0 {
		return path
	}
	return strconv.Quote(path)
}

func logPlan(w io.Writer, plan Plan) {
	for _, path := range plan.Transfers {
		fmt.Fprintf(w, "would transfer: %s\n", printable(path))
	}
	if more := plan.TransferCount - len(plan.Transfers); more > 0 {
		fmt.Fprintf(w, "… and %d more\n", more)
	}
	for _, path := range plan.Deletes {
		fmt.Fprintf(w, "would delete: %s\n", printable(path))
	}
	if more := plan.DeleteCount - len(plan.Deletes); more > 0 {
		fmt.Fprintf(w, "… and %d more\n", more)
	}
	fmt.Fprintf(w, "plan: %d to transfer, %d to delete\n", plan.TransferCount, plan.DeleteCount)
}

// report passes p on when the run wants progress.
func report(env engine.Env, p model.Progress) {
	if env.Progress != nil {
		env.Progress(p)
	}
}

// withProgress returns env with its progress reports passed through f.
func withProgress(env engine.Env, f func(model.Progress) model.Progress) engine.Env {
	if next := env.Progress; next != nil {
		env.Progress = func(p model.Progress) { next(f(p)) }
	}
	return env
}

// withPlanTotals fills in the totals of a transfer from the dry run that
// came before it, when there was one: rclone finds what to transfer as it
// goes, so its own totals only cover what it has queued so far.
func withPlanTotals(pr model.Progress, plan Plan) model.Progress {
	if plan.Bytes <= 0 {
		return pr
	}
	if pr.BytesTotal < plan.Bytes {
		pr.BytesTotal = plan.Bytes
		if pr.SpeedBps > 0 && pr.BytesTotal > pr.BytesDone {
			pr.ETASec = int64(float64(pr.BytesTotal-pr.BytesDone) / pr.SpeedBps)
		} else {
			pr.ETASec = 0
		}
	}
	if fc := int64(plan.TransferCount); pr.FilesTotal < fc {
		pr.FilesTotal = fc
	}
	pr.TotalEstimated = false
	return pr
}

// RunUnit syncs pair i. The steps run in this order and stop at the first
// error: local end, mirror source check, plan, (dry run ends here), delete
// limit, transfer. A sync plans only for a mirror with a delete limit.
// Each step's progress carries its phase.
func (e *Engine) RunUnit(ctx context.Context, env engine.Env, i int) (model.UnitResult, error) {
	p := e.spec.Pairs[i]
	name := e.unitName(p)
	out := model.UnitResult{Name: name}
	env = withProgress(env, func(pr model.Progress) model.Progress { pr.Unit = name; return pr })
	dryRun := e.req.Kind == model.RunDryRun

	report(env, model.Progress{Phase: model.PhaseCheck})
	if err := e.checkLocal(p); err != nil {
		return out, err
	}
	if e.mirror() {
		gone, err := e.sourceGone(ctx, env, p)
		if err != nil {
			return out, err
		}
		if gone {
			return out, guardErr("source is missing or empty; mirror aborted, destination untouched")
		}
	}

	var plan Plan
	if e.limited() || dryRun {
		report(env, model.Progress{Phase: model.PhasePlan})
		// A dry run moves no data: only its counts mean anything.
		planEnv := withProgress(env, func(pr model.Progress) model.Progress {
			return model.Progress{Unit: pr.Unit, Phase: model.PhasePlan, FilesDone: pr.FilesDone, BytesDone: pr.BytesDone, Scanned: pr.Scanned}
		})
		var err error
		if plan, err = e.p.Plan(ctx, planEnv, p); err != nil {
			return out, err
		}
		logPlan(env.Log, plan)
	}
	if dryRun {
		out.Status, out.Files, out.Bytes = model.StatusSuccess, int64(plan.TransferCount), plan.Bytes
		return out, nil
	}

	maxDelete := -1
	if e.limited() {
		if limit := e.spec.DeleteLimit(); plan.DeleteCount > limit && !e.req.AllowDeletes {
			return out, guardErr("would delete %d files (limit %d)", plan.DeleteCount, limit)
		}
		// The tool's own limit is a second gate: if more would go than
		// were just counted, something changed in between.
		maxDelete = plan.DeleteCount
	}
	transferEnv := withProgress(env, func(pr model.Progress) model.Progress {
		pr.Phase = model.PhaseTransfer
		return withPlanTotals(pr, plan)
	})
	report(transferEnv, model.Progress{})
	res, err := e.p.Transfer(ctx, transferEnv, p, maxDelete)
	res.Name = name
	return res, err
}
