// Package scheduler decides when which run is handed to the runner: cron
// triggers, the queue, the concurrency limit, catch-up after downtime and
// the "too long without success" rule.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"tern/internal/clock"
	"tern/internal/engine"
	"tern/internal/events"
	"tern/internal/model"
	"tern/internal/runner"
	"tern/internal/store"
)

var (
	ErrActive    = errors.New("scheduler: the task already has a run queued or running")
	ErrPaused    = errors.New("scheduler: paused")
	ErrNotActive = errors.New("scheduler: the run is not queued or running")
)

const (
	MetaMaxConcurrent = "settings.max_concurrent"
	metaLastTick      = "sched.last_tick"

	// onTimeWindow separates "fired a little late" from "missed".
	onTimeWindow = 5 * time.Minute
	maxSleep     = time.Minute
	// maxLookBack bounds the walk over missed scheduled times after a long
	// downtime or a wild clock jump.
	maxLookBack = 366 * 24 * time.Hour
)

type Deps struct {
	Store  *store.Store
	Runner *runner.Runner
	Clock  clock.Clock
	Events *events.Hub
	Log    *slog.Logger
}

type Scheduler struct {
	d   Deps
	sem *slots

	mu     sync.Mutex
	active map[string]*activeRun // by task ID: at most one run per task
	paused bool
	closed bool

	passMu        sync.Mutex // one scheduling pass at a time
	lastTick      time.Time
	lastHousekeep time.Time

	wake     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	loopDone chan struct{}
	wg       sync.WaitGroup // run goroutines
}

type activeRun struct {
	runID  string
	cancel context.CancelCauseFunc
}

func New(d Deps) *Scheduler {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Scheduler{
		d:      d,
		sem:    &slots{capacity: 1},
		active: map[string]*activeRun{},
		wake:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
	}
}

// Start finalizes runs a previous process left unfinished, deals with
// scheduled times missed while tern was down, and starts the main loop.
func (s *Scheduler) Start(ctx context.Context) error {
	now := s.d.Clock.Now()
	n, err := s.d.Store.InterruptLeftovers(ctx, now)
	if err != nil {
		return err
	}
	if n > 0 {
		s.d.Log.Warn("runs left unfinished by the previous process were marked interrupted", "count", n)
	}
	s.lastTick = now // first start: nothing was missed
	if raw, ok, err := s.d.Store.GetMeta(ctx, metaLastTick); err != nil {
		return err
	} else if ok {
		if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
			s.lastTick = time.Unix(sec, 0)
		}
	}
	s.pass() // scheduled times missed while tern was down
	s.loopDone = make(chan struct{})
	go s.loop()
	return nil
}

func (s *Scheduler) loop() {
	defer close(s.loopDone)
	var (
		timer   <-chan time.Time
		timerAt time.Time
	)
	for {
		d := s.pass()
		// Keep a pending timer that fires early enough; a wake-up must not
		// leave a trail of abandoned timers.
		if at := s.d.Clock.Now().Add(d); timer == nil || at.Before(timerAt) {
			timer, timerAt = s.d.Clock.After(d), at
		}
		select {
		case <-timer:
			timer = nil
		case <-s.wake:
		case <-s.stop:
			return
		}
	}
}

func (s *Scheduler) wakeLoop() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) maxConcurrent(ctx context.Context) int {
	return s.metaInt(ctx, MetaMaxConcurrent, DefaultMaxConcurrent)
}

// Reload applies changed settings now and has the loop look at the tasks
// again. Call it after tasks or settings change.
func (s *Scheduler) Reload() {
	s.sem.setCapacity(s.maxConcurrent(context.Background()))
	s.wakeLoop()
}

// Sync runs one scheduling pass on the calling goroutine and returns when
// it is done: every scheduled time up to now has been dealt with.
func (s *Scheduler) Sync() { s.pass() }

// pass deals with every scheduled time between the previous pass and now,
// and returns how long the loop may sleep.
func (s *Scheduler) pass() time.Duration {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	ctx := context.Background()
	s.sem.setCapacity(s.maxConcurrent(ctx))

	s.mu.Lock()
	closed, idle := s.closed, s.paused || s.closed
	s.mu.Unlock()
	if !closed {
		s.housekeepIfDue(ctx, s.d.Clock.Now()) // needs no credentials: also while paused
	}
	if idle {
		// lastTick stays put: what comes due meanwhile is handled as
		// "missed" once the pause ends.
		return maxSleep
	}

	now := s.d.Clock.Now()
	sleep := maxSleep
	if now.Before(s.lastTick) {
		// The clock went back. Nothing fires now, and thanks to
		// last_fire_at nothing fires twice when the time comes round again.
		s.d.Log.Warn("clock moved backwards", "from", s.lastTick, "to", now)
	} else {
		tasks, err := s.d.Store.ListTasks(ctx)
		if err != nil {
			s.d.Log.Error("scheduler cannot list tasks", "err", err)
			return maxSleep
		}
		for _, t := range tasks {
			if !t.Enabled || t.Schedule == "" {
				continue
			}
			if wait, ok := s.consider(ctx, t, now); ok {
				sleep = min(sleep, wait)
			}
		}
	}
	s.lastTick = now
	if err := s.d.Store.SetMeta(ctx, metaLastTick, strconv.FormatInt(now.Unix(), 10)); err != nil {
		s.d.Log.Error("scheduler cannot persist its position", "err", err)
	}
	return sleep
}

// consider handles the most recent scheduled time of t in (lastTick, now],
// if there is one, and returns the wait until t's next one.
func (s *Scheduler) consider(ctx context.Context, t model.Task, now time.Time) (time.Duration, bool) {
	sched, err := parse(t.Schedule)
	if err != nil {
		s.d.Log.Warn("task has an unusable schedule", "task", t.Name, "err", err)
		return 0, false
	}
	from := s.lastTick
	if earliest := now.Add(-maxLookBack); from.Before(earliest) {
		from = earliest
	}
	if f := next(sched, from); !f.IsZero() && !f.After(now) {
		// Several missed times collapse into the latest one.
		for {
			n := next(sched, f)
			if n.IsZero() || n.After(now) {
				break
			}
			f = n
		}
		s.fire(ctx, t, f, now)
	}
	upcoming := next(sched, now)
	if upcoming.IsZero() {
		return 0, false
	}
	return upcoming.Sub(now), true
}

// fire handles the scheduled time f of task t, at most once ever.
func (s *Scheduler) fire(ctx context.Context, t model.Task, f, now time.Time) {
	st, err := s.d.Store.GetTaskState(ctx, t.ID)
	if err != nil {
		s.d.Log.Error("scheduler cannot read task state", "task", t.Name, "err", err)
		return
	}
	if !f.After(st.LastFireAt) {
		return // already dealt with, before a restart or a clock step back
	}
	// Recorded first: after a crash right here the time counts as handled,
	// which loses one run instead of risking two.
	if err := s.d.Store.SetTaskState(ctx, t.ID, func(ts *store.TaskState) { ts.LastFireAt = f }); err != nil {
		s.d.Log.Error("scheduler cannot record a scheduled time", "task", t.Name, "err", err)
		return
	}
	trigger := model.TriggerSchedule
	if now.Sub(f) > onTimeWindow {
		if !t.CatchUp {
			s.d.Log.Info("missed scheduled time is not caught up", "task", t.Name, "scheduled", f)
			return
		}
		trigger = model.TriggerCatchUp
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused || s.closed {
		return
	}
	if _, busy := s.active[t.ID]; busy {
		s.recordSkipped(ctx, t, trigger, now)
		return
	}
	if _, err := s.startLocked(ctx, t, engine.Request{}, trigger); err != nil {
		s.d.Log.Error("scheduled run not started", "task", t.Name, "err", err)
	}
}

// recordSkipped writes the run record of a scheduled time that found the
// previous run of the same task still going.
func (s *Scheduler) recordSkipped(ctx context.Context, t model.Task, trigger model.Trigger, now time.Time) {
	at := now.UTC().Truncate(time.Second)
	run := model.Run{ID: model.NewID(), TaskID: t.ID, TaskName: t.Name, Kind: t.RunKind(), Trigger: trigger,
		Status: model.StatusQueued, StartedAt: at}
	err := s.d.Store.CreateRun(ctx, run)
	if err == nil {
		run.Status, run.FinishedAt = model.StatusSkipped, at
		run.ErrorSummary = "the previous run of this task was still in progress"
		run.Seq, err = s.d.Store.FinalizeRun(ctx, run)
	}
	if err != nil {
		s.d.Log.Error("skipped run not recorded", "task", t.Name, "err", err)
		return
	}
	s.d.Events.Publish(events.Event{Type: events.RunUpdated, Data: run})
}

// Enqueue queues a run of the task as it is right now; later edits do not
// affect this run. It fails with ErrActive when the task already has a run
// queued or running, and with ErrPaused while the scheduler is paused.
func (s *Scheduler) Enqueue(taskID string, req engine.Request, tr model.Trigger) (runID string, err error) {
	ctx := context.Background()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused || s.closed {
		return "", ErrPaused
	}
	if _, busy := s.active[taskID]; busy {
		return "", ErrActive
	}
	t, err := s.d.Store.GetTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	return s.startLocked(ctx, t, req, tr)
}

// startLocked creates the queued run record and the goroutine that will
// carry it. s.mu is held.
func (s *Scheduler) startLocked(ctx context.Context, t model.Task, req engine.Request, tr model.Trigger) (string, error) {
	if req.Kind == "" {
		req.Kind = t.RunKind()
	}
	run := model.Run{ID: model.NewID(), TaskID: t.ID, TaskName: t.Name, Kind: req.Kind, Trigger: tr,
		Status: model.StatusQueued, StartedAt: s.d.Clock.Now().UTC().Truncate(time.Second)}
	if err := s.d.Store.CreateRun(ctx, run); err != nil {
		return "", err
	}
	runCtx, cancel := context.WithCancelCause(context.Background())
	a := &activeRun{runID: run.ID, cancel: cancel}
	s.active[t.ID] = a
	s.d.Events.Publish(events.Event{Type: events.RunUpdated, Data: run})
	s.wg.Add(1)
	go s.carry(runCtx, a, run, runner.Job{RunID: run.ID, Task: t, Req: req, Trigger: tr})
	return run.ID, nil
}

// carry takes one run from the queue to its end.
func (s *Scheduler) carry(ctx context.Context, a *activeRun, run model.Run, job runner.Job) {
	defer s.wg.Done()
	defer a.cancel(nil)
	slot := &jobSlot{sem: s.sem}
	var once sync.Once
	// done frees the slot and the task. It runs before the final event is
	// published, so whoever reacts to that event finds the task free.
	done := func() {
		once.Do(func() {
			slot.Release()
			s.mu.Lock()
			if s.active[run.TaskID] == a {
				delete(s.active, run.TaskID)
			}
			s.mu.Unlock()
			s.wakeLoop()
		})
	}
	defer done()

	if err := slot.Acquire(ctx); err != nil {
		// Canceled or shut down while still queued: nothing ran.
		run.Status, run.ErrorSummary = model.StatusInterrupted, "interrupted before it started"
		if errors.Is(context.Cause(ctx), runner.ErrCanceled) {
			run.Status, run.ErrorSummary = model.StatusCanceled, "canceled while queued"
		}
		run.FinishedAt = s.d.Clock.Now().UTC().Truncate(time.Second)
		seq, err := s.d.Store.FinalizeRun(context.Background(), run)
		if err != nil {
			s.d.Log.Error("queued run not finalized", "run", run.ID, "err", err)
		}
		run.Seq = seq
		done()
		s.d.Events.Publish(events.Event{Type: events.RunUpdated, Data: run})
		return
	}
	job.Finished = done
	s.d.Runner.Execute(ctx, job, slot)
}

// Cancel stops a queued or running run. A queued run ends as canceled at
// once; a running one is terminated and still gets its post-task command.
func (s *Scheduler) Cancel(runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.active {
		if a.runID == runID {
			a.cancel(runner.ErrCanceled)
			return nil
		}
	}
	return ErrNotActive
}

// Active reports whether the task has a run queued or running.
func (s *Scheduler) Active(taskID string) bool {
	_, ok := s.ActiveRun(taskID)
	return ok
}

// ActiveRun returns the ID of the task's queued or running run.
func (s *Scheduler) ActiveRun(taskID string) (runID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.active[taskID]
	if !ok {
		return "", false
	}
	return a.runID, true
}

// SetPaused stops (true) or resumes (false) scheduling. tern pauses while
// the vault is not unlocked: no run could get at its credentials.
func (s *Scheduler) SetPaused(p bool) {
	s.mu.Lock()
	s.paused = p
	s.mu.Unlock()
	if !p {
		s.wakeLoop()
	}
}

// Shutdown stops scheduling, cancels every run with runner.ErrShutdown and
// waits, until ctx ends, for them to wind down.
func (s *Scheduler) Shutdown(ctx context.Context) {
	s.mu.Lock()
	s.closed = true
	for _, a := range s.active {
		a.cancel(runner.ErrShutdown)
	}
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })

	finished := make(chan struct{})
	go func() {
		if s.loopDone != nil {
			<-s.loopDone
		}
		s.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		s.d.Log.Warn("shutdown deadline reached with runs still winding down")
	}
}

// slots is the global concurrency limit: a FIFO semaphore whose capacity
// can change while it is in use.
type slots struct {
	mu       sync.Mutex
	capacity int
	used     int
	waiters  []chan struct{}
}

func (s *slots) grantLocked() {
	for s.used < s.capacity && len(s.waiters) > 0 {
		close(s.waiters[0])
		s.waiters = s.waiters[1:]
		s.used++
	}
}

func (s *slots) setCapacity(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.capacity = n
	s.grantLocked()
}

func (s *slots) acquire(ctx context.Context) error {
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return context.Cause(ctx)
	}
	ch := make(chan struct{})
	s.waiters = append(s.waiters, ch)
	s.grantLocked()
	s.mu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, w := range s.waiters {
		if w == ch {
			s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
			return context.Cause(ctx)
		}
	}
	// Granted in the same instant: give it back.
	s.used--
	s.grantLocked()
	return context.Cause(ctx)
}

func (s *slots) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used--
	s.grantLocked()
}

// jobSlot is one run's hold on the semaphore. Releasing twice, or without
// holding, does nothing, so the scheduler can always release at the end.
type jobSlot struct {
	sem  *slots
	mu   sync.Mutex
	held bool
}

func (j *jobSlot) Acquire(ctx context.Context) error {
	if err := j.sem.acquire(ctx); err != nil {
		return err
	}
	j.mu.Lock()
	j.held = true
	j.mu.Unlock()
	return nil
}

func (j *jobSlot) Release() {
	j.mu.Lock()
	held := j.held
	j.held = false
	j.mu.Unlock()
	if held {
		j.sem.release()
	}
}
