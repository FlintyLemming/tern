package scheduler

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"tern/internal/clock"
	"tern/internal/engine"
	"tern/internal/engine/fake"
	"tern/internal/events"
	"tern/internal/model"
	"tern/internal/runner"
	"tern/internal/store"
)

// recorder keeps every run.updated event so tests can wait for a state
// without sleeping.
type recorder struct {
	mu       sync.Mutex
	cond     *sync.Cond
	runs     []model.Run
	timedOut bool
}

func newRecorder(ctx context.Context, hub *events.Hub) *recorder {
	r := &recorder{}
	r.cond = sync.NewCond(&r.mu)
	ch := hub.Subscribe(ctx)
	go func() {
		for e := range ch {
			if run, ok := e.Data.(model.Run); ok && e.Type == events.RunUpdated {
				r.mu.Lock()
				r.runs = append(r.runs, run)
				r.cond.Broadcast()
				r.mu.Unlock()
			}
		}
	}()
	return r
}

// wait returns the first recorded run.updated matching pred.
func (r *recorder) wait(t *testing.T, what string, pred func(model.Run) bool) model.Run {
	t.Helper()
	timer := time.AfterFunc(10*time.Second, func() {
		r.mu.Lock()
		r.timedOut = true
		r.cond.Broadcast()
		r.mu.Unlock()
	})
	defer timer.Stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		for _, run := range r.runs {
			if pred(run) {
				return run
			}
		}
		if r.timedOut {
			t.Fatalf("timed out waiting for %s; saw %d run events", what, len(r.runs))
		}
		r.cond.Wait()
	}
}

func (r *recorder) status(t *testing.T, runID string, st model.Status) model.Run {
	t.Helper()
	return r.wait(t, "run "+runID+" to be "+string(st), func(run model.Run) bool { return run.ID == runID && run.Status == st })
}

func (r *recorder) final(t *testing.T, runID string) model.Run {
	t.Helper()
	return r.wait(t, "run "+runID+" to finish", func(run model.Run) bool { return run.ID == runID && run.Status.Terminal() })
}

type fx struct {
	t       *testing.T
	st      *store.Store
	clk     *clock.Fake
	hub     *events.Hub
	rec     *recorder
	s       *Scheduler
	engines map[string]*fake.Engine // by task name
	created int
}

// newFx builds everything but does not start the scheduler.
func newFx(t *testing.T, start time.Time) *fx {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{t: t, st: st, clk: clock.NewFake(start), hub: events.NewHub(), engines: map[string]*fake.Engine{}}
	ctx, cancel := context.WithCancel(context.Background())
	f.rec = newRecorder(ctx, f.hub)
	reg := engine.NewRegistry()
	reg.Register(model.TaskSync, func(task model.Task, req engine.Request) (engine.Engine, error) {
		return f.engine(task.Name), nil
	})
	r := &runner.Runner{Store: st, Registry: reg, Events: f.hub, Clock: f.clk, DataDir: t.TempDir(), RuntimeDir: t.TempDir()}
	f.s = New(Deps{Store: st, Runner: r, Clock: f.clk, Events: f.hub})
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		f.s.Shutdown(sctx)
		cancel()
		st.Close()
	})
	return f
}

var enginesMu sync.Mutex

// engine returns the engine used for the task called name; by default one
// that succeeds at once.
func (f *fx) engine(name string) *fake.Engine {
	enginesMu.Lock()
	defer enginesMu.Unlock()
	if f.engines[name] == nil {
		f.engines[name] = &fake.Engine{}
	}
	return f.engines[name]
}

func (f *fx) blocked(name string) *fake.Engine {
	e := &fake.Engine{Block: make(chan struct{})}
	enginesMu.Lock()
	f.engines[name] = e
	enginesMu.Unlock()
	return e
}

func (f *fx) start() {
	f.t.Helper()
	if err := f.s.Start(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

// addTask stores an enabled sync task; tasks are listed in creation order.
func (f *fx) addTask(name, schedule string, mutate ...func(*model.Task)) model.Task {
	f.t.Helper()
	f.created++
	created := time.Date(2026, 1, 1, 0, 0, f.created, 0, time.UTC)
	task := model.Task{
		ID: model.NewID(), Version: 1, Type: model.TaskSync, Name: name, Enabled: true, Schedule: schedule,
		Sync:      &model.SyncSpec{Protocol: "sftp", ConnectionID: "c1", Direction: "push", Pairs: []model.FolderPair{{Local: "/a", Remote: "a"}}},
		CreatedAt: created, UpdatedAt: created,
	}
	task.ApplyDefaults()
	for _, m := range mutate {
		m(&task)
	}
	if err := f.st.CreateTask(context.Background(), task, ""); err != nil {
		f.t.Fatal(err)
	}
	return task
}

func (f *fx) runs(taskID string) []model.Run { // oldest first
	f.t.Helper()
	runs, err := f.st.ListRuns(context.Background(), store.RunFilter{TaskID: taskID})
	if err != nil {
		f.t.Fatal(err)
	}
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	return runs
}

func (f *fx) run(id string) model.Run {
	f.t.Helper()
	r, err := f.st.GetRun(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

// tick moves the clock and lets the scheduler act on the new time.
func (f *fx) tick(d time.Duration) {
	f.clk.Advance(d)
	f.s.Sync()
}

func (f *fx) jump(to time.Time) {
	f.clk.Set(to)
	f.s.Sync()
}

func (f *fx) enqueue(task model.Task) string {
	f.t.Helper()
	id, err := f.s.Enqueue(task.ID, engine.Request{}, model.TriggerManual)
	if err != nil {
		f.t.Fatalf("Enqueue: %v", err)
	}
	return id
}

func TestFiresAtCronTime(t *testing.T) {
	f := newFx(t, at(10, 2, 59))
	task := f.addTask("photos", "0 3 * * *")
	f.start()
	if n := len(f.runs(task.ID)); n != 0 {
		t.Fatalf("%d runs before 03:00", n)
	}
	f.tick(time.Minute)
	runs := f.runs(task.ID)
	if len(runs) != 1 || runs[0].Trigger != model.TriggerSchedule || runs[0].Kind != model.RunSync {
		t.Fatalf("runs = %+v", runs)
	}
	if got := f.rec.final(t, runs[0].ID); got.Status != model.StatusSuccess {
		t.Errorf("run = %+v", got)
	}
	f.tick(30 * time.Second)
	f.tick(30 * time.Minute)
	if n := len(f.runs(task.ID)); n != 1 {
		t.Errorf("%d runs after the same scheduled time, want 1", n)
	}
	f.jump(at(11, 3, 0))
	if n := len(f.runs(task.ID)); n != 2 {
		t.Errorf("%d runs at the next day's 03:00, want 2", n)
	}
}

func TestDisabledTaskDoesNotFire(t *testing.T) {
	f := newFx(t, at(10, 2, 59))
	task := f.addTask("photos", "0 3 * * *", func(t *model.Task) { t.Enabled = false })
	f.start()
	f.tick(time.Minute)
	if n := len(f.runs(task.ID)); n != 0 {
		t.Errorf("%d runs of a disabled task", n)
	}
}

func TestOverlapRecordsSkipped(t *testing.T) {
	f := newFx(t, at(10, 2, 59))
	task := f.addTask("photos", "0 * * * *")
	eng := f.blocked("photos")
	f.start()
	f.tick(time.Minute)
	eng.WaitCalls(2) // preflight, unit:0 — the first run is under way
	f.tick(time.Hour)
	runs := f.runs(task.ID)
	if len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
	if s := runs[1]; s.Status != model.StatusSkipped || s.Seq == 0 || s.Trigger != model.TriggerSchedule || s.ErrorSummary == "" {
		t.Errorf("second record = %+v", s)
	}
	close(eng.Block)
	if got := f.rec.final(t, runs[0].ID); got.Status != model.StatusSuccess {
		t.Errorf("first run = %+v", got)
	}
	eng.WaitCalls(3) // preflight, unit:0, close
	if calls := eng.Calls(); len(calls) != 3 {
		t.Errorf("engine was used by the skipped run: %v", calls)
	}
}

func TestConcurrencyOne(t *testing.T) {
	f := newFx(t, at(10, 2, 59))
	a, b := f.addTask("a", "0 3 * * *"), f.addTask("b", "0 3 * * *")
	engA, engB := f.blocked("a"), f.blocked("b")
	f.start()
	f.tick(time.Minute)
	engA.WaitCalls(2)
	runA, runB := f.runs(a.ID)[0], f.runs(b.ID)[0]
	if got := f.run(runB.ID); got.Status != model.StatusQueued || len(engB.Calls()) != 0 {
		t.Fatalf("b = %+v with engine calls %v, want it queued", got, engB.Calls())
	}
	close(engA.Block)
	f.rec.final(t, runA.ID)
	engB.WaitCalls(2)
	close(engB.Block)
	if got := f.rec.final(t, runB.ID); got.Status != model.StatusSuccess {
		t.Errorf("b = %+v", got)
	}
}

func TestConcurrencyTwo(t *testing.T) {
	f := newFx(t, at(10, 2, 59))
	if err := f.st.SetMeta(context.Background(), MetaMaxConcurrent, "2"); err != nil {
		t.Fatal(err)
	}
	f.addTask("a", "0 3 * * *")
	f.addTask("b", "0 3 * * *")
	engA, engB := f.blocked("a"), f.blocked("b")
	f.start()
	f.tick(time.Minute)
	engA.WaitCalls(2) // both reach their unit while the other is still blocked
	engB.WaitCalls(2)
	close(engA.Block)
	close(engB.Block)
}

func TestRetryWaitFreesSlot(t *testing.T) {
	f := newFx(t, at(10, 2, 59))
	a := f.addTask("a", "0 3 * * *")
	f.addTask("b", "0 3 * * *")
	enginesMu.Lock()
	f.engines["a"] = &fake.Engine{Results: map[int][]fake.Outcome{0: {{Err: &model.KindError{Kind: model.KindNetwork, Msg: "down"}}}}}
	enginesMu.Unlock()
	engB := f.blocked("b")
	f.start()
	f.tick(time.Minute)
	runA := f.runs(a.ID)[0]
	f.rec.status(t, runA.ID, model.StatusRetryWait)
	engB.WaitCalls(2) // b got the slot although a is not finished
	if got := f.run(runA.ID); got.Status != model.StatusRetryWait {
		t.Errorf("a = %+v", got)
	}
	if !f.s.Active(a.ID) {
		t.Error("a task waiting for its retry is still active")
	}
}

func setLastTick(t *testing.T, f *fx, at time.Time) {
	t.Helper()
	if err := f.st.SetMeta(context.Background(), metaLastTick, strconv.FormatInt(at.Unix(), 10)); err != nil {
		t.Fatal(err)
	}
}

func TestCatchUpOn(t *testing.T) {
	f := newFx(t, at(10, 9, 0))
	setLastTick(t, f, at(9, 2, 0)) // down since yesterday 02:00: two scheduled times missed
	task := f.addTask("photos", "0 3 * * *", func(t *model.Task) { t.CatchUp = true })
	f.start()
	runs := f.runs(task.ID)
	if len(runs) != 1 || runs[0].Trigger != model.TriggerCatchUp {
		t.Fatalf("runs = %+v, want exactly one catch_up", runs)
	}
	f.rec.final(t, runs[0].ID)
	f.s.Sync()
	if n := len(f.runs(task.ID)); n != 1 {
		t.Errorf("%d runs after another pass", n)
	}
}

func TestCatchUpOff(t *testing.T) {
	f := newFx(t, at(10, 9, 0))
	setLastTick(t, f, at(9, 2, 0))
	task := f.addTask("photos", "0 3 * * *")
	f.start()
	if runs := f.runs(task.ID); len(runs) != 0 {
		t.Fatalf("runs = %+v, want none", runs)
	}
	f.jump(at(11, 3, 0))
	runs := f.runs(task.ID)
	if len(runs) != 1 || runs[0].Trigger != model.TriggerSchedule {
		t.Errorf("runs at the next 03:00 = %+v", runs)
	}
}

func TestFirstStartDoesNotCatchUp(t *testing.T) {
	f := newFx(t, at(10, 9, 0)) // no sched.last_tick at all
	task := f.addTask("photos", "0 3 * * *", func(t *model.Task) { t.CatchUp = true })
	f.start()
	if runs := f.runs(task.ID); len(runs) != 0 {
		t.Errorf("runs = %+v", runs)
	}
}

func TestClockJumpBackNoDuplicate(t *testing.T) {
	f := newFx(t, at(10, 2, 59))
	task := f.addTask("photos", "0 3 * * *")
	f.start()
	f.tick(time.Minute)
	runs := f.runs(task.ID)
	if len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	f.rec.final(t, runs[0].ID)
	f.jump(at(10, 2, 30)) // NTP pulls the clock back
	f.tick(29 * time.Minute)
	f.tick(time.Minute) // 03:00 for the second time
	f.tick(time.Minute)
	if runs := f.runs(task.ID); len(runs) != 1 {
		t.Errorf("the same scheduled time fired again: %+v", runs)
	}
	f.jump(at(11, 3, 0))
	if n := len(f.runs(task.ID)); n != 2 {
		t.Errorf("%d runs after the next day's 03:00, want 2", n)
	}
}

func TestClockJumpForward(t *testing.T) {
	for _, catchUp := range []bool{false, true} {
		f := newFx(t, at(10, 2, 0))
		task := f.addTask("photos", "0 3 * * *", func(t *model.Task) { t.CatchUp = catchUp })
		f.start()
		f.jump(at(11, 9, 0)) // skips 03:00 on the 10th and on the 11th
		runs := f.runs(task.ID)
		switch {
		case !catchUp && len(runs) != 0:
			t.Errorf("catch-up off: runs = %+v", runs)
		case catchUp && (len(runs) != 1 || runs[0].Trigger != model.TriggerCatchUp):
			t.Errorf("catch-up on: runs = %+v, want exactly one catch_up", runs)
		}
	}
}

func TestPaused(t *testing.T) {
	for _, catchUp := range []bool{false, true} {
		f := newFx(t, at(10, 2, 59))
		task := f.addTask("photos", "0 3 * * *", func(t *model.Task) { t.CatchUp = catchUp })
		f.start()
		f.s.SetPaused(true)
		f.tick(time.Minute)
		if runs := f.runs(task.ID); len(runs) != 0 {
			t.Fatalf("fired while paused: %+v", runs)
		}
		if _, err := f.s.Enqueue(task.ID, engine.Request{}, model.TriggerManual); !errors.Is(err, ErrPaused) {
			t.Errorf("Enqueue while paused = %v, want ErrPaused", err)
		}
		f.tick(10 * time.Minute)
		f.s.SetPaused(false)
		f.s.Sync()
		runs := f.runs(task.ID)
		switch {
		case !catchUp && len(runs) != 0:
			t.Errorf("catch-up off: runs after resume = %+v", runs)
		case catchUp && (len(runs) != 1 || runs[0].Trigger != model.TriggerCatchUp):
			t.Errorf("catch-up on: runs after resume = %+v", runs)
		}
	}
}

func TestEnqueueWhileActive(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	task := f.addTask("photos", "")
	eng := f.blocked("photos")
	f.start()
	id := f.enqueue(task)
	if !f.s.Active(task.ID) {
		t.Error("Active = false right after Enqueue")
	}
	if got, ok := f.s.ActiveRun(task.ID); !ok || got != id {
		t.Errorf("ActiveRun = %q, %v", got, ok)
	}
	if _, err := f.s.Enqueue(task.ID, engine.Request{}, model.TriggerManual); !errors.Is(err, ErrActive) {
		t.Errorf("second Enqueue = %v, want ErrActive", err)
	}
	close(eng.Block)
	if got := f.rec.final(t, id); got.Trigger != model.TriggerManual || got.Status != model.StatusSuccess {
		t.Errorf("run = %+v", got)
	}
	// By the time the final event is out, the task is free again.
	if f.s.Active(task.ID) {
		t.Error("still active after the final event")
	}
	f.rec.final(t, f.enqueue(task))
}

func TestEnqueueUnknownTask(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	f.start()
	if _, err := f.s.Enqueue("nope", engine.Request{}, model.TriggerManual); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestEnqueueUsesSnapshot(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	task := f.addTask("photos", "")
	eng := f.blocked("photos")
	f.start()
	id := f.enqueue(task)
	eng.WaitCalls(2)
	// Edit the task mid-run: a new name, and a hook that would leave a mark.
	task.Name, task.Enabled = "renamed", false
	if err := f.st.UpdateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	f.s.Reload()
	close(eng.Block)
	got := f.rec.final(t, id)
	if got.TaskName != "photos" || got.Status != model.StatusSuccess {
		t.Errorf("run = %+v, want it to finish under the old name", got)
	}
	if e := f.engines["renamed"]; e != nil {
		t.Error("the engine was built from the edited task")
	}
}

func TestCancelQueued(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	a, b := f.addTask("a", ""), f.addTask("b", "")
	engA, engB := f.blocked("a"), f.blocked("b")
	f.start()
	f.enqueue(a)
	engA.WaitCalls(2)
	idB := f.enqueue(b)
	if err := f.s.Cancel(idB); err != nil {
		t.Fatal(err)
	}
	got := f.rec.final(t, idB)
	if got.Status != model.StatusCanceled || got.Seq == 0 || len(engB.Calls()) != 0 {
		t.Errorf("run = %+v, engine calls %v", got, engB.Calls())
	}
	if f.s.Active(b.ID) {
		t.Error("b still active")
	}
	close(engA.Block)
}

func TestCancelRunning(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	task := f.addTask("photos", "")
	eng := f.blocked("photos")
	f.start()
	id := f.enqueue(task)
	eng.WaitCalls(2)
	if err := f.s.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if got := f.rec.final(t, id); got.Status != model.StatusCanceled {
		t.Errorf("run = %+v", got)
	}
	if err := f.s.Cancel(id); !errors.Is(err, ErrNotActive) {
		t.Errorf("Cancel of a finished run = %v, want ErrNotActive", err)
	}
}

func TestStartInterruptsLeftovers(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	left := model.Run{ID: model.NewID(), TaskID: "gone", TaskName: "gone", Kind: model.RunSync, Trigger: model.TriggerSchedule,
		Status: model.StatusQueued, StartedAt: at(9, 3, 0)}
	if err := f.st.CreateRun(context.Background(), left); err != nil {
		t.Fatal(err)
	}
	f.st.UpdateRun(context.Background(), left.ID, model.StatusRunning, 1)
	f.start()
	if got := f.run(left.ID); got.Status != model.StatusInterrupted || got.Seq == 0 {
		t.Errorf("leftover = %+v", got)
	}
}

func TestShutdownWaits(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	a, b := f.addTask("a", ""), f.addTask("b", "")
	engA := f.blocked("a")
	f.blocked("b")
	f.start()
	idA := f.enqueue(a)
	engA.WaitCalls(2)
	idB := f.enqueue(b) // stays queued behind a
	f.s.Shutdown(context.Background())
	for _, id := range []string{idA, idB} {
		if got := f.run(id); got.Status != model.StatusInterrupted {
			t.Errorf("after Shutdown: %+v", got)
		}
	}
	if _, err := f.s.Enqueue(a.ID, engine.Request{}, model.TriggerManual); err == nil {
		t.Error("Enqueue after Shutdown succeeded")
	}
}

func TestReloadPicksUpNewTask(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	f.start()
	task := f.addTask("photos", "0 3 * * *")
	f.s.Reload()
	f.tick(time.Hour)
	if runs := f.runs(task.ID); len(runs) != 1 {
		t.Errorf("runs = %+v", runs)
	}
}

func TestLoopWakesItselfAtTheScheduledTime(t *testing.T) {
	f := newFx(t, at(10, 2, 59))
	task := f.addTask("photos", "0 3 * * *")
	f.start()
	f.clk.BlockUntil(1)        // the loop is asleep until 03:00
	f.clk.Advance(time.Minute) // no Sync: the loop has to notice by itself
	got := f.rec.wait(t, "the scheduled run", func(r model.Run) bool { return r.TaskID == task.ID && r.Status.Terminal() })
	if got.Trigger != model.TriggerSchedule {
		t.Errorf("run = %+v", got)
	}
}

func TestLastTickPersisted(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	f.start()
	f.tick(90 * time.Second)
	raw, ok, err := f.st.GetMeta(context.Background(), metaLastTick)
	if err != nil || !ok || raw != strconv.FormatInt(f.clk.Now().Unix(), 10) {
		t.Errorf("sched.last_tick = %q, %v, %v; want %d", raw, ok, err, f.clk.Now().Unix())
	}
}

func TestBrokenScheduleDoesNotStall(t *testing.T) {
	f := newFx(t, at(10, 2, 59))
	// Stored before validation existed, or edited by hand: never fires.
	f.addTask("broken", "0 0 31 2 *")
	good := f.addTask("photos", "0 3 * * *")
	f.start()
	f.tick(time.Minute)
	if runs := f.runs(good.ID); len(runs) != 1 {
		t.Errorf("runs = %+v", runs)
	}
}
