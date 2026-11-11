package api

import (
	"errors"
	"net/http"
	"time"

	"tern/internal/model"
	"tern/internal/scheduler"
	"tern/internal/store"
)

type activeRunJSON struct {
	RunID    string          `json:"run_id"`
	Status   model.Status    `json:"status"`
	Progress *model.Progress `json:"progress"`
}

type overviewItem struct {
	TaskID        string         `json:"task_id"`
	Name          string         `json:"name"`
	Type          model.TaskType `json:"type"`
	Enabled       bool           `json:"enabled"`
	LastStatus    model.Status   `json:"last_status"` // of the latest finished run; "" when there is none
	LastRunAt     *string        `json:"last_run_at"`
	LastSuccessAt *string        `json:"last_success_at"`
	NextRunAt     *string        `json:"next_run_at"`
	Stale         bool           `json:"stale"` // too long without a success: the UI must not let this pass quietly
	StaleAfterSec int64          `json:"stale_after_sec"`
	ActiveRun     *activeRunJSON `json:"active_run"`

	// Repository maintenance of a backup task. These are runs of their
	// own and never change last_status.
	LastPruneAt     *string      `json:"last_prune_at"`
	LastCheckAt     *string      `json:"last_check_at"`
	LastCheckStatus model.Status `json:"last_check_status"` // of the latest check run; "" when there is none
}

func timePtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := rfc3339(t)
	return &s
}

func (a *API) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tasks, err := a.d.Store.ListTasks(ctx)
	if err != nil {
		internalError(w, r, err)
		return
	}
	now := a.d.Clock.Now()
	items := make([]overviewItem, 0, len(tasks))
	for _, t := range tasks {
		it := overviewItem{TaskID: t.ID, Name: t.Name, Type: t.Type, Enabled: t.Enabled}
		st, err := a.d.Store.GetTaskState(ctx, t.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue // deleted since the list was read
		}
		if err != nil {
			internalError(w, r, err)
			return
		}
		it.LastSuccessAt = timePtr(st.LastSuccessAt)
		// The task's own work: prune, check, restore and dry runs are
		// recorded separately and do not say how the backup or sync went.
		last, ok, err := a.d.Store.LastRunOfKinds(ctx, t.ID, model.RunBackup, model.RunSync)
		if err != nil {
			internalError(w, r, err)
			return
		}
		if ok {
			it.LastStatus, it.LastRunAt = last.Status, timePtr(last.StartedAt)
		}
		if t.Type == model.TaskBackup {
			it.LastPruneAt, it.LastCheckAt = timePtr(st.LastPruneAt), timePtr(st.LastCheckAt)
			check, ok, err := a.d.Store.LastRunOfKinds(ctx, t.ID, model.RunCheck)
			if err != nil {
				internalError(w, r, err)
				return
			}
			if ok {
				it.LastCheckStatus = check.Status
			}
		}
		if t.Enabled && t.Schedule != "" {
			if next, err := scheduler.NextRuns(t.Schedule, now, 1); err == nil {
				it.NextRunAt = timePtr(next[0])
			}
		}
		it.StaleAfterSec = int64(scheduler.StaleAfter(t, now) / time.Second)
		// A task someone switched off is not expected to succeed.
		it.Stale = t.Enabled && scheduler.IsStale(t, st.LastSuccessAt, now)
		if runID, ok := a.d.Scheduler.ActiveRun(t.ID); ok {
			if run, err := a.d.Store.GetRun(ctx, runID); err == nil && !run.Status.Terminal() {
				ar := &activeRunJSON{RunID: runID, Status: run.Status}
				if p, ok := a.d.Runner.Progress(runID); ok {
					ar.Progress = &p
				}
				it.ActiveRun = ar
			}
		}
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items})
}
