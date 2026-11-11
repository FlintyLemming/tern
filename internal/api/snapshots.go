package api

import (
	"context"
	"errors"
	"net/http"

	"tern/internal/engine"
	"tern/internal/engine/restic"
	"tern/internal/model"
	"tern/internal/scheduler"
)

// maxSnapshots is how many snapshots a listing returns: the newest ones.
// A repository can hold thousands.
const maxSnapshots = 200

func (a *API) registerSnapshots() {
	g := []Guard{NeedSession, NeedUnlocked}
	a.Handle("GET /api/v1/tasks/{id}/snapshots", a.handleSnapshots, g...)
	a.Handle("POST /api/v1/tasks/{id}/restore", a.handleRestore, g...)
}

type snapshotJSON struct {
	ID      string   `json:"id"`
	ShortID string   `json:"short_id"`
	Time    string   `json:"time"`
	Host    string   `json:"host"`
	Paths   []string `json:"paths"`
	Tags    []string `json:"tags"`
	Mine    bool     `json:"mine"` // made by this task
}

// handleSnapshots lists what is in the task's repository, live. The
// repository address comes along so the UI can show the equivalent restic
// commands.
func (a *API) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	t, ok := a.backupTask(w, r)
	if !ok {
		return
	}
	conn, err := a.d.Store.GetConnection(r.Context(), t.Backup.ConnectionID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	pw, err := a.repoPassword(r.Context(), t.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	defer clear(pw)
	ctx, cancel := context.WithTimeout(r.Context(), repoTimeout)
	defer cancel()
	iv, done, err := a.openRepo(ctx, t)
	if err != nil {
		internalError(w, r, err)
		return
	}
	defer done()
	if err := iv.WritePassword(pw); err != nil {
		internalError(w, r, err)
		return
	}
	snaps, err := restic.ListSnapshots(ctx, iv, t.ID)
	if err != nil {
		if !repoError(w, err) {
			internalError(w, r, err)
		}
		return
	}
	total := len(snaps)
	if len(snaps) > maxSnapshots {
		snaps = snaps[:maxSnapshots]
	}
	out := make([]snapshotJSON, len(snaps))
	for i, s := range snaps {
		out[i] = snapshotJSON{ID: s.ID, ShortID: s.ShortID, Time: rfc3339(s.Time), Host: s.Host, Paths: s.Paths, Tags: s.Tags, Mine: s.Mine}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total, "snapshots": out, "repository": restic.RecoveryRepository(conn, t.Backup.RepoPath),
	})
}

// paramError answers a refused restore parameter. On false err was
// something else and nothing has been written.
func paramError(w http.ResponseWriter, err error) bool {
	var pe *restic.ParamError
	if !errors.As(err, &pe) {
		return false
	}
	writeError(w, http.StatusBadRequest, "validation", pe.Msg, map[string]string{pe.Field: pe.Code})
	return true
}

// handleRestore queues a restore of one snapshot into a new or empty
// directory. It goes through the same queue as every other run; the
// task's pre- and post-task commands do not run for it.
func (a *API) handleRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SnapshotID string `json:"snapshot_id"`
		Subpath    string `json:"subpath"`
		TargetDir  string `json:"target_dir"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	t, ok := a.backupTask(w, r)
	if !ok {
		return
	}
	params := map[string]string{"snapshot_id": req.SnapshotID, "target_dir": req.TargetDir}
	if req.Subpath != "" {
		params["subpath"] = req.Subpath
	}
	p, err := restic.ParseRestoreParams(params)
	if err == nil {
		// Checked again when the run starts; refusing now saves the user
		// a failed run.
		err = restic.ValidateRestoreTarget(p.TargetDir, t.Backup.Sources, a.d.Cfg.DataDir, a.d.Cfg.SecretsDir, a.d.Mounted)
	}
	if err != nil {
		if !paramError(w, err) {
			internalError(w, r, err)
		}
		return
	}
	a.enqueue(w, r, t.ID, engine.Request{Kind: model.RunRestore, Params: params})
}

// enqueue queues a manual run and answers with its ID.
func (a *API) enqueue(w http.ResponseWriter, r *http.Request, taskID string, req engine.Request) {
	runID, err := a.d.Scheduler.Enqueue(taskID, req, model.TriggerManual)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]string{"run_id": runID})
	case errors.Is(err, scheduler.ErrActive):
		taskActive(w)
	case errors.Is(err, scheduler.ErrPaused):
		writeError(w, http.StatusServiceUnavailable, "paused", "runs cannot start right now: the vault is locked or tern is shutting down", nil)
	default:
		storeError(w, r, err, "task")
	}
}
