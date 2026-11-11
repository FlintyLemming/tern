package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tern/internal/engine/restic/restictest"
)

// snapshotsJSON is what `restic snapshots --json` prints for n snapshots
// taken an hour apart, oldest first, all on one line.
func snapshotsJSON(n int, taskID string) string {
	var b strings.Builder
	b.WriteString("[")
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		tags := `["tern-` + taskID + `"]`
		if i%2 == 1 {
			tags = "null"
		}
		fmt.Fprintf(&b, `{"time":%q,"paths":["/mnt/docs"],"hostname":"nas-1","tags":%s,"id":"%064x","short_id":"%08x"}`,
			base.Add(time.Duration(i)*time.Hour).Format(time.RFC3339), tags, i, i)
	}
	b.WriteString("]")
	return b.String()
}

func TestSnapshotsCapped(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	id := task["id"].(string)
	h.restic.Add(restictest.Step{Stdout: snapshotsJSON(3000, id)})
	got := wantStatus(t, h.do("GET", taskPath(task)+"/snapshots", nil), http.StatusOK)
	snaps := got["snapshots"].([]any)
	if len(snaps) != 200 || got["total"] != float64(3000) {
		t.Fatalf("%d snapshots, total %v; want 200 of 3000", len(snaps), got["total"])
	}
	first, last := snaps[0].(map[string]any), snaps[199].(map[string]any)
	if first["short_id"] != fmt.Sprintf("%08x", 2999) || last["short_id"] != fmt.Sprintf("%08x", 2800) {
		t.Errorf("first %v, last %v; want the newest 200, newest first", first["short_id"], last["short_id"])
	}
	want := map[string]any{
		"id": fmt.Sprintf("%064x", 2999), "short_id": fmt.Sprintf("%08x", 2999), "time": "2020-05-04T23:00:00Z",
		"host": "nas-1", "paths": []any{"/mnt/docs"}, "tags": []any{}, "mine": false,
	}
	if !reflect.DeepEqual(first, want) {
		t.Errorf("first snapshot = %v\nwant %v", first, want)
	}
	if second := snaps[1].(map[string]any); second["mine"] != true || !reflect.DeepEqual(second["tags"], []any{"tern-" + id}) {
		t.Errorf("second snapshot = %v", second)
	}
	calls := h.restic.Calls()
	if c := calls[len(calls)-1]; !c.Has("snapshots", "--json") || c.Password != repoPass || c.PasswordMode != 0o600 {
		t.Errorf("call = %q with password %q", c.Args, c.Password)
	}
	if left := h.runtimeLeftovers(); len(left) != 0 {
		t.Errorf("runtime directory still holds %q", left)
	}
}

func TestSnapshotsEmpty(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	h.restic.Add(restictest.Step{Stdout: "[]"})
	w := h.do("GET", taskPath(task)+"/snapshots", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"snapshots":[]`) || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Errorf("GET snapshots = %d %s", w.Code, w.Body)
	}
}

func TestSnapshotsRepository(t *testing.T) {
	h := readyHarness(t)
	body := backupBody("docs", h.nasConnID())
	body["backup"].(map[string]any)["repo_path"] = "/srv/repo"
	task := h.newTask(body)
	h.restic.Add(restictest.Step{Stdout: "[]"})
	got := wantStatus(t, h.do("GET", taskPath(task)+"/snapshots", nil), http.StatusOK)
	if got["repository"] != "sftp:backup@nas2.example:/srv/repo" {
		t.Errorf("repository = %v", got["repository"])
	}
}

func TestSnapshotsErrors(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	h.restic.Add(restictest.Step{Exit: 1, Stderr: "ssh: connect to host nas2 port 22: Connection refused\n"}, restictest.Step{Exit: 12})
	wantError(t, h.do("GET", taskPath(task)+"/snapshots", nil), http.StatusBadGateway, "unreachable")
	wantError(t, h.do("GET", taskPath(task)+"/snapshots", nil), http.StatusBadRequest, "wrong_repo_password")
	wantError(t, h.do("GET", taskPath(h.newSyncTask("photos"))+"/snapshots", nil), http.StatusBadRequest, "validation")
	wantError(t, h.do("GET", "/api/v1/tasks/nope/snapshots", nil), http.StatusNotFound, "not_found")
}

func restoreBody(target string) map[string]any {
	return map[string]any{"snapshot_id": "abcd1234", "target_dir": target}
}

func TestRestoreBadTarget(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	full := t.TempDir()
	os.WriteFile(filepath.Join(full, "leftover"), nil, 0o644)
	for target, code := range map[string]string{
		full:                                 "not_empty",
		"relative":                           "not_absolute",
		"/mnt/docs/restored":                 "inside_source",
		filepath.Join(h.dataDir, "restored"): "inside_data",
		filepath.Join(h.secrets, "restored"): "inside_data",
	} {
		e := wantError(t, h.do("POST", taskPath(task)+"/restore", restoreBody(target)), http.StatusBadRequest, "validation")
		if got := e["fields"].(map[string]any)["target_dir"]; got != code {
			t.Errorf("target %q: fields = %v, want %s", target, e["fields"], code)
		}
	}
	h.notMounted = true
	e := wantError(t, h.do("POST", taskPath(task)+"/restore", restoreBody(filepath.Join(t.TempDir(), "new"))), http.StatusBadRequest, "validation")
	if got := e["fields"].(map[string]any)["target_dir"]; got != "not_mounted" {
		t.Errorf("fields = %v, want not_mounted", e["fields"])
	}
	if n := h.count("runs"); n != 0 {
		t.Errorf("%d runs recorded for refused restores", n)
	}
}

func TestRestoreBadParams(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	target := filepath.Join(t.TempDir(), "new")
	for field, body := range map[string]map[string]any{
		"snapshot_id": {"snapshot_id": "latest", "target_dir": target},
		"subpath":     {"snapshot_id": "abcd1234", "subpath": "photos", "target_dir": target},
		"target_dir":  {"snapshot_id": "abcd1234"},
	} {
		wantField(t, wantError(t, h.do("POST", taskPath(task)+"/restore", body), http.StatusBadRequest, "validation"), field)
	}
	if n := h.count("runs"); n != 0 {
		t.Errorf("%d runs recorded", n)
	}
}

func TestRestoreQueued(t *testing.T) {
	h := readyHarness(t)
	trace := filepath.Join(t.TempDir(), "trace")
	body := backupBody("docs", h.sshConnID())
	body["pre"] = map[string]any{"script": "echo pre >> " + trace}
	body["post"] = map[string]any{"script": "echo post >> " + trace}
	task := h.newTask(body)
	target := filepath.Join(t.TempDir(), "restore-1")
	req := restoreBody(target)
	req["subpath"] = "/mnt/docs/2024"
	runID := wantStatus(t, h.do("POST", taskPath(task)+"/restore", req), http.StatusAccepted)["run_id"].(string)
	h.rec.final(t, runID)
	run := h.getRun(runID)
	if run["kind"] != "restore" || run["trigger"] != "manual" || run["status"] != "success" {
		t.Errorf("run = %v", run)
	}
	if _, err := os.Stat(trace); err == nil {
		t.Error("the task's pre- or post-task command ran for a restore")
	}
	built := h.engine("docs").Built()
	want := map[string]string{"snapshot_id": "abcd1234", "subpath": "/mnt/docs/2024", "target_dir": target}
	if len(built) != 1 || !reflect.DeepEqual(built[0].Params, want) {
		t.Errorf("engine built with %+v, want params %v", built, want)
	}
	// A restore is not a backup: no maintenance follows, nothing "succeeded".
	h.settle(task)
	if n := h.count("runs"); n != 1 {
		t.Errorf("%d runs, want only the restore", n)
	}
	if o := overviewOf(t, h, task["id"].(string)); o["last_success_at"] != nil || o["last_status"] != "" {
		t.Errorf("overview after a restore: %v", o)
	}
}

func TestRestoreWhileActive(t *testing.T) {
	h := readyHarness(t)
	task := h.newTask(backupBody("docs", h.sshConnID()))
	h.noMaintenance(task)
	eng := h.blocked("docs")
	id := h.runNow(task, nil)
	wantError(t, h.do("POST", taskPath(task)+"/restore", restoreBody(filepath.Join(t.TempDir(), "new"))), http.StatusConflict, "task_active")
	close(eng.Block)
	h.rec.final(t, id)
}

func TestRestoreOnSyncTask(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	wantError(t, h.do("POST", taskPath(task)+"/restore", restoreBody(filepath.Join(t.TempDir(), "new"))), http.StatusBadRequest, "validation")
	if n := h.count("runs"); n != 0 {
		t.Errorf("%d runs recorded", n)
	}
}
