package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tern/internal/model"
	"tern/internal/runlog"
)

// oldRun stores a run of taskID that started and finished age ago, with a
// log file; final or still in progress.
func (f *fx) oldRun(taskID string, age time.Duration, final bool) string {
	f.t.Helper()
	ctx := context.Background()
	at := f.clk.Now().Add(-age).UTC().Truncate(time.Second)
	r := model.Run{ID: model.NewID(), TaskID: taskID, TaskName: taskID, Kind: model.RunSync, Trigger: model.TriggerSchedule,
		Status: model.StatusQueued, StartedAt: at}
	if err := f.st.CreateRun(ctx, r); err != nil {
		f.t.Fatal(err)
	}
	if final {
		r.Status, r.FinishedAt = model.StatusSuccess, at
		if _, err := f.st.FinalizeRun(ctx, r); err != nil {
			f.t.Fatal(err)
		}
	}
	w, err := runlog.Create(f.s.d.Runner.DataDir, r.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	w.Write([]byte("log\n"))
	w.Close()
	return r.ID
}

func (f *fx) exists(runID string) (record, log bool) {
	_, err := f.st.GetRun(context.Background(), runID)
	_, ferr := os.Stat(filepath.Join(f.s.d.Runner.DataDir, "logs", runID+".log"))
	return err == nil, ferr == nil
}

const day = 24 * time.Hour

func TestHousekeepByAge(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	old, recent := f.oldRun("t1", 91*day, true), f.oldRun("t1", 89*day, true)
	f.start() // housekeeping runs once at start-up
	if rec, log := f.exists(old); rec || log {
		t.Errorf("91-day-old run: record %v, log %v; want both gone", rec, log)
	}
	if rec, log := f.exists(recent); !rec || !log {
		t.Errorf("89-day-old run: record %v, log %v; want both kept", rec, log)
	}
}

func TestHousekeepByCount(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	var ids []string // oldest first
	for i := 205; i >= 1; i-- {
		ids = append(ids, f.oldRun("t1", time.Duration(i)*time.Minute, true))
	}
	other := f.oldRun("t2", 300*time.Minute, true)
	if err := f.s.Housekeep(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		rec, log := f.exists(id)
		if want := i >= 5; rec != want || log != want {
			t.Fatalf("run %d of 205 (oldest first): record %v, log %v; want %v", i, rec, log, want)
		}
	}
	if rec, _ := f.exists(other); !rec {
		t.Error("the limit is per task: t2's only run must stay")
	}
}

func TestHousekeepHonoursSettings(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	ctx := context.Background()
	f.st.SetMeta(ctx, MetaLogKeepDays, "7")
	f.st.SetMeta(ctx, MetaLogKeepRuns, "2")
	tooOld := f.oldRun("t1", 8*day, true)
	var recent []string
	for i := 3; i >= 1; i-- {
		recent = append(recent, f.oldRun("t2", time.Duration(i)*time.Hour, true))
	}
	if err := f.s.Housekeep(ctx); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.exists(tooOld); rec {
		t.Error("log_keep_days 7: an 8-day-old run should be gone")
	}
	for i, id := range recent {
		if rec, _ := f.exists(id); rec != (i >= 1) {
			t.Errorf("log_keep_runs 2: run %d of 3 kept = %v", i, rec)
		}
	}
}

func TestHousekeepKeepsActive(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	active := f.oldRun("t1", 400*day, false)
	if err := f.s.Housekeep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec, log := f.exists(active); !rec || !log {
		t.Errorf("run in progress: record %v, log %v; want both kept", rec, log)
	}
}

func TestHousekeepRunsDaily(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	f.start()
	old := f.oldRun("t1", 100*day, true) // appears after the start-up sweep
	f.tick(23 * time.Hour)
	if rec, _ := f.exists(old); !rec {
		t.Fatal("housekeeping ran again before 24 hours had passed")
	}
	f.tick(time.Hour)
	if rec, log := f.exists(old); rec || log {
		t.Errorf("after 24h: record %v, log %v; want both gone", rec, log)
	}
}

func TestHousekeepRunsWhilePaused(t *testing.T) {
	f := newFx(t, at(10, 2, 0))
	old := f.oldRun("t1", 100*day, true)
	f.s.SetPaused(true) // vault locked: no runs, but the disk still fills
	f.start()
	if rec, _ := f.exists(old); rec {
		t.Error("housekeeping did not run while paused")
	}
}
