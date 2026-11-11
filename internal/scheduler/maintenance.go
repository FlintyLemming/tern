package scheduler

import (
	"context"
	"time"

	"tern/internal/engine"
	"tern/internal/model"
	"tern/internal/store"
)

// Due reports whether maintenance that last succeeded at last is due again
// at now, on a period of everyDays. A period of 0 means never; maintenance
// that never ran is due.
func Due(last, now time.Time, everyDays int) bool {
	if everyDays <= 0 {
		return false
	}
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= time.Duration(everyDays)*24*time.Hour
}

// afterRunLocked follows up a finished run of a backup task. prune and
// check are slow on a weak link, so they do not run with every backup:
//
//   - a prune or check that succeeded moves its last-run time; one that
//     failed does not, and is tried again after the next good backup;
//   - after a good backup, a prune is queued if one is due;
//   - after a good backup without a prune, or after any prune, a check is
//     queued if one is due.
//
// Each is a run of its own; what happens to it never changes the status of
// the backup. s.mu is held.
func (s *Scheduler) afterRunLocked(run model.Run) {
	ok := run.Status == model.StatusSuccess || run.Status == model.StatusWarning
	ctx := context.Background()
	now := s.d.Clock.Now()

	switch run.Kind {
	case model.RunBackup:
		if !ok {
			return
		}
	case model.RunPrune, model.RunCheck:
		if ok {
			at := now.UTC().Truncate(time.Second)
			err := s.d.Store.SetTaskState(ctx, run.TaskID, func(ts *store.TaskState) {
				if run.Kind == model.RunPrune {
					ts.LastPruneAt = at
				} else {
					ts.LastCheckAt = at
				}
			})
			if err != nil && err != store.ErrNotFound {
				s.d.Log.Error("scheduler cannot record maintenance", "task", run.TaskName, "kind", run.Kind, "err", err)
			}
		}
		if run.Kind == model.RunCheck {
			return
		}
	default:
		return
	}
	if s.paused || s.closed {
		return
	}

	t, err := s.d.Store.GetTask(ctx, run.TaskID)
	if err != nil || t.Backup == nil {
		return // deleted meanwhile
	}
	st, err := s.d.Store.GetTaskState(ctx, t.ID)
	if err != nil {
		s.d.Log.Error("scheduler cannot read task state", "task", t.Name, "err", err)
		return
	}
	var next model.RunKind
	switch {
	case run.Kind == model.RunBackup && Due(st.LastPruneAt, now, t.Backup.PruneEveryDays):
		next = model.RunPrune
	case Due(st.LastCheckAt, now, t.Backup.CheckEveryDays):
		next = model.RunCheck
	default:
		return
	}
	if _, busy := s.active[t.ID]; busy {
		return // cannot happen while s.mu is held since the task was freed
	}
	if _, err := s.startLocked(ctx, t, engine.Request{Kind: next}, model.TriggerSchedule); err != nil {
		s.d.Log.Error("maintenance run not started", "task", t.Name, "kind", next, "err", err)
	}
}
