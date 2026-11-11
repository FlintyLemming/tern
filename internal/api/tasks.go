package api

import (
	"errors"
	"io"
	"net/http"
	"time"

	"tern/internal/engine"
	"tern/internal/events"
	"tern/internal/model"
	"tern/internal/scheduler"
	"tern/internal/store"
)

func (a *API) registerTasks() {
	g := []Guard{NeedSession, NeedUnlocked}
	a.Handle("GET /api/v1/tasks", a.handleListTasks, g...)
	a.Handle("POST /api/v1/tasks", a.handleCreateTask, g...)
	a.Handle("GET /api/v1/tasks/{id}", a.handleGetTask, g...)
	a.Handle("PUT /api/v1/tasks/{id}", a.handleUpdateTask, g...)
	a.Handle("DELETE /api/v1/tasks/{id}", a.handleDeleteTask, g...)
	a.Handle("POST /api/v1/tasks/{id}/enabled", a.handleSetTaskEnabled, g...)
	a.Handle("POST /api/v1/tasks/{id}/run", a.handleRunTask, g...)
	a.Handle("GET /api/v1/tasks/{id}/command-line", a.handleCommandLine, g...)
	a.Handle("POST /api/v1/schedule/preview", a.handleSchedulePreview, g...)
}

// now is the time for everything about tasks and runs, to the second.
func (a *API) now() time.Time { return a.d.Clock.Now().UTC().Truncate(time.Second) }

// taskChanged tells the scheduler and the UI that a task was created,
// edited or deleted.
func (a *API) taskChanged(id string) {
	a.d.Scheduler.Reload()
	a.d.Events.Publish(events.Event{Type: events.TaskUpdated, Data: events.TaskRef{TaskID: id}})
}

// checkTaskRefs verifies what the model cannot: that the connections a
// task names exist and are of the type its protocol needs. Problems are
// added to fields.
func (a *API) checkTaskRefs(r *http.Request, t model.Task, fields map[string]string) error {
	check := func(field, connID, wantType string) error {
		if connID == "" || fields[field] != "" {
			return nil
		}
		c, err := a.d.Store.GetConnection(r.Context(), connID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			fields[field] = "not_found"
		case err != nil:
			return err
		case c.Type != wantType:
			fields[field] = "wrong_type"
		}
		return nil
	}
	if b := t.Backup; b != nil {
		if err := check("backup.connection_id", b.ConnectionID, model.ConnSSH); err != nil {
			return err
		}
	}
	if s := t.Sync; s != nil {
		want := model.ConnSSH
		if s.Protocol == "webdav" {
			want = model.ConnWebDAV
		}
		if err := check("sync.connection_id", s.ConnectionID, want); err != nil {
			return err
		}
	}
	for name, h := range map[string]model.Hook{"pre": t.Pre, "post": t.Post} {
		if h.Where != model.HookSSH {
			continue
		}
		if err := check(name+".connection_id", h.ConnectionID, model.ConnSSH); err != nil {
			return err
		}
	}
	return nil
}

func (a *API) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := a.d.Store.ListTasks(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (a *API) handleGetTask(w http.ResponseWriter, r *http.Request) {
	a.writeTask(w, r, r.PathValue("id"), http.StatusOK)
}

func (a *API) writeTask(w http.ResponseWriter, r *http.Request, id string, status int) {
	t, err := a.d.Store.GetTask(r.Context(), id)
	if err != nil {
		storeError(w, r, err, "task")
		return
	}
	writeJSON(w, status, t)
}

func (a *API) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if !readJSON(w, r, &req) {
		return
	}
	t := req.Task
	now := a.now()
	t.ID, t.Version, t.CreatedAt, t.UpdatedAt = model.NewID(), 1, now, now
	t.ApplyDefaults()
	if t.Type == model.TaskBackup && t.Backup != nil {
		a.createBackupTask(w, r, t, req)
		return
	}
	fields := t.Validate()
	if req.RepoPassword != nil {
		fields["repo_password"] = "invalid" // only backup tasks have one
	}
	if req.RecoveryConfirmed != nil {
		fields["recovery_confirmed"] = "invalid"
	}
	if err := a.checkTaskRefs(r, t, fields); err != nil {
		internalError(w, r, err)
		return
	}
	if len(fields) > 0 {
		writeError(w, http.StatusBadRequest, "validation", "invalid task", fields)
		return
	}
	if err := a.d.Store.CreateTask(r.Context(), t, ""); err != nil {
		storeError(w, r, err, "task")
		return
	}
	a.taskChanged(t.ID)
	a.writeTask(w, r, t.ID, http.StatusCreated)
}

// handleUpdateTask changes everything about a task except its type, the
// host of a backup task and the repository password: a request carrying
// repo_password is refused like any unknown field.
func (a *API) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	var t model.Task
	if !readJSON(w, r, &t) {
		return
	}
	old, err := a.d.Store.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, r, err, "task")
		return
	}
	if t.Type != old.Type {
		writeError(w, http.StatusBadRequest, "validation", "invalid task", map[string]string{"type": "immutable"})
		return
	}
	t.ID, t.CreatedAt, t.UpdatedAt = old.ID, old.CreatedAt, a.now()
	t.ApplyDefaults()
	fields := t.Validate()
	if t.Backup != nil && old.Backup != nil && t.Backup.Host != old.Backup.Host {
		fields["backup.host"] = "immutable"
	}
	if err := a.checkTaskRefs(r, t, fields); err != nil {
		internalError(w, r, err)
		return
	}
	if len(fields) > 0 {
		writeError(w, http.StatusBadRequest, "validation", "invalid task", fields)
		return
	}
	// A run that is queued or going keeps the snapshot it was queued with.
	if err := a.d.Store.UpdateTask(r.Context(), t); err != nil {
		storeError(w, r, err, "task")
		return
	}
	a.taskChanged(t.ID)
	a.writeTask(w, r, t.ID, http.StatusOK)
}

func (a *API) handleSetTaskEnabled(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	t, err := a.d.Store.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, r, err, "task")
		return
	}
	if t.Enabled != req.Enabled {
		t.Enabled, t.UpdatedAt = req.Enabled, a.now()
		if err := a.d.Store.UpdateTask(r.Context(), t); err != nil {
			storeError(w, r, err, "task")
			return
		}
		a.taskChanged(t.ID)
	}
	a.writeTask(w, r, t.ID, http.StatusOK)
}

func taskActive(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "task_active", "the task has a run queued or running", nil)
}

func (a *API) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.d.Scheduler.Active(id) {
		taskActive(w)
		return
	}
	if err := a.d.Store.DeleteTask(r.Context(), id); err != nil {
		storeError(w, r, err, "task")
		return
	}
	a.taskChanged(id)
	w.WriteHeader(http.StatusNoContent)
}

// runKindAllowed says which kinds "run now" may start for a task type.
// Restores have their own endpoint.
func runKindAllowed(tt model.TaskType, k model.RunKind) bool {
	switch tt {
	case model.TaskBackup:
		return k == model.RunBackup || k == model.RunPrune || k == model.RunCheck
	case model.TaskSync:
		return k == model.RunSync || k == model.RunDryRun
	}
	return false
}

func (a *API) handleRunTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind             string `json:"kind"`
		AllowDeletesOnce bool   `json:"allow_deletes_once"`
	}
	// The body is optional: a bare POST is a plain "run now".
	if r.ContentLength != 0 && !readJSON(w, r, &req) {
		return
	}
	t, err := a.d.Store.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, r, err, "task")
		return
	}
	kind := model.RunKind(req.Kind)
	if kind == "" {
		kind = t.RunKind()
	}
	if !runKindAllowed(t.Type, kind) {
		writeError(w, http.StatusBadRequest, "validation", "this kind of run is not available for the task", map[string]string{"kind": "invalid"})
		return
	}
	a.enqueue(w, r, t.ID, engine.Request{Kind: kind, AllowDeletes: req.AllowDeletesOnce})
}

// handleCommandLine shows what tern would run for the task, so the user is
// never locked in: the same commands work without tern.
func (a *API) handleCommandLine(w http.ResponseWriter, r *http.Request) {
	t, err := a.d.Store.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		storeError(w, r, err, "task")
		return
	}
	eng, err := a.d.Registry.Build(t, engine.Request{Kind: t.RunKind()})
	if err != nil {
		var ke *model.KindError
		if errors.As(err, &ke) {
			writeError(w, http.StatusUnprocessableEntity, "unavailable", ke.Error(), nil)
			return
		}
		internalError(w, r, err)
		return
	}
	lines := eng.Describe()
	if c, ok := eng.(io.Closer); ok {
		c.Close()
	}
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (a *API) handleSchedulePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Schedule string `json:"schedule"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	code := model.CheckSchedule(req.Schedule)
	if req.Schedule == "" {
		code = "required"
	}
	if code != "" {
		writeError(w, http.StatusBadRequest, "validation", "invalid schedule", map[string]string{"schedule": code})
		return
	}
	now := a.d.Clock.Now()
	next, err := scheduler.NextRuns(req.Schedule, now, 5)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "invalid schedule", map[string]string{"schedule": "never_fires"})
		return
	}
	period, err := scheduler.Period(req.Schedule, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", "invalid schedule", map[string]string{"schedule": "never_fires"})
		return
	}
	out := make([]string, len(next))
	for i, t := range next {
		out[i] = rfc3339(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"next": out, "period_sec": int64(period / time.Second)})
}
