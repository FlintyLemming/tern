package synccore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"tern/internal/engine"
	"tern/internal/model"
)

// fakePlanner records every call and answers as configured.
type fakePlanner struct {
	calls []string

	prepareErr  error
	state       SourceState
	stateErr    error
	plan        Plan
	planErr     error
	transferRes model.UnitResult
	transferErr error
	maxDeletes  []int // what Transfer received, in order
	closed      int
}

func (f *fakePlanner) Prepare(ctx context.Context, env engine.Env) error {
	f.calls = append(f.calls, "prepare")
	return f.prepareErr
}

func (f *fakePlanner) RemoteState(ctx context.Context, env engine.Env, p model.FolderPair) (SourceState, error) {
	f.calls = append(f.calls, "remote-state "+p.Remote)
	return f.state, f.stateErr
}

func (f *fakePlanner) Plan(ctx context.Context, env engine.Env, p model.FolderPair) (Plan, error) {
	f.calls = append(f.calls, "plan "+p.Local)
	return f.plan, f.planErr
}

func (f *fakePlanner) Transfer(ctx context.Context, env engine.Env, p model.FolderPair, maxDelete int) (model.UnitResult, error) {
	f.calls = append(f.calls, fmt.Sprintf("transfer %s %d", p.Local, maxDelete))
	f.maxDeletes = append(f.maxDeletes, maxDelete)
	res := f.transferRes
	if res.Status == "" {
		res.Status = model.StatusSuccess
	}
	return res, f.transferErr
}

func (f *fakePlanner) Describe(p model.FolderPair) string { return "tool " + p.Local + " " + p.Remote }

func (f *fakePlanner) Close() error { f.closed++; return nil }

func (f *fakePlanner) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func syncTask(direction, mode string, pairs ...model.FolderPair) model.Task {
	return model.Task{ID: "t1", Type: model.TaskSync, Name: "photos", Sync: &model.SyncSpec{
		Protocol: "sftp", ConnectionID: "c1", Direction: direction, Pairs: pairs,
		DeleteMode: mode, MaxDeletes: new(100), Compare: "size_mtime", Transfers: 4,
	}}
}

type fixture struct {
	t       *testing.T
	p       *fakePlanner
	log     *bytes.Buffer
	env     engine.Env
	mounted bool
	checked int
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, p: &fakePlanner{state: SourceOK}, log: &bytes.Buffer{}, mounted: true}
	f.env = engine.Env{Log: f.log}
	return f
}

func (f *fixture) engine(task model.Task, req engine.Request) *Engine {
	check := func(context.Context) error { f.checked++; return nil }
	mounted := func(string) (bool, error) { return f.mounted, nil }
	return New(task, req, f.p, check, mounted)
}

// run builds an engine for one pair and runs its only unit.
func (f *fixture) run(direction, mode string, pair model.FolderPair, req engine.Request) (model.UnitResult, error) {
	f.t.Helper()
	if req.Kind == "" {
		req.Kind = model.RunSync
	}
	e := f.engine(syncTask(direction, mode, pair), req)
	if err := e.Preflight(context.Background(), f.env); err != nil {
		f.t.Fatalf("Preflight: %v", err)
	}
	return e.RunUnit(context.Background(), f.env, 0)
}

// dirWith returns a new directory holding the given file names.
func dirWith(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func wantKind(t *testing.T, err error, kind model.ErrKind) *model.KindError {
	t.Helper()
	var ke *model.KindError
	if !errors.As(err, &ke) || ke.Kind != kind {
		t.Fatalf("error = %v (kind %q), want kind %q", err, model.KindOf(err), kind)
	}
	return ke
}

func TestUnitNames(t *testing.T) {
	f := newFixture(t)
	push := f.engine(syncTask("push", "keep", model.FolderPair{Local: "/sources/a", Remote: "/srv/a"},
		model.FolderPair{Local: "/sources/b", Remote: "/srv/b"}), engine.Request{Kind: model.RunSync})
	if got, want := push.Units(), []string{"/sources/a → /srv/a", "/sources/b → /srv/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("push units = %q, want %q", got, want)
	}
	pull := f.engine(syncTask("pull", "keep", model.FolderPair{Local: "/targets/a", Remote: "/srv/a"}), engine.Request{Kind: model.RunSync})
	if got, want := pull.Units(), []string{"/srv/a → /targets/a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pull units = %q, want %q", got, want)
	}
}

func TestPreflightOrder(t *testing.T) {
	f := newFixture(t)
	var order []string
	task := syncTask("push", "keep", model.FolderPair{Local: "/sources/a", Remote: "/srv/a"})
	boom := &model.KindError{Kind: model.KindNetwork, Msg: "cannot reach nas2"}
	failing := false
	check := func(context.Context) error {
		order = append(order, "check")
		if failing {
			return boom
		}
		return nil
	}
	e := New(task, engine.Request{Kind: model.RunSync}, f.p, check, func(string) (bool, error) { return true, nil })
	if err := e.Preflight(context.Background(), f.env); err != nil {
		t.Fatal(err)
	}
	order = append(order, f.p.calls...)
	if want := []string{"check", "prepare"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %q, want %q", order, want)
	}

	failing, f.p.calls = true, nil
	if err := e.Preflight(context.Background(), f.env); err != boom {
		t.Errorf("Preflight = %v, want the check's error unchanged", err)
	}
	if f.p.count("prepare") != 0 {
		t.Error("Prepare was called after a failed check")
	}
}

func TestKeepPush(t *testing.T) {
	f := newFixture(t)
	local := dirWith(t, "a.txt")
	res, err := f.run("push", "keep", model.FolderPair{Local: local, Remote: "/srv/a"}, engine.Request{})
	if err != nil || res.Status != model.StatusSuccess {
		t.Fatalf("RunUnit = %+v, %v", res, err)
	}
	if !reflect.DeepEqual(f.p.maxDeletes, []int{-1}) {
		t.Errorf("Transfer maxDelete = %v, want [-1]", f.p.maxDeletes)
	}
	if n := f.p.count("remote-state") + f.p.count("plan"); n != 0 {
		t.Errorf("calls = %q: RemoteState or Plan was called for keep", f.p.calls)
	}
}

func TestMirrorAbortsOnEmptyLocal(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("push", "mirror", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"}, engine.Request{})
	ke := wantKind(t, err, model.KindGuard)
	if ke.Msg != "source is missing or empty; mirror aborted, destination untouched" {
		t.Errorf("message = %q", ke.Msg)
	}
	if n := f.p.count("plan") + f.p.count("transfer"); n != 0 {
		t.Errorf("calls = %q", f.p.calls)
	}
}

func TestMirrorAbortsOnMissingLocal(t *testing.T) {
	f := newFixture(t)
	missing := filepath.Join(t.TempDir(), "gone")
	_, err := f.run("push", "mirror", model.FolderPair{Local: missing, Remote: "/srv/a"}, engine.Request{})
	ke := wantKind(t, err, model.KindConfig)
	if ke.Msg != "local source missing: "+missing {
		t.Errorf("message = %q", ke.Msg)
	}
	if n := f.p.count("plan") + f.p.count("transfer"); n != 0 {
		t.Errorf("calls = %q", f.p.calls)
	}
}

func TestMirrorAbortsOnEmptyRemote(t *testing.T) {
	f := newFixture(t)
	f.p.state = SourceEmpty
	_, err := f.run("pull", "mirror", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"}, engine.Request{})
	wantKind(t, err, model.KindGuard)
	if f.p.count("remote-state /srv/a") != 1 || f.p.count("transfer") != 0 || f.p.count("plan") != 0 {
		t.Errorf("calls = %q", f.p.calls)
	}
}

func TestMirrorAbortsOnMissingRemote(t *testing.T) {
	f := newFixture(t)
	f.p.state = SourceMissing
	_, err := f.run("pull", "mirror", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"}, engine.Request{})
	wantKind(t, err, model.KindGuard)
	if f.p.count("transfer") != 0 {
		t.Errorf("calls = %q", f.p.calls)
	}
}

func TestRemoteStateErrorPassesThrough(t *testing.T) {
	f := newFixture(t)
	f.p.stateErr = &model.KindError{Kind: model.KindNetwork, Msg: "connection reset"}
	_, err := f.run("pull", "mirror", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"}, engine.Request{})
	if !model.KindOf(err).Retryable() || err != f.p.stateErr {
		t.Errorf("error = %v, want the RemoteState error unchanged and retryable", err)
	}
	if f.p.count("transfer") != 0 {
		t.Errorf("calls = %q", f.p.calls)
	}
}

func TestKeepIgnoresEmptySource(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run("push", "keep", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"}, engine.Request{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.p.maxDeletes, []int{-1}) {
		t.Errorf("Transfer maxDelete = %v, want [-1]", f.p.maxDeletes)
	}
}

// deletes returns a plan with n deletions.
func deletes(n int) Plan {
	var p Plan
	for i := 0; i < n; i++ {
		p.AddDelete(fmt.Sprintf("old/%d.txt", i))
	}
	return p
}

func TestDeleteLimitBlocks(t *testing.T) {
	f := newFixture(t)
	f.p.plan = deletes(101)
	_, err := f.run("push", "mirror", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"}, engine.Request{})
	ke := wantKind(t, err, model.KindGuard)
	if !strings.Contains(ke.Msg, "101") || !strings.Contains(ke.Msg, "100") {
		t.Errorf("message = %q", ke.Msg)
	}
	if f.p.count("transfer") != 0 {
		t.Errorf("calls = %q", f.p.calls)
	}
}

func TestDeleteLimitBoundary(t *testing.T) {
	f := newFixture(t)
	f.p.plan = deletes(100)
	if _, err := f.run("push", "mirror", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"}, engine.Request{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.p.maxDeletes, []int{100}) {
		t.Errorf("Transfer maxDelete = %v, want [100]", f.p.maxDeletes)
	}
}

func TestAllowDeletesOnce(t *testing.T) {
	f := newFixture(t)
	f.p.plan = deletes(101)
	if _, err := f.run("push", "mirror", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"}, engine.Request{AllowDeletes: true}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.p.maxDeletes, []int{101}) {
		t.Errorf("Transfer maxDelete = %v, want [101]", f.p.maxDeletes)
	}
}

// A delete limit of 0 means no limit: the mirror skips the dry run that
// counts deletions and gives the tool no limit either. The source check
// still runs: an empty source never empties the destination.
func TestMirrorUnlimitedSkipsPlan(t *testing.T) {
	f := newFixture(t)
	f.p.plan = deletes(5000)
	var phases []model.Phase
	f.env.Progress = func(p model.Progress) { phases = append(phases, p.Phase) }
	task := syncTask("pull", "mirror", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"})
	task.Sync.MaxDeletes = new(0)
	e := f.engine(task, engine.Request{Kind: model.RunSync})
	if _, err := e.RunUnit(context.Background(), f.env, 0); err != nil {
		t.Fatal(err)
	}
	if f.p.count("plan") != 0 || f.p.count("remote-state") != 1 {
		t.Errorf("calls = %q", f.p.calls)
	}
	if !reflect.DeepEqual(f.p.maxDeletes, []int{-1}) {
		t.Errorf("Transfer maxDelete = %v, want [-1]", f.p.maxDeletes)
	}
	if want := []model.Phase{model.PhaseCheck, model.PhaseTransfer}; !reflect.DeepEqual(phases, want) {
		t.Errorf("phases = %q, want %q", phases, want)
	}
}

func TestMirrorUnlimitedStillGuardsEmptySource(t *testing.T) {
	f := newFixture(t)
	f.p.state = SourceEmpty
	task := syncTask("pull", "mirror", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"})
	task.Sync.MaxDeletes = new(0)
	_, err := f.engine(task, engine.Request{Kind: model.RunSync}).RunUnit(context.Background(), f.env, 0)
	wantKind(t, err, model.KindGuard)
	if f.p.count("transfer") != 0 {
		t.Errorf("calls = %q", f.p.calls)
	}
}

// The dry-run button still shows what would happen, limit or not.
func TestDryRunWithUnlimitedDeletes(t *testing.T) {
	f := newFixture(t)
	task := syncTask("push", "mirror", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"})
	task.Sync.MaxDeletes = new(0)
	if _, err := f.engine(task, engine.Request{Kind: model.RunDryRun}).RunUnit(context.Background(), f.env, 0); err != nil {
		t.Fatal(err)
	}
	if f.p.count("plan") != 1 || f.p.count("transfer") != 0 {
		t.Errorf("calls = %q", f.p.calls)
	}
}

func TestMirrorZeroDeletes(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run("push", "mirror", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"}, engine.Request{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.p.maxDeletes, []int{0}) {
		t.Errorf("Transfer maxDelete = %v, want [0]", f.p.maxDeletes)
	}
}

func TestGuardNotRetryable(t *testing.T) {
	cases := map[string]func(f *fixture) error{
		"empty local": func(f *fixture) error {
			_, err := f.run("push", "mirror", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"}, engine.Request{})
			return err
		},
		"empty remote": func(f *fixture) error {
			f.p.state = SourceEmpty
			_, err := f.run("pull", "mirror", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"}, engine.Request{})
			return err
		},
		"missing remote": func(f *fixture) error {
			f.p.state = SourceMissing
			_, err := f.run("pull", "mirror", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"}, engine.Request{})
			return err
		},
		"delete limit": func(f *fixture) error {
			f.p.plan = deletes(101)
			_, err := f.run("push", "mirror", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"}, engine.Request{})
			return err
		},
	}
	for name, c := range cases {
		err := c(newFixture(t))
		if kind := model.KindOf(err); kind != model.KindGuard || kind.Retryable() {
			t.Errorf("%s: kind %q (retryable %v), want guard, not retryable", name, kind, kind.Retryable())
		}
	}
}

func TestDryRunNeverTransfers(t *testing.T) {
	for _, mode := range []string{"keep", "mirror"} {
		f := newFixture(t)
		f.p.plan.AddTransfer("a.txt", 5)
		f.p.plan.AddTransfer("sub/b.txt", 7)
		res, err := f.run("push", mode, model.FolderPair{Local: dirWith(t, "a.txt"), Remote: "/srv/a"}, engine.Request{Kind: model.RunDryRun})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if res.Status != model.StatusSuccess || res.Files != 2 || res.Bytes != 12 {
			t.Errorf("%s: result = %+v, want success, 2 files, 12 bytes", mode, res)
		}
		if f.p.count("plan") != 1 || f.p.count("transfer") != 0 {
			t.Errorf("%s: calls = %q", mode, f.p.calls)
		}
	}
}

func TestDryRunOverLimitStillSucceeds(t *testing.T) {
	f := newFixture(t)
	f.p.plan = deletes(101)
	res, err := f.run("push", "mirror", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"}, engine.Request{Kind: model.RunDryRun})
	if err != nil || res.Status != model.StatusSuccess {
		t.Fatalf("RunUnit = %+v, %v", res, err)
	}
	if f.p.count("transfer") != 0 {
		t.Errorf("calls = %q", f.p.calls)
	}
}

func TestPlanLogged(t *testing.T) {
	f := newFixture(t)
	f.p.plan.AddTransfer("sub/b.txt", 7)
	f.p.plan.AddDelete("old/x.txt")
	f.p.plan.AddDelete("old/y.txt")
	if _, err := f.run("push", "mirror", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"}, engine.Request{}); err != nil {
		t.Fatal(err)
	}
	log := f.log.String()
	for _, want := range []string{"would transfer: sub/b.txt\n", "would delete: old/x.txt\n", "would delete: old/y.txt\n", "plan: 1 to transfer, 2 to delete\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestPlanListTruncated(t *testing.T) {
	p := deletes(5000)
	if p.DeleteCount != 5000 || len(p.Deletes) != PlanListCap {
		t.Fatalf("DeleteCount %d, %d listed", p.DeleteCount, len(p.Deletes))
	}
	f := newFixture(t)
	f.p.plan = p
	if _, err := f.run("push", "mirror", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"}, engine.Request{Kind: model.RunDryRun}); err != nil {
		t.Fatal(err)
	}
	log := f.log.String()
	if n := strings.Count(log, "would delete: "); n != 1000 {
		t.Errorf("%d 'would delete' lines, want 1000", n)
	}
	if !strings.Contains(log, "… and 4000 more\n") {
		t.Errorf("log lacks the truncation line")
	}
	if !strings.Contains(log, "plan: 0 to transfer, 5000 to delete\n") {
		t.Errorf("log lacks the plan summary")
	}
}

func TestPullTargetMissing(t *testing.T) {
	f := newFixture(t)
	missing := filepath.Join(t.TempDir(), "share")
	_, err := f.run("pull", "keep", model.FolderPair{Local: missing, Remote: "/srv/a"}, engine.Request{})
	ke := wantKind(t, err, model.KindConfig)
	if !strings.Contains(ke.Msg, "not on a mounted volume") {
		t.Errorf("message = %q", ke.Msg)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the target was created: %v", err)
	}
	if len(f.p.calls) != 1 { // just Prepare
		t.Errorf("calls = %q", f.p.calls)
	}
}

func TestPullTargetNotMounted(t *testing.T) {
	f := newFixture(t)
	f.mounted = false
	_, err := f.run("pull", "keep", model.FolderPair{Local: t.TempDir(), Remote: "/srv/a"}, engine.Request{})
	wantKind(t, err, model.KindConfig)
	if f.p.count("transfer") != 0 {
		t.Errorf("calls = %q", f.p.calls)
	}
}

func TestCloseClosesPlanner(t *testing.T) {
	f := newFixture(t)
	e := f.engine(syncTask("push", "keep", model.FolderPair{Local: "/sources/a", Remote: "/srv/a"}), engine.Request{Kind: model.RunSync})
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if f.p.closed != 1 {
		t.Errorf("Planner closed %d times", f.p.closed)
	}
}

// progressPlanner reports fixed progress updates from Transfer.
type progressPlanner struct {
	fakePlanner
	reported model.Progress
}

func (p *progressPlanner) Transfer(ctx context.Context, env engine.Env, fp model.FolderPair, maxDelete int) (model.UnitResult, error) {
	pr := p.reported
	if pr == (model.Progress{}) {
		pr = model.Progress{BytesDone: 1}
	}
	env.Progress(pr)
	return p.fakePlanner.Transfer(ctx, env, fp, maxDelete)
}

func TestProgressCarriesUnitName(t *testing.T) {
	f := newFixture(t)
	var got []model.Progress
	f.env.Progress = func(p model.Progress) { got = append(got, p) }
	local := dirWith(t, "a")
	e := New(syncTask("push", "keep", model.FolderPair{Local: local, Remote: "/srv/a"}), engine.Request{Kind: model.RunSync},
		&progressPlanner{}, func(context.Context) error { return nil }, func(string) (bool, error) { return true, nil })
	if _, err := e.RunUnit(context.Background(), f.env, 0); err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.Unit != local+" → /srv/a" {
			t.Errorf("progress without the unit: %+v", p)
		}
	}
	if len(got) == 0 || got[len(got)-1].BytesDone != 1 {
		t.Errorf("progress = %+v", got)
	}
}

func TestMirrorProgressUsesPlanTotals(t *testing.T) {
	f := newFixture(t)
	var got []model.Progress
	f.env.Progress = func(p model.Progress) { got = append(got, p) }
	local := dirWith(t, "a")
	p := &progressPlanner{
		reported: model.Progress{
			BytesDone: 100, BytesTotal: 500, TotalEstimated: true,
			FilesDone: 1, FilesTotal: 5, SpeedBps: 50, ETASec: 8,
		},
	}
	p.plan.AddTransfer("big.bin", 1000)
	p.plan.AddTransfer("more.bin", 1100)
	e := New(syncTask("push", "mirror", model.FolderPair{Local: local, Remote: "/srv/a"}), engine.Request{Kind: model.RunSync},
		p, func(context.Context) error { return nil }, func(string) (bool, error) { return true, nil })
	if _, err := e.RunUnit(context.Background(), f.env, 0); err != nil {
		t.Fatal(err)
	}
	want := model.Progress{
		Unit: local + " → /srv/a", Phase: model.PhaseTransfer, BytesDone: 100, BytesTotal: 2100, TotalEstimated: false,
		FilesDone: 1, FilesTotal: 5, SpeedBps: 50, ETASec: 40,
	}
	if len(got) == 0 || got[len(got)-1] != want {
		t.Errorf("progress = %+v, want last %+v", got, want)
	}
	// The plan's totals are there from the start of the transfer.
	if start := got[len(got)-2]; start.Phase != model.PhaseTransfer || start.BytesTotal != 2100 || start.FilesTotal != 2 {
		t.Errorf("transfer start = %+v", start)
	}
}

func TestKeepProgressPreservesEstimated(t *testing.T) {
	f := newFixture(t)
	var got []model.Progress
	f.env.Progress = func(p model.Progress) { got = append(got, p) }
	local := dirWith(t, "a")
	p := &progressPlanner{
		reported: model.Progress{
			BytesDone: 100, BytesTotal: 500, TotalEstimated: true,
			FilesDone: 1, FilesTotal: 5, SpeedBps: 50, ETASec: 8,
		},
	}
	e := New(syncTask("push", "keep", model.FolderPair{Local: local, Remote: "/srv/a"}), engine.Request{Kind: model.RunSync},
		p, func(context.Context) error { return nil }, func(string) (bool, error) { return true, nil })
	if _, err := e.RunUnit(context.Background(), f.env, 0); err != nil {
		t.Fatal(err)
	}
	if last := got[len(got)-1]; !last.TotalEstimated || last.BytesTotal != 500 {
		t.Errorf("progress = %+v", got)
	}
}

// phasePlanner reports what the tools report: dry-run counts from Plan
// (with the speed and totals rclone's accounting makes up for them), and
// transfer progress.
type phasePlanner struct{ fakePlanner }

func (p *phasePlanner) Plan(ctx context.Context, env engine.Env, fp model.FolderPair) (Plan, error) {
	env.Progress(model.Progress{FilesDone: 3, BytesDone: 300, Scanned: 40, BytesTotal: 300, FilesTotal: 3, SpeedBps: 1e9, ETASec: 1})
	return p.fakePlanner.Plan(ctx, env, fp)
}

func (p *phasePlanner) Transfer(ctx context.Context, env engine.Env, fp model.FolderPair, maxDelete int) (model.UnitResult, error) {
	env.Progress(model.Progress{BytesDone: 1000, BytesTotal: 1000, SpeedBps: 120})
	return p.fakePlanner.Transfer(ctx, env, fp, maxDelete)
}

func TestProgressPhases(t *testing.T) {
	f := newFixture(t)
	var got []model.Progress
	f.env.Progress = func(p model.Progress) { got = append(got, p) }
	pl := &phasePlanner{fakePlanner: fakePlanner{state: SourceOK}}
	local := dirWith(t, "a")
	e := New(syncTask("push", "mirror", model.FolderPair{Local: local, Remote: "/srv/a"}), engine.Request{Kind: model.RunSync},
		pl, func(context.Context) error { return nil }, func(string) (bool, error) { return true, nil })
	if err := e.Preflight(context.Background(), f.env); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunUnit(context.Background(), f.env, 0); err != nil {
		t.Fatal(err)
	}
	unit := local + " → /srv/a"
	want := []model.Progress{
		{Phase: model.PhaseConnect},
		{Unit: unit, Phase: model.PhaseCheck},
		{Unit: unit, Phase: model.PhasePlan},
		// Speed, ETA and totals of a dry run are rclone's bookkeeping.
		{Unit: unit, Phase: model.PhasePlan, FilesDone: 3, BytesDone: 300, Scanned: 40},
		{Unit: unit, Phase: model.PhaseTransfer},
		{Unit: unit, Phase: model.PhaseTransfer, BytesDone: 1000, BytesTotal: 1000, SpeedBps: 120},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("progress =\n%+v\nwant\n%+v", got, want)
	}
}

func TestDryRunPhases(t *testing.T) {
	f := newFixture(t)
	var phases []model.Phase
	f.env.Progress = func(p model.Progress) { phases = append(phases, p.Phase) }
	if _, err := f.run("push", "keep", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"}, engine.Request{Kind: model.RunDryRun}); err != nil {
		t.Fatal(err)
	}
	if want := []model.Phase{model.PhaseConnect, model.PhaseCheck, model.PhasePlan}; !reflect.DeepEqual(slices.Compact(phases), want) {
		t.Errorf("phases = %q, want %q", phases, want)
	}
}

func TestPlanLogOneLinePerPath(t *testing.T) {
	f := newFixture(t)
	f.p.plan.AddTransfer("new\nline.txt", 1)
	if _, err := f.run("push", "keep", model.FolderPair{Local: dirWith(t, "a"), Remote: "/srv/a"}, engine.Request{Kind: model.RunDryRun}); err != nil {
		t.Fatal(err)
	}
	if want := "would transfer: \"new\\nline.txt\"\n"; !strings.Contains(f.log.String(), want) {
		t.Errorf("log lacks %q:\n%s", want, f.log.String())
	}
}
