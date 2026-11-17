package api

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"tern/internal/events"
	"tern/internal/model"
	"tern/internal/sshx/sshtest"
)

// recorder keeps every run.updated event so tests can wait for a run to
// reach a state without sleeping.
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
			t.Fatalf("timed out waiting for %s", what)
		}
		r.cond.Wait()
	}
}

func (r *recorder) final(t *testing.T, runID string) model.Run {
	t.Helper()
	return r.wait(t, "run "+runID+" to finish", func(run model.Run) bool { return run.ID == runID && run.Status.Terminal() })
}

func (h *harness) sshConnID() string {
	h.t.Helper()
	return h.newSSHConn(sshtest.Start(h.t))["id"].(string)
}

func (h *harness) davConnID() string {
	h.t.Helper()
	return wantStatus(h.t, h.do("POST", "/api/v1/connections", davBody("dav-"+model.NewID())), http.StatusCreated)["id"].(string)
}

func syncBody(name, connID string) map[string]any {
	return map[string]any{
		"type": "sync", "name": name, "enabled": true,
		"sync": map[string]any{
			"protocol": "sftp", "connection_id": connID, "direction": "push",
			"pairs": []map[string]any{{"local": "/mnt/photos", "remote": "backup/photos"}},
		},
	}
}

func backupBody(name, connID string) map[string]any {
	return map[string]any{
		"type": "backup", "name": name, "enabled": true,
		"backup":        map[string]any{"connection_id": connID, "repo_path": "/srv/restic/docs", "sources": []string{"/mnt/docs"}},
		"repo_password": repoPass, "recovery_confirmed": true,
	}
}

// newTask creates a task and returns it as the API answered.
func (h *harness) newTask(body map[string]any) map[string]any {
	h.t.Helper()
	return wantStatus(h.t, h.do("POST", "/api/v1/tasks", body), http.StatusCreated)
}

// newSyncTask creates a manual sync task on a fresh SSH connection.
func (h *harness) newSyncTask(name string) map[string]any {
	h.t.Helper()
	return h.newTask(syncBody(name, h.sshConnID()))
}

func taskPath(task map[string]any) string { return "/api/v1/tasks/" + task["id"].(string) }

// runNow starts a run and returns its ID.
func (h *harness) runNow(task map[string]any, body any) string {
	h.t.Helper()
	return wantStatus(h.t, h.do("POST", taskPath(task)+"/run", body), http.StatusAccepted)["run_id"].(string)
}

func (h *harness) getRun(id string) map[string]any {
	h.t.Helper()
	return wantStatus(h.t, h.do("GET", "/api/v1/runs/"+id, nil), http.StatusOK)
}

func decodeList(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var list []map[string]any
	if err := jsonUnmarshal(body, &list); err != nil {
		t.Fatalf("response is not a JSON array: %v\n%s", err, body)
	}
	return list
}

// max_deletes 0 means no limit and survives create and update; left out,
// it is 100.
func TestSyncTaskZeroDeleteLimit(t *testing.T) {
	h := readyHarness(t)
	body := syncBody("photos", h.sshConnID())
	body["sync"].(map[string]any)["max_deletes"] = 0
	task := h.newTask(body)
	if got := task["sync"].(map[string]any)["max_deletes"]; got != float64(0) {
		t.Fatalf("created max_deletes = %v, want 0", got)
	}
	updated := wantStatus(t, h.do("PUT", taskPath(task), task), http.StatusOK)
	if got := updated["sync"].(map[string]any)["max_deletes"]; got != float64(0) {
		t.Errorf("updated max_deletes = %v, want 0", got)
	}
}

func TestCreateSyncTask(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	if task["id"] == "" || task["version"] != float64(1) || task["type"] != "sync" || task["created_at"] != "2026-11-10T12:00:00Z" {
		t.Errorf("task = %v", task)
	}
	if r := task["retry"].(map[string]any); r["retries"] != float64(3) {
		t.Errorf("retry = %v", r)
	}
	s := task["sync"].(map[string]any)
	if s["max_deletes"] != float64(100) || s["delete_mode"] != "keep" || s["transfers"] != float64(4) {
		t.Errorf("sync = %v", s)
	}
	if _, has := task["backup"]; has {
		t.Errorf("sync task has a backup section: %v", task)
	}
	got := wantStatus(t, h.do("GET", taskPath(task), nil), http.StatusOK)
	if got["name"] != "photos" {
		t.Errorf("GET = %v", got)
	}
	list := decodeList(t, h.do("GET", "/api/v1/tasks", nil).Body.Bytes())
	if len(list) != 1 || list[0]["id"] != task["id"] {
		t.Errorf("list = %v", list)
	}
	wantError(t, h.do("POST", "/api/v1/tasks", syncBody("photos", s["connection_id"].(string))), http.StatusConflict, "conflict")
	wantError(t, h.do("GET", "/api/v1/tasks/nope", nil), http.StatusNotFound, "not_found")
}

func TestTasksListEmptyIsArray(t *testing.T) {
	h := readyHarness(t)
	if body := h.do("GET", "/api/v1/tasks", nil).Body.String(); body != "[]\n" {
		t.Errorf("body = %q", body)
	}
}

func TestCreateBackupTask(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	b := task["backup"].(map[string]any)
	if b["host"] != "nas-1" || b["connections"] != float64(5) || b["prune_every_days"] != float64(7) || b["check_every_days"] != float64(30) {
		t.Errorf("backup = %v", b)
	}
}

func TestCreateTaskValidation(t *testing.T) {
	h := readyHarness(t)
	body := syncBody("photos", h.sshConnID())
	body["sync"].(map[string]any)["protocol"] = "rsync"
	body["sync"].(map[string]any)["transfers"] = 4
	e := wantError(t, h.do("POST", "/api/v1/tasks", body), http.StatusBadRequest, "validation")
	wantField(t, e, "sync.transfers")

	body = syncBody("photos", h.sshConnID())
	body["schedule"] = "0 0 31 2 *"
	e = wantError(t, h.do("POST", "/api/v1/tasks", body), http.StatusBadRequest, "validation")
	if e["fields"].(map[string]any)["schedule"] != "never_fires" {
		t.Errorf("fields = %v", e["fields"])
	}
	if n := h.count("tasks"); n != 0 {
		t.Errorf("%d tasks stored", n)
	}
}

func TestConnectionTypeMismatch(t *testing.T) {
	h := readyHarness(t)
	dav, sshConn := h.davConnID(), h.sshConnID()
	e := wantError(t, h.do("POST", "/api/v1/tasks", syncBody("photos", dav)), http.StatusBadRequest, "validation")
	wantField(t, e, "sync.connection_id")

	body := syncBody("photos", sshConn)
	body["sync"].(map[string]any)["protocol"] = "webdav"
	wantField(t, wantError(t, h.do("POST", "/api/v1/tasks", body), http.StatusBadRequest, "validation"), "sync.connection_id")

	wantField(t, wantError(t, h.do("POST", "/api/v1/tasks", backupBody("docs", dav)), http.StatusBadRequest, "validation"), "backup.connection_id")

	body = syncBody("photos", sshConn)
	body["post"] = map[string]any{"script": "true", "where": "ssh", "connection_id": dav}
	wantField(t, wantError(t, h.do("POST", "/api/v1/tasks", body), http.StatusBadRequest, "validation"), "post.connection_id")

	body = syncBody("photos", dav)
	body["sync"].(map[string]any)["protocol"] = "webdav"
	h.newTask(body)
}

func TestUnknownConnection(t *testing.T) {
	h := readyHarness(t)
	e := wantError(t, h.do("POST", "/api/v1/tasks", syncBody("photos", "nope")), http.StatusBadRequest, "validation")
	wantField(t, e, "sync.connection_id")
}

// update PUTs task with the given changes applied.
func (h *harness) update(task map[string]any, change func(map[string]any)) *httpResult {
	h.t.Helper()
	body := map[string]any{}
	for k, v := range task {
		body[k] = v
	}
	delete(body, "repo_state") // only in the answer to a create
	change(body)
	return &httpResult{h.do("PUT", taskPath(task), body)}
}

func TestUpdateTask(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	h.now = h.now.Add(time.Hour)
	h.clk.Advance(time.Hour)
	got := wantStatus(t, h.update(task, func(b map[string]any) { b["name"] = "pictures"; b["schedule"] = "0 3 * * *" }).w, http.StatusOK)
	if got["name"] != "pictures" || got["version"] != float64(2) || got["schedule"] != "0 3 * * *" ||
		got["created_at"] != task["created_at"] || got["updated_at"] != "2026-11-10T13:00:00Z" {
		t.Errorf("updated = %v", got)
	}
	e := wantError(t, h.update(got, func(b map[string]any) { b["type"] = "backup" }).w, http.StatusBadRequest, "validation")
	wantField(t, e, "type")
}

func TestUpdateVersionConflict(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	wantStatus(t, h.update(task, func(b map[string]any) { b["name"] = "one" }).w, http.StatusOK)
	wantError(t, h.update(task, func(b map[string]any) { b["name"] = "two" }).w, http.StatusConflict, "conflict")
}

func TestBackupHostImmutable(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	e := wantError(t, h.update(task, func(b map[string]any) {
		bk := b["backup"].(map[string]any)
		bk["host"] = "other-node"
	}).w, http.StatusBadRequest, "validation")
	if e["fields"].(map[string]any)["backup.host"] != "immutable" {
		t.Errorf("fields = %v", e["fields"])
	}
}

func TestDeleteTask(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	id := h.runNow(task, nil)
	h.rec.final(t, id)
	if w := h.do("DELETE", taskPath(task), nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	wantError(t, h.do("GET", taskPath(task), nil), http.StatusNotFound, "not_found")
	wantError(t, h.do("DELETE", taskPath(task), nil), http.StatusNotFound, "not_found")
	if got := h.getRun(id); got["task_name"] != "photos" {
		t.Errorf("run record after deleting its task = %v", got)
	}
}

func TestDeleteActiveTask(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	eng := h.blocked("photos")
	id := h.runNow(task, nil)
	eng.WaitCalls(2)
	wantError(t, h.do("DELETE", taskPath(task), nil), http.StatusConflict, "task_active")
	close(eng.Block)
	h.rec.final(t, id)
	if w := h.do("DELETE", taskPath(task), nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete after the run = %d %s", w.Code, w.Body)
	}
}

func TestEditWhileRunningAllowed(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	eng := h.blocked("photos")
	id := h.runNow(task, nil)
	eng.WaitCalls(2)
	wantStatus(t, h.update(task, func(b map[string]any) { b["name"] = "pictures" }).w, http.StatusOK)
	wantStatus(t, h.do("POST", taskPath(task)+"/enabled", map[string]any{"enabled": false}), http.StatusOK)
	close(eng.Block)
	h.rec.final(t, id)
	if got := h.getRun(id); got["task_name"] != "photos" || got["status"] != "success" {
		t.Errorf("run = %v, want it to finish under the old name", got)
	}
}

func TestDeleteConnectionInUse(t *testing.T) {
	h := readyHarness(t)
	main, hook := h.sshConnID(), h.sshConnID()
	body := syncBody("photos", main)
	body["pre"] = map[string]any{"script": "true", "where": "ssh", "connection_id": hook}
	task := h.newTask(body)
	for _, id := range []string{main, hook} {
		wantError(t, h.do("DELETE", "/api/v1/connections/"+id, nil), http.StatusConflict, "in_use")
	}
	h.do("DELETE", taskPath(task), nil)
	for _, id := range []string{main, hook} {
		if w := h.do("DELETE", "/api/v1/connections/"+id, nil); w.Code != http.StatusNoContent {
			t.Errorf("delete unused connection = %d %s", w.Code, w.Body)
		}
	}
}

func TestSetEnabled(t *testing.T) {
	h := readyHarness(t)
	body := syncBody("photos", h.sshConnID())
	body["schedule"] = "0 3 * * *"
	task := h.newTask(body)
	got := wantStatus(t, h.do("POST", taskPath(task)+"/enabled", map[string]any{"enabled": false}), http.StatusOK)
	if got["enabled"] != false || got["version"] != float64(2) {
		t.Errorf("after disable: %v", got)
	}
	h.advance(15 * time.Hour) // 03:00 the next day
	if n := h.count("runs"); n != 0 {
		t.Fatalf("%d runs of a disabled task", n)
	}
	wantStatus(t, h.do("POST", taskPath(task)+"/enabled", map[string]any{"enabled": true}), http.StatusOK)
	h.advance(24 * time.Hour)
	if n := h.count("runs"); n != 1 {
		t.Errorf("%d runs after enabling again, want 1", n)
	}
}

func TestSchedulePreview(t *testing.T) {
	h := readyHarness(t)
	got := wantStatus(t, h.do("POST", "/api/v1/schedule/preview", map[string]any{"schedule": "0 3 * * *"}), http.StatusOK)
	next := got["next"].([]any)
	if len(next) != 5 || next[0] != "2026-11-11T03:00:00Z" || next[4] != "2026-11-15T03:00:00Z" || got["period_sec"] != float64(86400) {
		t.Errorf("preview = %v", got)
	}
	for _, bad := range []string{"nope", "", "0 0 31 2 *"} {
		e := wantError(t, h.do("POST", "/api/v1/schedule/preview", map[string]any{"schedule": bad}), http.StatusBadRequest, "validation")
		wantField(t, e, "schedule")
	}
}

func TestCommandLine(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	got := wantStatus(t, h.do("GET", taskPath(task)+"/command-line", nil), http.StatusOK)
	lines := got["lines"].([]any)
	if len(lines) != 1 || lines[0] != "fake-tool --for photos" {
		t.Errorf("lines = %v", lines)
	}
	if n := h.count("runs"); n != 0 {
		t.Errorf("asking for the command line created %d runs", n)
	}
}

func TestTasksNeedUnlockedVault(t *testing.T) {
	h := newHarness(t)
	h.setupLegacy() // locked
	for _, req := range [][2]string{{"GET", "/api/v1/tasks"}, {"GET", "/api/v1/runs"}, {"GET", "/api/v1/overview"}, {"GET", "/api/v1/events"}} {
		wantError(t, h.do(req[0], req[1], nil), http.StatusServiceUnavailable, "vault_locked")
	}
	h.cookie = ""
	wantError(t, h.do("GET", "/api/v1/tasks", nil), http.StatusUnauthorized, "unauthorized")
}
