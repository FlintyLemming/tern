// Package runner executes one run from start to finish: pre-task command,
// the engine with its retries, post-task command, and the run record.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"tern/internal/clock"
	"tern/internal/engine"
	"tern/internal/events"
	"tern/internal/hooks"
	"tern/internal/model"
	"tern/internal/redact"
	"tern/internal/rundir"
	"tern/internal/runlog"
	"tern/internal/store"
)

// Causes with which the context passed to Execute is canceled.
var (
	ErrCanceled = errors.New("canceled by the user")
	ErrShutdown = errors.New("tern is shutting down")

	errMaxRuntime = errors.New("max runtime exceeded")
)

const maxSummaryLen = 1000

// Slot is the execution slot a run holds. The runner gives it up while it
// waits for a retry, so queued runs can go first.
type Slot interface {
	Release()
	Acquire(ctx context.Context) error
}

// Job is one run to execute.
type Job struct {
	RunID   string
	Task    model.Task // snapshot taken when the run was queued
	Req     engine.Request
	Trigger model.Trigger
	// Finished, when set, is called once the run record is final and
	// before the final run.updated event goes out.
	Finished func()
}

type Runner struct {
	Store               *store.Store
	Registry            *engine.Registry
	Hooks               hooks.Runner
	Events              *events.Hub
	Clock               clock.Clock
	DataDir, RuntimeDir string
	Log                 *slog.Logger

	progress sync.Map // run ID → progressState
}

type progressState struct {
	last      model.Progress
	published time.Time
}

// Progress returns the latest progress an engine reported for a run that
// is still executing.
func (r *Runner) Progress(runID string) (model.Progress, bool) {
	v, ok := r.progress.Load(runID)
	if !ok {
		return model.Progress{}, false
	}
	return v.(progressState).last, true
}

func (r *Runner) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// execution is the state of one Execute call.
type execution struct {
	r    *Runner
	j    Job
	slot Slot
	run  model.Run
	bg   context.Context // for bookkeeping that must outlive cancellation

	dir  *rundir.Dir
	logw *runlog.Writer
	out  io.WriteCloser // redacting writer in front of logw
	red  *redact.Redactor
	eng  engine.Engine

	units   []model.UnitResult
	kinds   []model.ErrKind
	forced  model.Status // set when the status does not come from the units
	kind    model.ErrKind
	summary string
	warn    bool
}

func (x *execution) logf(format string, args ...any) {
	if x.out != nil {
		fmt.Fprintf(x.out, "[tern %s] %s\n", x.r.Clock.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
}

// save records an in-progress status and tells subscribers of a change. A failing
// store must not stop the run, so errors only reach the process log.
func (x *execution) save(st model.Status) {
	changed := x.run.Status != st
	x.run.Status = st
	if err := x.r.Store.UpdateRun(x.bg, x.run.ID, st, x.run.Attempts); err != nil {
		x.r.logger().Error("run record not updated", "run", x.run.ID, "status", st, "err", err)
	}
	if !changed {
		return // only the attempt counter moved; not worth an event
	}
	x.r.Events.Publish(events.Event{Type: events.RunUpdated, Data: x.run})
}

func (x *execution) fail(kind model.ErrKind, summary string) {
	x.forced, x.kind, x.summary = model.StatusFailed, kind, summary
}

// Execute runs j. On entry the run is stored as queued and the caller holds
// an execution slot; on return the run is final and the slot is the
// caller's to release (if the run was cut short while waiting for a retry,
// it is not held). Execute never panics on a failing store: the run is
// completed, post-task command included, and returned as it would have
// been stored.
func (r *Runner) Execute(ctx context.Context, j Job, slot Slot) model.Run {
	x := &execution{r: r, j: j, slot: slot, bg: context.WithoutCancel(ctx)}
	run, err := r.Store.GetRun(x.bg, j.RunID)
	if err != nil {
		r.logger().Error("run record not readable", "run", j.RunID, "err", err)
		run = model.Run{ID: j.RunID, TaskID: j.Task.ID, TaskName: j.Task.Name, Kind: j.Req.Kind, Trigger: j.Trigger, StartedAt: r.Clock.Now().UTC().Truncate(time.Second)}
	}
	x.run = run
	began := r.Clock.Now()
	x.save(model.StatusRunning)

	if x.prepare() {
		x.pipeline(ctx, began)
	}
	return x.finish()
}

// prepare sets up the run directory, the log and the engine. It returns
// false, with the failure recorded, when the run cannot start.
func (x *execution) prepare() bool {
	r := x.r
	x.red = redact.New()
	logw, err := runlog.Create(r.DataDir, x.run.ID)
	if err != nil {
		// No log is not a reason to skip a backup.
		r.logger().Error("run log not created", "run", x.run.ID, "err", err)
		x.out = x.red.Writer(io.Discard)
	} else {
		x.logw = logw
		x.out = x.red.Writer(logw)
	}
	x.logf("%s run of task %q, trigger %s", x.run.Kind, x.j.Task.Name, x.run.Trigger)

	if x.dir, err = rundir.New(r.RuntimeDir, x.run.ID); err != nil {
		x.fail(model.KindConfig, "cannot create the run directory: "+err.Error())
		return false
	}
	if x.eng, err = r.Registry.Build(x.j.Task, x.j.Req); err != nil {
		x.fail(model.KindOf(err), err.Error())
		return false
	}
	return true
}

func (x *execution) hookVars() hooks.Vars {
	t := x.j.Task
	return hooks.Vars{TaskID: t.ID, TaskName: t.Name, TaskType: string(t.Type), Trigger: string(x.run.Trigger)}
}

func (x *execution) pipeline(parent context.Context, began time.Time) {
	r, task := x.r, x.j.Task
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	if task.MaxRuntimeSec > 0 {
		// Counted on tern's clock from the moment the run started, retry
		// waits included.
		limit := r.Clock.After(time.Duration(task.MaxRuntimeSec) * time.Second)
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-limit:
				cancel(errMaxRuntime)
			case <-stop:
			}
		}()
	}

	withHooks := x.run.Kind.RunsHooks()
	engineRan := false
	if withHooks {
		if err := r.Hooks.Run(ctx, task.Pre, x.hookVars(), x.out); err != nil {
			x.logf("pre-task command failed: %v", err)
			if !task.Pre.IgnoreFailure {
				x.fail("", "pre-task command failed: "+err.Error())
			}
		}
	}
	if x.forced == "" {
		engineRan = true
		x.attempts(ctx)
	}

	// A run that was cut short takes its status from the reason. One that
	// got all its work done before the cancel arrived keeps its result.
	if cause := context.Cause(ctx); cause != nil && !(engineRan && x.allUnitsOK()) {
		switch {
		case errors.Is(cause, ErrCanceled):
			x.forced, x.kind, x.summary = model.StatusCanceled, "", "canceled"
		case errors.Is(cause, errMaxRuntime):
			x.fail("", fmt.Sprintf("max runtime exceeded (%ds)", task.MaxRuntimeSec))
		default: // ErrShutdown, or the caller's context ended without a cause
			x.forced, x.kind, x.summary = model.StatusInterrupted, "", "interrupted: "+cause.Error()
		}
		x.logf("%s", x.summary)
	}

	if withHooks {
		status := x.status()
		_, summary := x.failure()
		vars := x.hookVars()
		bytes, files := x.totals()
		vars.Result = &hooks.ResultVars{
			Status:      string(status),
			DurationSec: int64(r.Clock.Now().Sub(began) / time.Second),
			Bytes:       bytes, Files: files, Error: summary,
		}
		// Always runs, and is not cut short by the cancellation of the run:
		// "stop service / start service" pairs must not be left half done.
		if err := r.Hooks.Run(x.bg, task.Post, vars, x.out); err != nil {
			x.logf("post-task command failed: %v", err)
			x.warn = true
		}
	}
}

func (x *execution) allUnitsOK() bool {
	for _, u := range x.units {
		if !unitOK(u.Status) {
			return false
		}
	}
	return len(x.units) > 0
}

// attempts is the retry loop around the engine.
func (x *execution) attempts(ctx context.Context) {
	r, retry := x.r, x.j.Task.Retry
	names := x.eng.Units()
	x.units = make([]model.UnitResult, len(names))
	x.kinds = make([]model.ErrKind, len(names))
	for i, name := range names {
		x.units[i].Name = name
	}
	env := engine.Env{Run: x.run, Dir: x.dir, Log: x.out, Redact: x.red, Progress: x.reportProgress}

	for attempt := 1; ; attempt++ {
		x.run.Attempts = attempt
		x.save(model.StatusRunning)
		x.logf("attempt %d", attempt)
		env.Run = x.run

		if err := x.eng.Preflight(ctx, env); err != nil {
			x.logf("preflight failed: %v", err)
			for i := range x.units {
				if !unitOK(x.units[i].Status) {
					x.units[i].Status, x.units[i].Error, x.kinds[i] = model.StatusFailed, err.Error(), model.KindOf(err)
				}
			}
		} else {
			for i := range x.units {
				if unitOK(x.units[i].Status) {
					continue // done in an earlier attempt
				}
				if ctx.Err() != nil {
					break
				}
				res, err := x.eng.RunUnit(ctx, env, i)
				res.Name = names[i]
				if err != nil {
					res.Status, res.Error, x.kinds[i] = model.StatusFailed, err.Error(), model.KindOf(err)
					x.logf("%s failed: %v", names[i], err)
				} else {
					if res.Status != model.StatusWarning {
						res.Status = model.StatusSuccess
					}
					res.Error, x.kinds[i] = "", ""
				}
				x.units[i] = res
			}
		}

		failed, retryable := false, false
		for i, u := range x.units {
			if !unitOK(u.Status) {
				failed = true
				retryable = retryable || x.kinds[i].Retryable()
			}
		}
		if !failed || !retryable || attempt > retry.Retries || ctx.Err() != nil {
			return
		}

		if err := x.eng.Cleanup(ctx, env); err != nil {
			x.logf("cleanup before retry failed: %v", err)
		}
		delay := retry.Delay(attempt)
		x.logf("retry %d of %d in %s", attempt, retry.Retries, delay)
		x.save(model.StatusRetryWait)
		x.slot.Release()
		select {
		case <-r.Clock.After(delay):
		case <-ctx.Done():
			return
		}
		if err := x.slot.Acquire(ctx); err != nil {
			return
		}
	}
}

// reportProgress remembers the latest progress and publishes at most one
// run.progress per second.
func (x *execution) reportProgress(p model.Progress) {
	now := x.r.Clock.Now()
	st := progressState{last: p}
	if prev, ok := x.r.progress.Load(x.run.ID); ok {
		st.published = prev.(progressState).published
	}
	publish := st.published.IsZero() || now.Sub(st.published) >= time.Second || now.Before(st.published)
	if publish {
		st.published = now
	}
	x.r.progress.Store(x.run.ID, st)
	if publish {
		x.r.Events.Publish(events.Event{Type: events.RunProgress, Data: events.Progress{RunID: x.run.ID, Progress: p}})
	}
}

func (x *execution) status() model.Status {
	if x.forced != "" {
		return x.forced
	}
	return FinalStatus(x.units, x.warn)
}

// failure returns the error kind and summary of the run: what forced its
// status, or else the first failed unit.
func (x *execution) failure() (model.ErrKind, string) {
	if x.forced != "" {
		return x.kind, x.summary
	}
	for i, u := range x.units {
		if !unitOK(u.Status) {
			return x.kinds[i], u.Error
		}
	}
	return "", ""
}

func (x *execution) totals() (bytes, files int64) {
	for _, u := range x.units {
		bytes += u.Bytes
		files += u.Files
	}
	return bytes, files
}

// finish writes the final record and releases everything the run held.
func (x *execution) finish() model.Run {
	r := x.r
	run := &x.run
	run.Status = x.status()
	run.ErrorKind, run.ErrorSummary = x.failure()
	if len(run.ErrorSummary) > maxSummaryLen {
		run.ErrorSummary = run.ErrorSummary[:maxSummaryLen]
	}
	run.Bytes, run.Files = x.totals()
	run.FinishedAt = r.Clock.Now().UTC().Truncate(time.Second)
	for i := range x.units {
		if x.units[i].Status == "" { // never got its turn
			x.units[i].Status = model.StatusFailed
			if run.Status == model.StatusCanceled || run.Status == model.StatusInterrupted {
				x.units[i].Status = run.Status
			}
		}
	}
	run.Units = x.units
	x.logf("finished: %s", run.Status)

	seq, err := r.Store.FinalizeRun(x.bg, *run)
	if err != nil && !errors.Is(err, store.ErrFinalized) {
		r.logger().Error("run record not finalized, retrying once", "run", run.ID, "status", run.Status, "err", err)
		seq, err = r.Store.FinalizeRun(x.bg, *run)
	}
	if err != nil {
		r.logger().Error("run record not finalized; the result is lost", "run", run.ID, "task", run.TaskName,
			"status", run.Status, "error_summary", run.ErrorSummary, "err", err)
	}
	run.Seq = seq
	r.progress.Delete(run.ID)
	if x.j.Finished != nil {
		x.j.Finished()
	}
	r.Events.Publish(events.Event{Type: events.RunUpdated, Data: *run})

	if x.out != nil {
		x.out.Close()
	}
	if x.logw != nil {
		x.logw.Close()
	}
	if c, ok := x.eng.(io.Closer); ok {
		if err := c.Close(); err != nil {
			r.logger().Warn("engine close failed", "run", run.ID, "err", err)
		}
	}
	if x.dir != nil {
		if err := x.dir.Close(); err != nil {
			r.logger().Error("run directory not removed", "run", run.ID, "dir", x.dir.Path, "err", err)
		}
	}
	return *run
}
