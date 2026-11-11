package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"tern/internal/engine"
	"tern/internal/engine/fake"
	"tern/internal/model"
	"tern/internal/store"
)

var maintStart = time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)

func TestDue(t *testing.T) {
	now := maintStart
	cases := []struct {
		last time.Time
		days int
		want bool
	}{
		{time.Time{}, 7, true},
		{now.Add(-6 * day), 7, false},
		{now.Add(-7 * day), 7, true},
		{now.Add(-7*day + time.Second), 7, false},
		{now.Add(-8 * day), 7, true},
		{time.Time{}, 0, false},
		{now.Add(-100 * day), 0, false},
		{now.Add(day), 7, false}, // a clock that went back does not make it due
	}
	for _, c := range cases {
		if got := Due(c.last, now, c.days); got != c.want {
			t.Errorf("Due(%v, now, %d) = %v, want %v", c.last, c.days, got, c.want)
		}
	}
}

// addBackupTask stores a manual backup task; pruneAgo and checkAgo say how
// long ago the last prune and check were (0 = never).
func (f *fx) addBackupTask(name string, pruneDays, checkDays int, pruneAgo, checkAgo time.Duration, mutate ...func(*model.Task)) model.Task {
	f.t.Helper()
	f.created++
	created := time.Date(2026, 1, 1, 0, 0, f.created, 0, time.UTC)
	task := model.Task{
		ID: model.NewID(), Version: 1, Type: model.TaskBackup, Name: name, Enabled: true,
		Backup: &model.BackupSpec{ConnectionID: "c1", RepoPath: "/srv/repo", Host: "nas-1", Sources: []string{"/a"},
			PruneEveryDays: pruneDays, CheckEveryDays: checkDays},
		CreatedAt: created, UpdatedAt: created,
	}
	task.ApplyDefaults()
	for _, m := range mutate {
		m(&task)
	}
	ctx := context.Background()
	if err := f.st.CreateTask(ctx, task, "v1.sealed"); err != nil {
		f.t.Fatal(err)
	}
	now := f.clk.Now()
	err := f.st.SetTaskState(ctx, task.ID, func(ts *store.TaskState) {
		if pruneAgo > 0 {
			ts.LastPruneAt = now.Add(-pruneAgo)
		}
		if checkAgo > 0 {
			ts.LastCheckAt = now.Add(-checkAgo)
		}
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return task
}

func (f *fx) state(taskID string) store.TaskState {
	f.t.Helper()
	st, err := f.st.GetTaskState(context.Background(), taskID)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func kinds(runs []model.Run) []model.RunKind {
	var out []model.RunKind
	for _, r := range runs {
		out = append(out, r.Kind)
	}
	return out
}

// settle waits until every run of the task so far is final and returns
// them, oldest first. Follow-up runs are queued before their predecessor's
// final event goes out, so the list is complete once nothing is unfinished.
func (f *fx) settle(taskID string) []model.Run {
	f.t.Helper()
	for {
		runs := f.runs(taskID)
		pending := ""
		for _, r := range runs {
			if !r.Status.Terminal() {
				pending = r.ID
				break
			}
		}
		if pending == "" {
			return runs
		}
		f.rec.final(f.t, pending)
	}
}

func failing(kind model.ErrKind) *fake.Engine {
	return &fake.Engine{Results: map[int][]fake.Outcome{0: {{Err: &model.KindError{Kind: kind, Msg: "scripted failure"}}}}}
}

func (f *fx) setEngine(name string, e *fake.Engine) {
	enginesMu.Lock()
	f.engines[name] = e
	enginesMu.Unlock()
}

func TestBackupSuccessEnqueuesPrune(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 7, 0, 8*day, 0)
	f.rec.final(t, f.enqueue(task))
	runs := f.settle(task.ID)
	if !reflect.DeepEqual(kinds(runs), []model.RunKind{model.RunBackup, model.RunPrune}) {
		t.Fatalf("runs = %v", kinds(runs))
	}
	if p := runs[1]; p.Trigger != model.TriggerSchedule || p.Status != model.StatusSuccess {
		t.Errorf("prune run = %+v", p)
	}
}

func TestBackupWarningEnqueuesPrune(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 7, 0, 8*day, 0)
	f.setEngine("docs", &fake.Engine{Results: map[int][]fake.Outcome{0: {{Status: model.StatusWarning}}}})
	if got := f.rec.final(t, f.enqueue(task)); got.Status != model.StatusWarning {
		t.Fatalf("backup = %s", got.Status)
	}
	if got := kinds(f.settle(task.ID)); !reflect.DeepEqual(got, []model.RunKind{model.RunBackup, model.RunPrune}) {
		t.Errorf("runs = %v", got)
	}
}

func TestBackupFailureNoMaintenance(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 7, 30, 0, 0) // both overdue
	f.setEngine("docs", failing(model.KindConfig))
	if got := f.rec.final(t, f.enqueue(task)); got.Status != model.StatusFailed {
		t.Fatalf("backup = %s", got.Status)
	}
	if got := kinds(f.settle(task.ID)); !reflect.DeepEqual(got, []model.RunKind{model.RunBackup}) {
		t.Errorf("runs = %v; a failed backup must not be followed by maintenance", got)
	}
}

func TestPruneNotDue(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 7, 0, day, 0)
	f.rec.final(t, f.enqueue(task))
	if got := kinds(f.settle(task.ID)); !reflect.DeepEqual(got, []model.RunKind{model.RunBackup}) {
		t.Errorf("runs = %v", got)
	}
}

func TestSyncTaskHasNoMaintenance(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addTask("photos", "")
	f.rec.final(t, f.enqueue(task))
	if got := kinds(f.settle(task.ID)); !reflect.DeepEqual(got, []model.RunKind{model.RunSync}) {
		t.Errorf("runs = %v", got)
	}
}

func TestCheckAfterPrune(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 7, 30, 0, 0)
	prune := f.blocked("docs/prune")
	backupID := f.enqueue(task)
	f.rec.final(t, backupID)
	prune.WaitCalls(2) // preflight, unit: the prune is running
	// The task is busy with the prune: no check yet, and nothing else gets in.
	if got := kinds(f.runs(task.ID)); !reflect.DeepEqual(got, []model.RunKind{model.RunBackup, model.RunPrune}) {
		t.Fatalf("while pruning, runs = %v", got)
	}
	if _, err := f.s.Enqueue(task.ID, engine.Request{}, model.TriggerManual); err != ErrActive {
		t.Errorf("Enqueue during the prune = %v, want ErrActive", err)
	}
	close(prune.Block)
	runs := f.settle(task.ID)
	if !reflect.DeepEqual(kinds(runs), []model.RunKind{model.RunBackup, model.RunPrune, model.RunCheck}) {
		t.Fatalf("runs = %v", kinds(runs))
	}
	for _, r := range runs[1:] {
		if r.Trigger != model.TriggerSchedule || r.Status != model.StatusSuccess {
			t.Errorf("%s run = %+v", r.Kind, r)
		}
	}
	// Finished in the order they were queued, one after the other.
	if !(runs[0].Seq < runs[1].Seq && runs[1].Seq < runs[2].Seq) {
		t.Errorf("seqs = %d, %d, %d", runs[0].Seq, runs[1].Seq, runs[2].Seq)
	}
}

func TestCheckWithoutPrune(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 7, 30, day, 31*day)
	f.rec.final(t, f.enqueue(task))
	if got := kinds(f.settle(task.ID)); !reflect.DeepEqual(got, []model.RunKind{model.RunBackup, model.RunCheck}) {
		t.Errorf("runs = %v", got)
	}
}

func TestCheckAfterFailedPrune(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 7, 30, 0, 0)
	f.setEngine("docs/prune", failing(model.KindConfig))
	f.rec.final(t, f.enqueue(task))
	runs := f.settle(task.ID)
	if !reflect.DeepEqual(kinds(runs), []model.RunKind{model.RunBackup, model.RunPrune, model.RunCheck}) || runs[1].Status != model.StatusFailed {
		t.Errorf("runs = %v, prune %s", kinds(runs), runs[1].Status)
	}
}

func TestPruneUpdatesState(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	ok := f.addBackupTask("ok", 7, 0, 8*day, 0)
	bad := f.addBackupTask("bad", 7, 0, 8*day, 0)
	f.setEngine("bad/prune", failing(model.KindConfig))
	for _, task := range []model.Task{ok, bad} {
		f.rec.final(t, f.enqueue(task))
		f.settle(task.ID)
	}
	if got := f.state(ok.ID).LastPruneAt; !got.Equal(maintStart) {
		t.Errorf("after a successful prune LastPruneAt = %v, want %v", got, maintStart)
	}
	if got := f.state(bad.ID).LastPruneAt; !got.Equal(maintStart.Add(-8 * day)) {
		t.Errorf("after a failed prune LastPruneAt = %v, want it unchanged", got)
	}
	// Not due again right after it ran.
	f.rec.final(t, f.enqueue(ok))
	if got := kinds(f.settle(ok.ID)); !reflect.DeepEqual(got, []model.RunKind{model.RunBackup, model.RunPrune, model.RunBackup}) {
		t.Errorf("runs = %v", got)
	}
	// The failed one is tried again after the next good backup.
	f.rec.final(t, f.enqueue(bad))
	if got := kinds(f.settle(bad.ID)); !reflect.DeepEqual(got, []model.RunKind{model.RunBackup, model.RunPrune, model.RunBackup, model.RunPrune}) {
		t.Errorf("runs = %v", got)
	}
}

func TestCheckFailureIsolated(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 0, 30, 0, 31*day)
	f.setEngine("docs/check", failing(model.KindConfig))
	f.rec.final(t, f.enqueue(task))
	f.clk.Advance(time.Hour) // the check ends later than the backup
	runs := f.settle(task.ID)
	if !reflect.DeepEqual(kinds(runs), []model.RunKind{model.RunBackup, model.RunCheck}) || runs[1].Status != model.StatusFailed {
		t.Fatalf("runs = %v, check %s", kinds(runs), runs[1].Status)
	}
	st := f.state(task.ID)
	if !st.LastSuccessAt.Equal(runs[0].FinishedAt) {
		t.Errorf("LastSuccessAt = %v, want the backup's %v", st.LastSuccessAt, runs[0].FinishedAt)
	}
	if !st.LastCheckAt.Equal(maintStart.Add(-31 * day)) {
		t.Errorf("LastCheckAt = %v, want it unchanged after a failed check", st.LastCheckAt)
	}
}

func TestCheckUpdatesState(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 0, 30, 0, 0)
	f.rec.final(t, f.enqueue(task))
	f.settle(task.ID)
	if got := f.state(task.ID).LastCheckAt; !got.Equal(maintStart) {
		t.Errorf("LastCheckAt = %v, want %v", got, maintStart)
	}
}

func TestManualMaintenanceIgnoresPeriod(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 7, 30, day, day)
	id, err := f.s.Enqueue(task.ID, engine.Request{Kind: model.RunPrune}, model.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.rec.final(t, id); got.Kind != model.RunPrune || got.Status != model.StatusSuccess || got.Trigger != model.TriggerManual {
		t.Errorf("run = %+v", got)
	}
	if got := f.state(task.ID).LastPruneAt; !got.Equal(maintStart) {
		t.Errorf("LastPruneAt = %v", got)
	}
}

func TestMaintenanceNoHooks(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	trace := filepath.Join(t.TempDir(), "trace")
	task := f.addBackupTask("docs", 7, 30, 0, 0, func(task *model.Task) {
		task.Pre.Script = "echo pre >> " + trace
		task.Post.Script = "echo post >> " + trace
	})
	f.rec.final(t, f.enqueue(task))
	if got := kinds(f.settle(task.ID)); len(got) != 3 {
		t.Fatalf("runs = %v", got)
	}
	got, err := os.ReadFile(trace)
	if err != nil || string(got) != "pre\npost\n" {
		t.Errorf("hooks ran as %q (%v), want once around the backup only", got, err)
	}
}

func TestNoMaintenanceForDeletedTask(t *testing.T) {
	f := newFx(t, maintStart)
	f.start()
	task := f.addBackupTask("docs", 7, 30, 0, 0)
	eng := f.blocked("docs")
	id := f.enqueue(task)
	eng.WaitCalls(2)
	if err := f.st.DeleteTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	close(eng.Block)
	f.rec.final(t, id)
	if got := kinds(f.settle(task.ID)); !reflect.DeepEqual(got, []model.RunKind{model.RunBackup}) {
		t.Errorf("runs = %v", got)
	}
}
