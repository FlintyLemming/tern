package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tern/internal/access"
	"tern/internal/engine/faketool"
	"tern/internal/engine/syncengine"
	"tern/internal/model"
)

// realSync makes sync runs use the real sync engines, over a fake rclone
// and a fake rsync.
func (h *harness) realSync() (rclone, rsync *faketool.Fake) {
	h.t.Helper()
	rclone, rsync = faketool.New(h.t, "rclone"), faketool.New(h.t, "rsync")
	h.reg.Register(model.TaskSync, syncengine.NewBuilder(syncengine.Deps{
		Access:    harnessAccess{h: h, real: access.Opener{Store: h.st, Vault: h.vault}},
		Store:     h.st,
		RcloneBin: rclone.Bin,
		RsyncBin:  rsync.Bin,
	}))
	return rclone, rsync
}

// sourceDirs makes n local directories with a file in each.
func sourceDirs(t *testing.T, n int) []string {
	t.Helper()
	var dirs []string
	for i := 0; i < n; i++ {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	return dirs
}

// pushBody is a push task syncing each local directory to /srv/<i>.
func pushBody(name, protocol, connID, mode string, locals []string) map[string]any {
	var pairs []map[string]any
	for i, l := range locals {
		pairs = append(pairs, map[string]any{"local": l, "remote": fmt.Sprintf("/srv/%d", i)})
	}
	return map[string]any{"type": "sync", "name": name, "enabled": true, "sync": map[string]any{
		"protocol": protocol, "connection_id": connID, "direction": "push", "delete_mode": mode, "pairs": pairs}}
}

// dryRunDeletes is rclone's dry-run output for n files to delete.
func dryRunDeletes(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `{"level":"notice","msg":"Skipped delete as --dry-run is set (size 1)","skipped":"delete","size":1,"object":"old/%d.txt"}`+"\n", i)
	}
	return b.String()
}

func units(t *testing.T, run map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, u := range run["units"].([]any) {
		out = append(out, u.(map[string]any))
	}
	return out
}

func TestSyncTaskEndToEnd(t *testing.T) {
	h := readyHarness(t)
	rclone, _ := h.realSync()
	task := h.newTask(pushBody("photos", "sftp", h.sshConnID(), "keep", sourceDirs(t, 2)))
	id := h.runNow(task, nil)
	h.rec.final(t, id)
	run := h.getRun(id)
	if run["status"] != "success" || len(units(t, run)) != 2 {
		t.Fatalf("run = %v\nlog:\n%s", run, h.readLog(id, 0)["data"])
	}
	calls := rclone.Calls()
	if len(calls) != 2 || calls[0].Args[0] != "copy" || calls[1].Args[2] != "remote:/srv/1" {
		t.Errorf("rclone calls = %+v", calls)
	}
}

func TestDryRunViaAPI(t *testing.T) {
	h := readyHarness(t)
	rclone, _ := h.realSync()
	task := h.newTask(pushBody("photos", "sftp", h.sshConnID(), "mirror", sourceDirs(t, 2)))
	id := h.runNow(task, map[string]any{"kind": "dry_run"})
	h.rec.final(t, id)
	run := h.getRun(id)
	if run["status"] != "success" || run["kind"] != "dry_run" {
		t.Fatalf("run = %v", run)
	}
	if log := h.readLog(id, 0)["data"].(string); !strings.Contains(log, "plan: 0 to transfer, 0 to delete") {
		t.Errorf("log:\n%s", log)
	}
	calls := rclone.Calls()
	if len(calls) != 2 {
		t.Fatalf("rclone calls = %+v", calls)
	}
	for _, c := range calls {
		if !slices.Contains(c.Args, "--dry-run") {
			t.Errorf("call without --dry-run: %q", c.Args)
		}
	}
}

func TestGuardSurfaces(t *testing.T) {
	h := readyHarness(t)
	rclone, _ := h.realSync()
	rclone.Add(faketool.Step{Stderr: dryRunDeletes(101)})
	task := h.newTask(pushBody("photos", "sftp", h.sshConnID(), "mirror", sourceDirs(t, 1)))
	id := h.runNow(task, nil)
	h.rec.final(t, id)
	run := h.getRun(id)
	if run["status"] != "failed" || run["error_kind"] != "guard" || !strings.Contains(run["error_summary"].(string), "101") ||
		run["attempts"] != float64(1) {
		t.Errorf("run = %v", run)
	}
	if n := len(rclone.Calls()); n != 1 {
		t.Errorf("%d rclone calls, want the dry run only", n)
	}
}

func TestAllowDeletesOnceViaAPI(t *testing.T) {
	h := readyHarness(t)
	rclone, _ := h.realSync()
	rclone.Add(faketool.Step{Stderr: dryRunDeletes(101)}, faketool.Step{}, faketool.Step{Stderr: dryRunDeletes(101)})
	task := h.newTask(pushBody("photos", "sftp", h.sshConnID(), "mirror", sourceDirs(t, 1)))

	id := h.runNow(task, map[string]any{"allow_deletes_once": true})
	h.rec.final(t, id)
	if run := h.getRun(id); run["status"] != "success" {
		t.Fatalf("run with allow_deletes_once = %v", run)
	}
	calls := rclone.Calls()
	if len(calls) != 2 || !calls[1].Has("--max-delete", "101") || slices.Contains(calls[1].Args, "--dry-run") {
		t.Fatalf("rclone calls = %+v", calls)
	}

	id = h.runNow(task, nil)
	h.rec.final(t, id)
	if run := h.getRun(id); run["status"] != "failed" || run["error_kind"] != "guard" {
		t.Errorf("next run = %v", run)
	}
	if n := len(rclone.Calls()); n != 3 {
		t.Errorf("%d rclone calls, want 3", n)
	}
}

func TestPartialAcrossPairs(t *testing.T) {
	h := readyHarness(t)
	_, rsync := h.realSync()
	rsync.Add(faketool.Step{Exit: 23, Stderr: "rsync: [sender] send_files failed to open \"/x/a.txt\": Permission denied (13)\n"}, faketool.Step{})
	task := h.newTask(pushBody("photos", "rsync", h.sshConnID(), "keep", sourceDirs(t, 2)))
	id := h.runNow(task, nil)
	h.rec.final(t, id)
	run := h.getRun(id)
	us := units(t, run)
	if run["status"] != "partial" || len(us) != 2 || us[0]["status"] != "failed" || us[1]["status"] != "success" || run["attempts"] != float64(1) {
		t.Errorf("run = %v", run)
	}
}

func TestCommandLineAPI(t *testing.T) {
	h := readyHarness(t)
	h.realSync()
	task := h.newTask(pushBody("photos", "rsync", h.sshConnID(), "keep", sourceDirs(t, 3)))
	out := wantStatus(t, h.do("GET", taskPath(task)+"/command-line", nil), http.StatusOK)
	lines := out["lines"].([]any)
	if len(lines) != 3 || !strings.HasPrefix(lines[0].(string), "rsync -aH ") {
		t.Errorf("lines = %q", lines)
	}
}
