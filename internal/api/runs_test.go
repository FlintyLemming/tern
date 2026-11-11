package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tern/internal/engine/fake"
	"tern/internal/model"
)

type httpResult struct{ w *httptest.ResponseRecorder }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func TestRunNow(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	id := h.runNow(task, nil)
	h.rec.final(t, id)
	got := h.getRun(id)
	if got["status"] != "success" || got["trigger"] != "manual" || got["kind"] != "sync" || got["task_id"] != task["id"] ||
		got["seq"] != float64(1) || got["attempts"] != float64(1) || got["finished_at"] == nil {
		t.Errorf("run = %v", got)
	}
	wantError(t, h.do("GET", "/api/v1/runs/nope", nil), http.StatusNotFound, "not_found")
	wantError(t, h.do("POST", "/api/v1/tasks/nope/run", nil), http.StatusNotFound, "not_found")
}

func TestRunWhileActive(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	eng := h.blocked("photos")
	id := h.runNow(task, nil)
	eng.WaitCalls(2)
	wantError(t, h.do("POST", taskPath(task)+"/run", nil), http.StatusConflict, "task_active")
	close(eng.Block)
	h.rec.final(t, id)
}

func TestRunKindMismatch(t *testing.T) {
	h := readyHarness(t)
	syncTask := h.newSyncTask("photos")
	backupTask := h.newTask(backupBody("docs", h.sshConnID()))
	h.noMaintenance(backupTask) // or the manual runs below would meet automatic ones
	for _, c := range []struct {
		task map[string]any
		kind string
	}{{syncTask, "prune"}, {syncTask, "check"}, {syncTask, "backup"}, {backupTask, "dry_run"}, {backupTask, "sync"}, {syncTask, "restore"}, {syncTask, "bogus"}} {
		e := wantError(t, h.do("POST", taskPath(c.task)+"/run", map[string]any{"kind": c.kind}), http.StatusBadRequest, "validation")
		wantField(t, e, "kind")
	}
	if n := h.count("runs"); n != 0 {
		t.Fatalf("%d runs created", n)
	}
	for _, c := range []struct {
		task map[string]any
		kind string
	}{{syncTask, "dry_run"}, {backupTask, "prune"}, {backupTask, "check"}, {backupTask, "backup"}} {
		id := h.runNow(c.task, map[string]any{"kind": c.kind})
		if got := h.rec.final(t, id); string(got.Kind) != c.kind {
			t.Errorf("run kind = %s, want %s", got.Kind, c.kind)
		}
	}
}

func TestRunAllowDeletesOnce(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	h.rec.final(t, h.runNow(task, map[string]any{"allow_deletes_once": true}))
	h.rec.final(t, h.runNow(task, nil))
	built := h.engine("photos").Built()
	if len(built) != 2 || !built[0].AllowDeletes || built[1].AllowDeletes {
		t.Errorf("requests = %+v", built)
	}
}

func TestCancelRun(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	eng := h.blocked("photos")
	id := h.runNow(task, nil)
	eng.WaitCalls(2)
	if w := h.do("POST", "/api/v1/runs/"+id+"/cancel", nil); w.Code != http.StatusAccepted {
		t.Fatalf("cancel = %d %s", w.Code, w.Body)
	}
	h.rec.final(t, id)
	if got := h.getRun(id); got["status"] != "canceled" {
		t.Errorf("run = %v", got)
	}
	wantError(t, h.do("POST", "/api/v1/runs/"+id+"/cancel", nil), http.StatusConflict, "conflict")
	wantError(t, h.do("POST", "/api/v1/runs/nope/cancel", nil), http.StatusNotFound, "not_found")
}

func (h *harness) readLog(id string, offset int) map[string]any {
	h.t.Helper()
	return wantStatus(h.t, h.do("GET", fmt.Sprintf("/api/v1/runs/%s/log?offset=%d", id, offset), nil), http.StatusOK)
}

func TestLogPaging(t *testing.T) {
	h := readyHarness(t)
	body := syncBody("photos", h.sshConnID())
	// About 600 KiB of output with multi-byte characters: three pages.
	body["pre"] = map[string]any{"script": `i=0; while [ $i -lt 6000 ]; do echo "line $i 照片 ................................................................................"; i=$((i+1)); done`}
	task := h.newTask(body)
	eng := h.blocked("photos")
	id := h.runNow(task, nil)
	eng.WaitCalls(2) // the pre-task command is done, the run is not

	var text strings.Builder
	offset, pages := 0, 0
	for {
		page := h.readLog(id, offset)
		data, next := page["data"].(string), int(page["next_offset"].(float64))
		if len(data) > 256<<10 || next != offset+len(data) {
			t.Fatalf("page at %d: %d bytes, next_offset %d", offset, len(data), next)
		}
		if page["eof"] != false {
			t.Fatal("eof while the run is still going")
		}
		text.WriteString(data)
		offset = next
		if data == "" {
			break
		}
		pages++
	}
	if pages < 3 {
		t.Errorf("log came in %d pages, want at least 3", pages)
	}
	close(eng.Block)
	h.rec.final(t, id)
	for {
		page := h.readLog(id, offset)
		text.WriteString(page["data"].(string))
		offset = int(page["next_offset"].(float64))
		if page["eof"] == true {
			break
		}
	}
	got := text.String()
	if strings.Contains(got, "�") {
		t.Error("a multi-byte character was split across pages")
	}
	if strings.Count(got, " 照片 ") != 6000 || !strings.Contains(got, "line 5999 照片") || !strings.Contains(got, "finished: success") {
		t.Errorf("reassembled log is wrong: %d bytes, %d lines of hook output", len(got), strings.Count(got, " 照片 "))
	}
	wantError(t, h.do("GET", "/api/v1/runs/"+id+"/log?offset=-1", nil), http.StatusBadRequest, "bad_request")
	wantError(t, h.do("GET", "/api/v1/runs/nope/log", nil), http.StatusNotFound, "not_found")
}

func TestRunsFilter(t *testing.T) {
	h := readyHarness(t)
	a, b := h.newSyncTask("a"), h.newSyncTask("b")
	var idsA []string
	for i := 0; i < 3; i++ {
		id := h.runNow(a, nil)
		h.rec.final(t, id)
		idsA = append(idsA, id)
		h.advance(time.Hour)
	}
	h.rec.final(t, h.runNow(b, nil))

	list := func(query string) []map[string]any {
		w := h.do("GET", "/api/v1/runs"+query, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /runs%s = %d %s", query, w.Code, w.Body)
		}
		return decodeList(t, w.Body.Bytes())
	}
	if got := list(""); len(got) != 4 || got[0]["task_name"] != "b" {
		t.Errorf("all runs = %v", got)
	}
	onlyA := list("?task_id=" + a["id"].(string))
	if len(onlyA) != 3 || onlyA[0]["id"] != idsA[2] || onlyA[2]["id"] != idsA[0] {
		t.Errorf("runs of a = %v", onlyA)
	}
	page1 := list("?task_id=" + a["id"].(string) + "&limit=2")
	if len(page1) != 2 {
		t.Fatalf("page 1 = %v", page1)
	}
	page2 := list("?task_id=" + a["id"].(string) + "&limit=2&before=" + page1[1]["started_at"].(string))
	if len(page2) != 1 || page2[0]["id"] != idsA[0] {
		t.Errorf("page 2 = %v", page2)
	}
	for _, bad := range []string{"?limit=0", "?limit=abc", "?before=yesterday"} {
		wantError(t, h.do("GET", "/api/v1/runs"+bad, nil), http.StatusBadRequest, "bad_request")
	}
	if got := list("?limit=5000"); len(got) != 4 {
		t.Errorf("limit above the cap: %d runs", len(got))
	}
}

func overviewOf(t *testing.T, h *harness, taskID string) map[string]any {
	t.Helper()
	ov := wantStatus(t, h.do("GET", "/api/v1/overview", nil), http.StatusOK)
	for _, item := range ov["tasks"].([]any) {
		if m := item.(map[string]any); m["task_id"] == taskID {
			return m
		}
	}
	t.Fatalf("task %s not in overview %v", taskID, ov)
	return nil
}

func TestOverview(t *testing.T) {
	h := readyHarness(t)
	body := syncBody("photos", h.sshConnID())
	body["schedule"] = "0 3 * * *"
	task := h.newTask(body)
	id := task["id"].(string)

	o := overviewOf(t, h, id)
	if o["name"] != "photos" || o["type"] != "sync" || o["enabled"] != true || o["stale"] != false ||
		o["stale_after_sec"] != float64(172800) || o["next_run_at"] != "2026-11-11T03:00:00Z" ||
		o["last_status"] != "" || o["last_run_at"] != nil || o["last_success_at"] != nil || o["active_run"] != nil {
		t.Errorf("fresh task: %v", o)
	}

	// 49 hours pass in one step: both scheduled times are missed and not
	// caught up, so nothing has succeeded for more than two periods.
	h.advance(49 * time.Hour)
	h.login() // the session idled out meanwhile
	if o = overviewOf(t, h, id); o["stale"] != true {
		t.Errorf("after 49h without a success: %v", o)
	}

	eng := h.blocked("photos")
	runID := h.runNow(task, nil)
	eng.WaitCalls(2)
	o = overviewOf(t, h, id)
	active, _ := o["active_run"].(map[string]any)
	if active == nil || active["run_id"] != runID || active["status"] != "running" {
		t.Errorf("while running: %v", o)
	}
	close(eng.Block)
	h.rec.final(t, runID)

	o = overviewOf(t, h, id)
	if o["stale"] != false || o["last_status"] != "success" || o["last_success_at"] != "2026-11-12T13:00:00Z" ||
		o["last_run_at"] != "2026-11-12T13:00:00Z" || o["active_run"] != nil {
		t.Errorf("after a success: %v", o)
	}

	wantStatus(t, h.do("POST", taskPath(task)+"/enabled", map[string]any{"enabled": false}), http.StatusOK)
	if o = overviewOf(t, h, id); o["next_run_at"] != nil || o["enabled"] != false {
		t.Errorf("disabled: %v", o)
	}
}

func TestOverviewEmpty(t *testing.T) {
	h := readyHarness(t)
	if body := h.do("GET", "/api/v1/overview", nil).Body.String(); body != "{\"tasks\":[]}\n" {
		t.Errorf("body = %q", body)
	}
}

func TestRunWhenPaused(t *testing.T) {
	h := newHarness(t)
	h.setup("passphrase")
	task := h.newSyncTask("photos")
	h.restart() // the vault is locked again
	h.login()
	wantError(t, h.do("POST", taskPath(task)+"/run", nil), http.StatusServiceUnavailable, "vault_locked")
	// The scheduler is paused as well, for callers that get past the guard.
	if _, err := h.sched.Enqueue(task["id"].(string), engineRequest(), model.TriggerManual); err == nil {
		t.Error("the scheduler accepted a run while the vault is locked")
	}
	// Unlocking resumes it.
	if w := h.do("POST", "/api/v1/unlock", map[string]any{"passphrase": vaultPass}); w.Code != http.StatusNoContent {
		t.Fatalf("unlock = %d %s", w.Code, w.Body)
	}
	h.rec.final(t, h.runNow(task, nil))
}

func TestSchedulerResumesAfterSetup(t *testing.T) {
	h := newHarness(t) // not set up: no vault, scheduler paused
	h.setup("keyfile")
	h.rec.final(t, h.runNow(h.newSyncTask("photos"), nil))
}

// noMaintenance switches off the automatic prune and check of a backup task.
func (h *harness) noMaintenance(task map[string]any) {
	h.t.Helper()
	w := h.update(task, func(b map[string]any) {
		spec := map[string]any{}
		for k, v := range b["backup"].(map[string]any) {
			spec[k] = v
		}
		spec["prune_every_days"], spec["check_every_days"] = 0, 0
		b["backup"] = spec
	}).w
	if w.Code != http.StatusOK {
		h.t.Fatalf("switch off maintenance = %d %s", w.Code, w.Body)
	}
}

// settle waits until no run of the task is queued or running any more.
func (h *harness) settle(task map[string]any) {
	h.t.Helper()
	id := task["id"].(string)
	for {
		runID, ok := h.sched.ActiveRun(id)
		if !ok {
			return
		}
		h.rec.final(h.t, runID)
	}
}

func TestOverviewMaintenance(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	id := task["id"].(string)
	o := overviewOf(t, h, id)
	if o["last_prune_at"] != nil || o["last_check_at"] != nil || o["last_check_status"] != "" {
		t.Errorf("fresh task: %v", o)
	}

	// The first backup is followed by a prune and a check, both overdue.
	h.rec.final(t, h.runNow(task, nil))
	h.settle(task)
	o = overviewOf(t, h, id)
	if o["last_prune_at"] != "2026-11-10T12:00:00Z" || o["last_check_at"] != "2026-11-10T12:00:00Z" || o["last_check_status"] != "success" {
		t.Errorf("after backup, prune and check: %v", o)
	}
	if o["last_status"] != "success" || o["last_success_at"] != "2026-11-10T12:00:00Z" {
		t.Errorf("backup status: %v", o)
	}

	// A check that fails is reported as such and leaves the rest alone.
	h.advance(time.Hour)
	h.enginesMu.Lock()
	h.engines["docs"] = &fake.Engine{Results: map[int][]fake.Outcome{0: {{Err: &model.KindError{Kind: model.KindConfig, Msg: "repository contains errors"}}}}}
	h.enginesMu.Unlock()
	if got := h.rec.final(t, h.runNow(task, map[string]any{"kind": "check"})); got.Status != model.StatusFailed {
		t.Fatalf("check = %s", got.Status)
	}
	o = overviewOf(t, h, id)
	if o["last_check_status"] != "failed" || o["last_check_at"] != "2026-11-10T12:00:00Z" {
		t.Errorf("after a failed check: %v", o)
	}
	if o["last_status"] != "success" || o["last_run_at"] != "2026-11-10T12:00:00Z" || o["last_success_at"] != "2026-11-10T12:00:00Z" {
		t.Errorf("a failed check changed the backup's own status: %v", o)
	}
}

func TestOverviewSyncHasNoMaintenance(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	o := overviewOf(t, h, task["id"].(string))
	if o["last_prune_at"] != nil || o["last_check_at"] != nil || o["last_check_status"] != "" {
		t.Errorf("sync task: %v", o)
	}
}
