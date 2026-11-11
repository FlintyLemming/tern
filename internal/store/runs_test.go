package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"tern/internal/model"
)

func newRun(taskID string, kind model.RunKind, started time.Time) model.Run {
	return model.Run{
		ID: model.NewID(), TaskID: taskID, TaskName: "task " + taskID, Kind: kind,
		Trigger: model.TriggerManual, Status: model.StatusQueued, StartedAt: started,
	}
}

func mustCreateRun(t *testing.T, s *Store, r model.Run) {
	t.Helper()
	if err := s.CreateRun(context.Background(), r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
}

// finish finalizes r with st and returns the seq.
func finish(t *testing.T, s *Store, r model.Run, st model.Status) int64 {
	t.Helper()
	r.Status, r.FinishedAt = st, r.StartedAt.Add(time.Minute)
	seq, err := s.FinalizeRun(context.Background(), r)
	if err != nil {
		t.Fatalf("FinalizeRun: %v", err)
	}
	return seq
}

func TestRunRoundTrip(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	r := newRun("t1", model.RunSync, t0)
	mustCreateRun(t, s, r)
	if err := s.UpdateRun(ctx, r.ID, model.StatusRunning, 2); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(ctx, r.ID)
	if err != nil || got.Status != model.StatusRunning || got.Attempts != 2 || got.Seq != 0 || !got.FinishedAt.IsZero() {
		t.Fatalf("running: %+v, %v", got, err)
	}
	r.Status, r.FinishedAt, r.Attempts, r.Bytes, r.Files = model.StatusPartial, t0.Add(time.Hour), 3, 1234, 5
	r.ErrorKind, r.ErrorSummary = model.KindConfig, "boom"
	r.Units = []model.UnitResult{{Name: "/a", Status: model.StatusSuccess, Bytes: 1234, Files: 5}, {Name: "/b", Status: model.StatusFailed, Error: "boom"}}
	seq, err := s.FinalizeRun(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.Seq = seq
	got, _ = s.GetRun(ctx, r.ID)
	if !reflect.DeepEqual(got, r) {
		t.Errorf("got  %+v\nwant %+v", got, r)
	}
	if _, err := s.GetRun(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRun(nope) = %v", err)
	}
	if err := s.UpdateRun(ctx, model.NewID(), model.StatusSuccess, 1); err == nil {
		t.Error("UpdateRun accepted a terminal status")
	}
}

func TestFinalizeAssignsMonotonicSeq(t *testing.T) {
	s := open(t, t.TempDir())
	for want := int64(1); want <= 3; want++ {
		r := newRun("t1", model.RunSync, t0)
		mustCreateRun(t, s, r)
		if seq := finish(t, s, r, model.StatusSuccess); seq != want {
			t.Errorf("seq = %d, want %d", seq, want)
		}
	}
}

func TestSeqSurvivesPrune(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		r := newRun("t1", model.RunSync, t0)
		mustCreateRun(t, s, r)
		finish(t, s, r, model.StatusSuccess)
	}
	removed, err := s.PruneRuns(ctx, t0.Add(24*time.Hour), 200)
	if err != nil || len(removed) != 3 {
		t.Fatalf("PruneRuns = %v, %v", removed, err)
	}
	r := newRun("t1", model.RunSync, t0)
	mustCreateRun(t, s, r)
	if seq := finish(t, s, r, model.StatusSuccess); seq != 4 {
		t.Errorf("seq after prune = %d, want 4", seq)
	}
}

func TestFinalizeTwice(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	r := newRun("t1", model.RunSync, t0)
	mustCreateRun(t, s, r)
	finish(t, s, r, model.StatusSuccess)
	before, _ := s.GetRun(ctx, r.ID)

	r.Status, r.FinishedAt, r.ErrorSummary = model.StatusFailed, t0.Add(time.Hour), "late"
	if _, err := s.FinalizeRun(ctx, r); !errors.Is(err, ErrFinalized) {
		t.Errorf("second FinalizeRun = %v, want ErrFinalized", err)
	}
	after, _ := s.GetRun(ctx, r.ID)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("record changed:\n%+v\n%+v", before, after)
	}
	ghost := newRun("t1", model.RunSync, t0)
	ghost.Status = model.StatusFailed
	if _, err := s.FinalizeRun(ctx, ghost); !errors.Is(err, ErrNotFound) {
		t.Errorf("FinalizeRun of unknown run = %v, want ErrNotFound", err)
	}
	// The failed attempts must not have consumed sequence numbers.
	next := newRun("t1", model.RunSync, t0)
	mustCreateRun(t, s, next)
	if seq := finish(t, s, next, model.StatusSuccess); seq != 2 {
		t.Errorf("next seq = %d, want 2", seq)
	}
}

func TestFinalizeNeedsTerminalStatus(t *testing.T) {
	s := open(t, t.TempDir())
	r := newRun("t1", model.RunSync, t0)
	mustCreateRun(t, s, r)
	r.Status = model.StatusRunning
	if _, err := s.FinalizeRun(context.Background(), r); err == nil {
		t.Error("FinalizeRun accepted a non-terminal status")
	}
}

func TestUpdateAfterFinalizeNoop(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	r := newRun("t1", model.RunSync, t0)
	mustCreateRun(t, s, r)
	finish(t, s, r, model.StatusSuccess)
	if err := s.UpdateRun(ctx, r.ID, model.StatusRunning, 9); !errors.Is(err, ErrFinalized) {
		t.Errorf("UpdateRun after finalize = %v, want ErrFinalized", err)
	}
	got, _ := s.GetRun(ctx, r.ID)
	if got.Status != model.StatusSuccess || got.Attempts != 0 {
		t.Errorf("record changed: %+v", got)
	}
}

func TestLastSuccessUpdated(t *testing.T) {
	cases := []struct {
		kind    model.RunKind
		status  model.Status
		updated bool
	}{
		{model.RunSync, model.StatusSuccess, true},
		{model.RunBackup, model.StatusWarning, true},
		{model.RunSync, model.StatusPartial, false},
		{model.RunBackup, model.StatusFailed, false},
		{model.RunDryRun, model.StatusSuccess, false},
		{model.RunRestore, model.StatusSuccess, false},
		{model.RunPrune, model.StatusSuccess, false},
		{model.RunCheck, model.StatusSuccess, false},
	}
	for _, c := range cases {
		s := open(t, t.TempDir())
		task := mustCreateTask(t, s, sampleTask("photos"))
		r := newRun(task.ID, c.kind, t0)
		mustCreateRun(t, s, r)
		finish(t, s, r, c.status)
		st, err := s.GetTaskState(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.LastSuccessAt.Equal(t0.Add(time.Minute)); got != c.updated {
			t.Errorf("%s/%s: last_success_at = %v, updated = %v, want %v", c.kind, c.status, st.LastSuccessAt, got, c.updated)
		}
	}
}

func TestInterruptLeftovers(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	var open3 []model.Run
	for i, st := range []model.Status{model.StatusRunning, model.StatusRunning, model.StatusQueued} {
		r := newRun("t1", model.RunSync, t0.Add(time.Duration(i)*time.Second))
		mustCreateRun(t, s, r)
		if st != model.StatusQueued {
			s.UpdateRun(ctx, r.ID, st, 1)
		}
		open3 = append(open3, r)
	}
	done := newRun("t1", model.RunSync, t0)
	mustCreateRun(t, s, done)
	finish(t, s, done, model.StatusSuccess)

	now := t0.Add(time.Hour)
	n, err := s.InterruptLeftovers(ctx, now)
	if err != nil || n != 3 {
		t.Fatalf("InterruptLeftovers = %d, %v; want 3", n, err)
	}
	seen := map[int64]bool{}
	for _, r := range open3 {
		got, _ := s.GetRun(ctx, r.ID)
		if got.Status != model.StatusInterrupted || got.Seq < 2 || seen[got.Seq] || !got.FinishedAt.Equal(now) {
			t.Errorf("leftover %+v", got)
		}
		seen[got.Seq] = true
	}
	if got, _ := s.GetRun(ctx, done.ID); got.Status != model.StatusSuccess || got.Seq != 1 {
		t.Errorf("finished run touched: %+v", got)
	}
	if n, _ := s.InterruptLeftovers(ctx, now); n != 0 {
		t.Errorf("second call = %d, want 0", n)
	}
}

func TestListRunsPaging(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	var ids []string // oldest first
	for i := 0; i < 5; i++ {
		r := newRun("t1", model.RunSync, t0.Add(time.Duration(i)*time.Hour))
		mustCreateRun(t, s, r)
		ids = append(ids, r.ID)
	}
	mustCreateRun(t, s, newRun("t2", model.RunSync, t0))

	var got []string
	f := RunFilter{TaskID: "t1", Limit: 2}
	for {
		page, err := s.ListRuns(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 2 {
			t.Fatalf("page of %d, limit 2", len(page))
		}
		for _, r := range page {
			got = append(got, r.ID)
		}
		f.Before = page[len(page)-1].StartedAt
	}
	want := []string{ids[4], ids[3], ids[2], ids[1], ids[0]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v\nwant    %v", got, want)
	}
	all, _ := s.ListRuns(ctx, RunFilter{})
	if len(all) != 6 {
		t.Errorf("unfiltered = %d runs, want 6", len(all))
	}
}

func TestLastRun(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	if _, ok, err := s.LastRun(ctx, "t1"); ok || err != nil {
		t.Fatalf("LastRun on empty = %v, %v", ok, err)
	}
	old := newRun("t1", model.RunSync, t0)
	mustCreateRun(t, s, old)
	finish(t, s, old, model.StatusFailed)
	recent := newRun("t1", model.RunSync, t0.Add(time.Hour))
	mustCreateRun(t, s, recent)
	finish(t, s, recent, model.StatusSuccess)
	mustCreateRun(t, s, newRun("t1", model.RunSync, t0.Add(2*time.Hour))) // still queued
	got, ok, err := s.LastRun(ctx, "t1")
	if err != nil || !ok || got.ID != recent.ID {
		t.Errorf("LastRun = %+v, %v, %v; want the latest finished run", got, ok, err)
	}
}

func TestPruneRuns(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	mk := func(task string, started time.Time, st model.Status) string {
		r := newRun(task, model.RunSync, started)
		mustCreateRun(t, s, r)
		if st.Terminal() {
			finish(t, s, r, st)
		}
		return r.ID
	}
	cutoff := t0
	oldDone := mk("t1", t0.Add(-48*time.Hour), model.StatusSuccess)
	oldActive := mk("t1", t0.Add(-48*time.Hour), model.StatusQueued)
	var t2 []string // oldest first, all newer than the cutoff
	for i := 0; i < 5; i++ {
		t2 = append(t2, mk("t2", t0.Add(time.Duration(i+1)*time.Hour), model.StatusSuccess))
	}
	t2Active := mk("t2", t0.Add(time.Hour), model.StatusQueued)

	removed, err := s.PruneRuns(ctx, cutoff, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{oldDone: true, t2[0]: true, t2[1]: true}
	if len(removed) != len(want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
	for _, id := range removed {
		if !want[id] {
			t.Errorf("removed %s unexpectedly", id)
		}
		if _, err := s.GetRun(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s still in the table", id)
		}
	}
	for _, id := range []string{oldActive, t2Active, t2[2], t2[3], t2[4]} {
		if _, err := s.GetRun(ctx, id); err != nil {
			t.Errorf("%s should have been kept: %v", id, err)
		}
	}
}
