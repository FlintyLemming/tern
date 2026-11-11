package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tern/internal/clock"
	"tern/internal/engine"
	"tern/internal/engine/fake"
	"tern/internal/events"
	"tern/internal/model"
	"tern/internal/proc"
	"tern/internal/store"
)

func init() { proc.KillGrace = 200 * time.Millisecond }

var t0 = time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)

func kindErr(k model.ErrKind) error { return &model.KindError{Kind: k, Msg: string(k) + " failure"} }

var (
	netFail  = fake.Outcome{Err: kindErr(model.KindNetwork)}
	confFail = fake.Outcome{Err: kindErr(model.KindConfig)}
	ok       = fake.Outcome{Status: model.StatusSuccess, Bytes: 100, Files: 2}
)

// recClock records every After duration requested of the fake clock.
type recClock struct {
	*clock.Fake
	mu    sync.Mutex
	waits []time.Duration
}

func (c *recClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	return c.Fake.After(d)
}

func (c *recClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// countSlot records how the runner uses its execution slot.
type countSlot struct {
	mu     sync.Mutex
	events []string
}

func (s *countSlot) Release() { s.mu.Lock(); s.events = append(s.events, "release"); s.mu.Unlock() }
func (s *countSlot) Acquire(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "acquire")
	return ctx.Err()
}
func (s *countSlot) log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

type fx struct {
	t       *testing.T
	st      *store.Store
	clk     *recClock
	hub     *events.Hub
	r       *Runner
	eng     *fake.Engine
	slot    *countSlot
	hookLog string // file the hook scripts append to
	task    model.Task
}

func newFx(t *testing.T, eng *fake.Engine) *fx {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fx{t: t, st: st, clk: &recClock{Fake: clock.NewFake(t0)}, hub: events.NewHub(), eng: eng, slot: &countSlot{},
		hookLog: filepath.Join(t.TempDir(), "hooks.txt")}
	reg := engine.NewRegistry()
	reg.Register(model.TaskSync, fake.Builder(eng))
	f.r = &Runner{Store: st, Registry: reg, Events: f.hub, Clock: f.clk, DataDir: t.TempDir(), RuntimeDir: t.TempDir()}
	f.task = model.Task{
		ID: "task1", Version: 1, Type: model.TaskSync, Name: "photos", Enabled: true,
		Sync: &model.SyncSpec{Protocol: "sftp", ConnectionID: "c1", Direction: "push", Pairs: []model.FolderPair{{Local: "/a", Remote: "a"}}},
	}
	f.task.ApplyDefaults()
	return f
}

// withHooks gives the task a pre and a post command that leave a trace.
func (f *fx) withHooks() {
	f.task.Pre.Script = `echo "pre" >> ` + f.hookLog
	f.task.Post.Script = `echo "post result=$TERN_RESULT error=$TERN_ERROR" >> ` + f.hookLog
}

func (f *fx) hookLines() []string {
	raw, err := os.ReadFile(f.hookLog)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func (f *fx) job(kind model.RunKind) Job {
	f.t.Helper()
	run := model.Run{ID: model.NewID(), TaskID: f.task.ID, TaskName: f.task.Name, Kind: kind,
		Trigger: model.TriggerManual, Status: model.StatusQueued, StartedAt: f.clk.Now()}
	if err := f.st.CreateRun(context.Background(), run); err != nil {
		f.t.Fatal(err)
	}
	return Job{RunID: run.ID, Task: f.task, Req: engine.Request{Kind: kind}, Trigger: model.TriggerManual}
}

// exec runs a sync job to completion on the calling goroutine.
func (f *fx) exec() model.Run {
	f.t.Helper()
	return f.r.Execute(context.Background(), f.job(model.RunSync), f.slot)
}

// start runs a sync job in the background.
func (f *fx) start(ctx context.Context) <-chan model.Run {
	f.t.Helper()
	job := f.job(model.RunSync)
	ch := make(chan model.Run, 1)
	go func() { ch <- f.r.Execute(ctx, job, f.slot) }()
	return ch
}

func (f *fx) wait(ch <-chan model.Run) model.Run {
	f.t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		f.t.Fatal("run did not finish")
	}
	panic("unreachable")
}

// stored returns the run as the database has it and checks that it matches
// what Execute returned.
func (f *fx) stored(run model.Run) model.Run {
	f.t.Helper()
	got, err := f.st.GetRun(context.Background(), run.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if !reflect.DeepEqual(got, run) {
		f.t.Errorf("stored run differs from returned run:\n%+v\n%+v", got, run)
	}
	return got
}

func unitCalls(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "unit:") {
			out = append(out, c)
		}
	}
	return out
}

func count(calls []string, what string) int {
	n := 0
	for _, c := range calls {
		if c == what {
			n++
		}
	}
	return n
}

func TestHappyPath(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {ok}}})
	run := f.stored(f.exec())
	if run.Status != model.StatusSuccess || run.Attempts != 1 || run.Seq <= 0 || run.Bytes != 100 || run.Files != 2 {
		t.Errorf("run = %+v", run)
	}
	if !run.FinishedAt.Equal(t0) || run.ErrorKind != "" || run.ErrorSummary != "" {
		t.Errorf("run = %+v", run)
	}
	if len(run.Units) != 1 || run.Units[0].Status != model.StatusSuccess || run.Units[0].Name != "unit" {
		t.Errorf("units = %+v", run.Units)
	}
	if log, _ := os.ReadFile(filepath.Join(f.r.DataDir, "logs", run.ID+".log")); len(log) == 0 {
		t.Error("the run has no log")
	}
}

func TestRetriesNetworkThenOK(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {netFail, netFail, ok}}})
	ch := f.start(context.Background())
	f.clk.BlockUntil(1)
	if got, _ := f.st.GetRun(context.Background(), f.lastRunID()); got.Status != model.StatusRetryWait || got.Attempts != 1 {
		t.Errorf("while waiting: %+v", got)
	}
	f.clk.Advance(60 * time.Second)
	f.clk.BlockUntil(1)
	f.clk.Advance(300 * time.Second)
	run := f.stored(f.wait(ch))
	if run.Status != model.StatusSuccess || run.Attempts != 3 || run.ErrorSummary != "" {
		t.Errorf("run = %+v", run)
	}
	if got := f.clk.Waits(); !reflect.DeepEqual(got, []time.Duration{60 * time.Second, 300 * time.Second}) {
		t.Errorf("waits = %v", got)
	}
}

func (f *fx) lastRunID() string {
	runs, err := f.st.ListRuns(context.Background(), store.RunFilter{Limit: 1})
	if err != nil || len(runs) == 0 {
		f.t.Fatalf("no runs: %v", err)
	}
	return runs[0].ID
}

func TestSlotReleasedWhileWaiting(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {netFail, ok}}})
	ch := f.start(context.Background())
	f.clk.BlockUntil(1)
	if got := f.slot.log(); !reflect.DeepEqual(got, []string{"release"}) {
		t.Errorf("slot use while waiting = %v, want one release", got)
	}
	f.clk.Advance(60 * time.Second)
	f.wait(ch)
	if got := f.slot.log(); !reflect.DeepEqual(got, []string{"release", "acquire"}) {
		t.Errorf("slot use = %v", got)
	}
}

func TestNoRetryOnNonRetryable(t *testing.T) {
	for _, k := range []model.ErrKind{model.KindAuth, model.KindHostKey, model.KindConfig, model.KindData, model.KindGuard} {
		f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {{Err: kindErr(k)}}}})
		run := f.stored(f.exec())
		if run.Status != model.StatusFailed || run.Attempts != 1 || run.ErrorKind != k || run.ErrorSummary == "" {
			t.Errorf("%s: run = %+v", k, run)
		}
	}
}

func TestRetryExhausted(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {netFail}}})
	ch := f.start(context.Background())
	for _, d := range []time.Duration{60, 300, 900} {
		f.clk.BlockUntil(1)
		f.clk.Advance(d * time.Second)
	}
	run := f.stored(f.wait(ch))
	if run.Status != model.StatusFailed || run.Attempts != 4 || run.ErrorKind != model.KindNetwork {
		t.Errorf("run = %+v", run)
	}
}

func TestUnknownIsRetried(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {{Err: errors.New("mystery")}, ok}}})
	ch := f.start(context.Background())
	f.clk.BlockUntil(1)
	f.clk.Advance(60 * time.Second)
	if run := f.stored(f.wait(ch)); run.Status != model.StatusSuccess || run.Attempts != 2 {
		t.Errorf("run = %+v", run)
	}
}

func twoUnits(results map[int][]fake.Outcome) *fake.Engine {
	return &fake.Engine{UnitNames: []string{"/a", "/b"}, Results: results}
}

func TestOnlyFailedUnitsRerun(t *testing.T) {
	f := newFx(t, twoUnits(map[int][]fake.Outcome{0: {ok}, 1: {netFail, ok}}))
	ch := f.start(context.Background())
	f.clk.BlockUntil(1)
	f.clk.Advance(60 * time.Second)
	run := f.stored(f.wait(ch))
	if got := unitCalls(f.eng.Calls()); !reflect.DeepEqual(got, []string{"unit:0", "unit:1", "unit:1"}) {
		t.Errorf("unit calls = %v", got)
	}
	if run.Status != model.StatusSuccess || run.Bytes != 200 || run.Files != 4 {
		t.Errorf("run = %+v", run)
	}
}

func TestPartial(t *testing.T) {
	f := newFx(t, twoUnits(map[int][]fake.Outcome{0: {ok}, 1: {confFail}}))
	run := f.stored(f.exec())
	if run.Status != model.StatusPartial || run.ErrorKind != model.KindConfig {
		t.Errorf("run = %+v", run)
	}
	if u := run.Units; len(u) != 2 || u[0].Status != model.StatusSuccess || u[1].Status != model.StatusFailed || u[1].Error == "" || u[1].Name != "/b" {
		t.Errorf("units = %+v", u)
	}
}

func TestLaterUnitsRunAfterFailure(t *testing.T) {
	f := newFx(t, twoUnits(map[int][]fake.Outcome{0: {confFail}, 1: {ok}}))
	run := f.exec()
	if got := unitCalls(f.eng.Calls()); !reflect.DeepEqual(got, []string{"unit:0", "unit:1"}) {
		t.Errorf("unit calls = %v", got)
	}
	if run.Status != model.StatusPartial {
		t.Errorf("status = %s", run.Status)
	}
}

func TestPreflightFailureRetries(t *testing.T) {
	f := newFx(t, &fake.Engine{Preflights: []error{kindErr(model.KindNetwork)}})
	ch := f.start(context.Background())
	f.clk.BlockUntil(1)
	if got := f.eng.Calls(); !reflect.DeepEqual(got, []string{"preflight", "cleanup"}) {
		t.Errorf("calls after the failed preflight = %v", got)
	}
	f.clk.Advance(60 * time.Second)
	run := f.stored(f.wait(ch))
	if run.Status != model.StatusSuccess || run.Attempts != 2 {
		t.Errorf("run = %+v", run)
	}
}

func TestPreflightFailureNotRetryable(t *testing.T) {
	f := newFx(t, twoUnits(nil))
	f.eng.Preflights = []error{kindErr(model.KindAuth)}
	run := f.stored(f.exec())
	if run.Status != model.StatusFailed || run.ErrorKind != model.KindAuth || len(unitCalls(f.eng.Calls())) != 0 {
		t.Errorf("run = %+v, calls %v", run, f.eng.Calls())
	}
	for _, u := range run.Units {
		if u.Status != model.StatusFailed || u.Error == "" {
			t.Errorf("unit = %+v", u)
		}
	}
}

func TestCleanupBeforeRetry(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {netFail, netFail, ok}}})
	ch := f.start(context.Background())
	f.clk.BlockUntil(1)
	f.clk.Advance(60 * time.Second)
	f.clk.BlockUntil(1)
	f.clk.Advance(300 * time.Second)
	f.wait(ch)
	want := []string{"preflight", "unit:0", "cleanup", "preflight", "unit:0", "cleanup", "preflight", "unit:0", "close"}
	if got := f.eng.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v\nwant    %v", got, want)
	}
}

func TestHooksOncePerRun(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {netFail, netFail, ok}}})
	f.withHooks()
	ch := f.start(context.Background())
	f.clk.BlockUntil(1)
	f.clk.Advance(60 * time.Second)
	f.clk.BlockUntil(1)
	f.clk.Advance(300 * time.Second)
	f.wait(ch)
	if got := f.hookLines(); !reflect.DeepEqual(got, []string{"pre", "post result=success error="}) {
		t.Errorf("hook trace = %q", got)
	}
}

func TestPreFailSkipsEngine(t *testing.T) {
	f := newFx(t, &fake.Engine{})
	f.withHooks()
	f.task.Pre.Script = "exit 1"
	run := f.stored(f.exec())
	if run.Status != model.StatusFailed || !strings.HasPrefix(run.ErrorSummary, "pre-task command failed") {
		t.Errorf("run = %+v", run)
	}
	if calls := f.eng.Calls(); count(calls, "preflight") != 0 || len(unitCalls(calls)) != 0 {
		t.Errorf("engine was used: %v", calls)
	}
	if got := f.hookLines(); len(got) != 1 || !strings.HasPrefix(got[0], "post result=failed error=pre-task command failed") {
		t.Errorf("hook trace = %q", got)
	}
}

func TestPreFailIgnored(t *testing.T) {
	f := newFx(t, &fake.Engine{})
	f.task.Pre.Script, f.task.Pre.IgnoreFailure = "exit 1", true
	run := f.exec()
	if run.Status != model.StatusSuccess || len(unitCalls(f.eng.Calls())) != 1 {
		t.Errorf("run = %+v, calls %v", run, f.eng.Calls())
	}
}

func TestPostFailIsWarning(t *testing.T) {
	f := newFx(t, &fake.Engine{})
	f.task.Post.Script = "exit 1"
	if run := f.stored(f.exec()); run.Status != model.StatusWarning {
		t.Errorf("run = %+v", run)
	}
}

func TestPostFailKeepsFailure(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {confFail}}})
	f.task.Post.Script = "exit 1"
	if run := f.exec(); run.Status != model.StatusFailed {
		t.Errorf("status = %s, want failed", run.Status)
	}
}

func TestPostSeesResult(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {confFail}}})
	f.withHooks()
	f.exec()
	if got := f.hookLines(); len(got) != 2 || got[1] != "post result=failed error=config failure" {
		t.Errorf("hook trace = %q", got)
	}
}

func TestCancelRunsPost(t *testing.T) {
	eng := &fake.Engine{Block: make(chan struct{})}
	f := newFx(t, eng)
	f.withHooks()
	ctx, cancel := context.WithCancelCause(context.Background())
	ch := f.start(ctx)
	eng.WaitCalls(2) // preflight, unit:0 — now blocked
	cancel(ErrCanceled)
	run := f.stored(f.wait(ch))
	if run.Status != model.StatusCanceled || run.ErrorSummary == "" {
		t.Errorf("run = %+v", run)
	}
	if got := f.hookLines(); len(got) != 2 || !strings.HasPrefix(got[1], "post result=canceled") {
		t.Errorf("hook trace = %q", got)
	}
}

func TestCancelDuringRetryWait(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {netFail}}})
	ctx, cancel := context.WithCancelCause(context.Background())
	ch := f.start(ctx)
	f.clk.BlockUntil(1)
	cancel(ErrCanceled)
	if run := f.stored(f.wait(ch)); run.Status != model.StatusCanceled || run.Attempts != 1 {
		t.Errorf("run = %+v", run)
	}
}

func TestShutdownIsInterrupted(t *testing.T) {
	eng := &fake.Engine{Block: make(chan struct{})}
	f := newFx(t, eng)
	ctx, cancel := context.WithCancelCause(context.Background())
	ch := f.start(ctx)
	eng.WaitCalls(2)
	cancel(ErrShutdown)
	if run := f.stored(f.wait(ch)); run.Status != model.StatusInterrupted {
		t.Errorf("run = %+v", run)
	}
}

func TestMaxRuntime(t *testing.T) {
	eng := &fake.Engine{Block: make(chan struct{})}
	f := newFx(t, eng)
	f.withHooks()
	f.task.MaxRuntimeSec = 60
	ch := f.start(context.Background())
	eng.WaitCalls(2)
	f.clk.BlockUntil(1)
	f.clk.Advance(61 * time.Second)
	run := f.stored(f.wait(ch))
	if run.Status != model.StatusFailed || !strings.Contains(run.ErrorSummary, "max runtime exceeded") {
		t.Errorf("run = %+v", run)
	}
	if got := f.hookLines(); len(got) != 2 || !strings.HasPrefix(got[1], "post result=failed") {
		t.Errorf("hook trace = %q", got)
	}
}

func TestMaxRuntimeCoversRetryWait(t *testing.T) {
	f := newFx(t, &fake.Engine{Results: map[int][]fake.Outcome{0: {netFail}}})
	f.task.MaxRuntimeSec = 90
	ch := f.start(context.Background())
	f.clk.BlockUntil(2) // the runtime limit and the 60s retry wait
	f.clk.Advance(60 * time.Second)
	f.clk.BlockUntil(2) // the runtime limit and the 300s retry wait
	f.clk.Advance(30 * time.Second)
	run := f.stored(f.wait(ch))
	if run.Status != model.StatusFailed || !strings.Contains(run.ErrorSummary, "max runtime exceeded") || run.Attempts != 2 {
		t.Errorf("run = %+v", run)
	}
}

func TestNoHooksForOtherKinds(t *testing.T) {
	for _, kind := range []model.RunKind{model.RunDryRun, model.RunRestore, model.RunPrune, model.RunCheck} {
		f := newFx(t, &fake.Engine{})
		f.withHooks()
		run := f.r.Execute(context.Background(), f.job(kind), f.slot)
		if run.Status != model.StatusSuccess || run.Kind != kind {
			t.Errorf("%s: run = %+v", kind, run)
		}
		if got := f.hookLines(); got != nil {
			t.Errorf("%s: hooks ran: %q", kind, got)
		}
	}
}

func TestBuildFailure(t *testing.T) {
	f := newFx(t, &fake.Engine{})
	f.withHooks()
	f.task.Type = model.TaskBackup // nothing registered for it
	run := f.stored(f.exec())
	if run.Status != model.StatusFailed || run.ErrorKind != model.KindConfig || run.ErrorSummary == "" {
		t.Errorf("run = %+v", run)
	}
	if got := f.hookLines(); got != nil {
		t.Errorf("hooks ran: %q", got)
	}
}

func (f *fx) runtimeLeftovers() []string {
	entries, _ := os.ReadDir(f.r.RuntimeDir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// closeSpy notes whether the run directory still existed when the engine
// was closed.
type closeSpy struct {
	*fake.Engine
	mu        sync.Mutex
	dir       string
	closes    int
	dirExists bool
}

func (c *closeSpy) Preflight(ctx context.Context, env engine.Env) error {
	c.mu.Lock()
	c.dir = env.Dir.Path
	c.mu.Unlock()
	return c.Engine.Preflight(ctx, env)
}

func (c *closeSpy) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	_, err := os.Stat(c.dir)
	c.dirExists = err == nil
	return nil
}

func TestRunDirRemovedAndEngineClosed(t *testing.T) {
	cases := map[string]struct {
		eng    *fake.Engine
		cancel bool
		want   model.Status
	}{
		"success":  {&fake.Engine{}, false, model.StatusSuccess},
		"failure":  {&fake.Engine{Results: map[int][]fake.Outcome{0: {confFail}}}, false, model.StatusFailed},
		"canceled": {&fake.Engine{Block: make(chan struct{})}, true, model.StatusCanceled},
	}
	for name, c := range cases {
		f := newFx(t, c.eng)
		spy := &closeSpy{Engine: c.eng}
		f.r.Registry.Register(model.TaskSync, func(model.Task, engine.Request) (engine.Engine, error) { return spy, nil })
		ctx, cancel := context.WithCancelCause(context.Background())
		ch := f.start(ctx)
		if c.cancel {
			c.eng.WaitCalls(2)
			cancel(ErrCanceled)
		}
		run := f.wait(ch)
		cancel(nil)
		if run.Status != c.want {
			t.Errorf("%s: status = %s", name, run.Status)
		}
		if left := f.runtimeLeftovers(); len(left) != 0 {
			t.Errorf("%s: runtime directory still has %v", name, left)
		}
		if spy.closes != 1 || !spy.dirExists {
			t.Errorf("%s: engine closed %d times, run dir present at close: %v", name, spy.closes, spy.dirExists)
		}
	}
}

func TestFinalizeFailureStillCompletes(t *testing.T) {
	eng := &fake.Engine{Block: make(chan struct{})}
	f := newFx(t, eng)
	f.withHooks()
	ch := f.start(context.Background())
	eng.WaitCalls(2)
	f.st.Close() // every write from here on fails, as with a full /data
	close(eng.Block)
	run := f.wait(ch)
	if run.Status != model.StatusSuccess || run.Seq != 0 {
		t.Errorf("run = %+v, want the in-memory result without a seq", run)
	}
	if got := f.hookLines(); len(got) != 2 || got[1] != "post result=success error=" {
		t.Errorf("hook trace = %q", got)
	}
	if left := f.runtimeLeftovers(); len(left) != 0 {
		t.Errorf("runtime directory still has %v", left)
	}
}

func TestFinalStatus(t *testing.T) {
	u := func(sts ...model.Status) []model.UnitResult {
		var out []model.UnitResult
		for _, s := range sts {
			out = append(out, model.UnitResult{Status: s})
		}
		return out
	}
	S, W, F := model.StatusSuccess, model.StatusWarning, model.StatusFailed
	cases := []struct {
		units []model.UnitResult
		warn  bool
		want  model.Status
	}{
		{nil, false, F},
		{u(F, F), false, F},
		{u(F), true, F},
		{u(S, F), false, model.StatusPartial},
		{u(W, F), true, model.StatusPartial},
		{u(S, W), false, W},
		{u(S, S), true, W},
		{u(S, S), false, S},
	}
	for _, c := range cases {
		if got := FinalStatus(c.units, c.warn); got != c.want {
			t.Errorf("FinalStatus(%v, %v) = %s, want %s", c.units, c.warn, got, c.want)
		}
	}
}

func TestEventsAndProgressThrottled(t *testing.T) {
	f := newFx(t, &fake.Engine{ProgressCalls: 1000})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := f.hub.Subscribe(ctx)
	run := f.exec()

	var statuses []model.Status
	progress := 0
	for done := false; !done; {
		select {
		case e := <-sub:
			switch e.Type {
			case events.RunProgress:
				progress++
				if p := e.Data.(events.Progress); p.RunID != run.ID {
					t.Errorf("progress for run %q", p.RunID)
				}
			case events.RunUpdated:
				r := e.Data.(model.Run)
				statuses = append(statuses, r.Status)
				done = r.Status.Terminal()
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no terminal run.updated event")
		}
	}
	if progress < 1 || progress > 2 {
		t.Errorf("%d run.progress events for 1000 reports within one second, want 1 or 2", progress)
	}
	if !reflect.DeepEqual(statuses, []model.Status{model.StatusRunning, model.StatusSuccess}) {
		t.Errorf("run.updated statuses = %v", statuses)
	}
	if _, ok := f.r.Progress(run.ID); ok {
		t.Error("progress of a finished run is still remembered")
	}
}

func TestFinishedCalledBeforeFinalEvent(t *testing.T) {
	f := newFx(t, &fake.Engine{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := f.hub.Subscribe(ctx)
	job := f.job(model.RunSync)
	var finalStatus model.Status
	job.Finished = func() {
		got, _ := f.st.GetRun(context.Background(), job.RunID)
		finalStatus = got.Status
	}
	f.r.Execute(context.Background(), job, f.slot)
	if finalStatus != model.StatusSuccess {
		t.Errorf("at Finished the stored status was %q, want success", finalStatus)
	}
	<-sub // running
	select {
	case e := <-sub:
		if r := e.Data.(model.Run); r.Status != model.StatusSuccess {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no final event")
	}
}

// Review focus 4: on shutdown the post-task command still runs, but only for
// ShutdownHookCap, so the process finishes within docker's grace period.
func TestShutdownCapsPostHook(t *testing.T) {
	old := ShutdownHookCap
	ShutdownHookCap = time.Second
	t.Cleanup(func() { ShutdownHookCap = old })

	eng := &fake.Engine{Block: make(chan struct{})}
	f := newFx(t, eng)
	f.task.Post.Script = `echo "post started" >> ` + f.hookLog + `; sleep 600`
	f.task.Post.TimeoutSec = 600
	ctx, cancel := context.WithCancelCause(context.Background())
	ch := f.start(ctx)
	eng.WaitCalls(2) // preflight, unit:0 — now blocked
	began := time.Now()
	cancel(ErrShutdown)
	run := f.stored(f.wait(ch))
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Execute returned %s after the shutdown, want about %s", took, ShutdownHookCap)
	}
	if run.Status != model.StatusInterrupted {
		t.Errorf("run = %+v", run)
	}
	if got := f.hookLines(); len(got) != 1 || got[0] != "post started" {
		t.Errorf("the post-task command did not run: %q", got)
	}
}
