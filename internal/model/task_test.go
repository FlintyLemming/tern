package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func syncTask() Task {
	t := Task{
		Type: TaskSync, Name: "photos", Enabled: true, Schedule: "0 3 * * *",
		Sync: &SyncSpec{
			Protocol: "sftp", ConnectionID: "c1", Direction: "push",
			Pairs: []FolderPair{{Local: "/mnt/photos", Remote: "backup/photos"}},
		},
	}
	t.ApplyDefaults()
	return t
}

func backupTask() Task {
	t := Task{
		Type: TaskBackup, Name: "documents", Enabled: true, Schedule: "30 2 * * 1-5",
		Pre:  Hook{Script: "echo stop", Where: "ssh", ConnectionID: "c2"},
		Post: Hook{Script: "echo start"},
		Backup: &BackupSpec{
			ConnectionID: "c1", RepoPath: "/srv/restic/docs", Host: "nas-1",
			Sources: []string{"/mnt/docs"}, Excludes: []string{"*.tmp"},
			Keep: KeepPolicy{Last: 3, Daily: 7},
		},
	}
	t.ApplyDefaults()
	return t
}

func TestApplyDefaults(t *testing.T) {
	task := Task{Type: TaskSync, Sync: &SyncSpec{Protocol: "sftp"}}
	task.ApplyDefaults()
	if !reflect.DeepEqual(task.Retry, RetryPolicy{Retries: 3, DelaysSec: []int{60, 300, 900}}) {
		t.Errorf("Retry = %+v", task.Retry)
	}
	s := task.Sync
	if s.DeleteMode != "keep" || s.MaxDeletes != 100 || s.Compare != "size_mtime" || s.Transfers != 4 {
		t.Errorf("Sync = %+v", s)
	}
	for name, h := range map[string]Hook{"pre": task.Pre, "post": task.Post} {
		if h.TimeoutSec != 600 || h.Where != "local" {
			t.Errorf("%s hook = %+v", name, h)
		}
	}
}

func TestApplyDefaultsBackup(t *testing.T) {
	task := Task{Type: TaskBackup, Backup: &BackupSpec{}}
	task.ApplyDefaults()
	if task.Backup.Connections != 5 {
		t.Errorf("Connections = %d, want 5", task.Backup.Connections)
	}
}

func TestDefaultsRsyncNoTransfers(t *testing.T) {
	task := Task{Type: TaskSync, Sync: &SyncSpec{Protocol: "rsync"}}
	task.ApplyDefaults()
	if task.Sync.Transfers != 0 {
		t.Errorf("Transfers = %d, want 0", task.Sync.Transfers)
	}
}

func TestRetryZeroRespected(t *testing.T) {
	task := Task{Retry: RetryPolicy{Retries: 0, DelaysSec: []int{60}}}
	task.ApplyDefaults()
	if !reflect.DeepEqual(task.Retry, RetryPolicy{Retries: 0, DelaysSec: []int{60}}) {
		t.Errorf("Retry = %+v", task.Retry)
	}
}

func TestValidateOK(t *testing.T) {
	for _, task := range []Task{syncTask(), backupTask()} {
		if f := task.Validate(); len(f) != 0 {
			t.Errorf("%s: fields = %v, want none", task.Name, f)
		}
	}
}

func TestValidateRsyncTransfers(t *testing.T) {
	task := syncTask()
	task.Sync.Protocol, task.Sync.Transfers = "rsync", 4
	if _, ok := task.Validate()["sync.transfers"]; !ok {
		t.Errorf("fields = %v", task.Validate())
	}
}

func TestValidatePaths(t *testing.T) {
	for _, p := range []string{"relative/x", "/a/../b", "/a\nb", ""} {
		task := syncTask()
		task.Sync.Pairs[0].Local = p
		if _, ok := task.Validate()["sync.pairs[0].local"]; !ok {
			t.Errorf("%q: fields = %v", p, task.Validate())
		}
	}
	task := syncTask()
	task.Sync.Pairs = append(task.Sync.Pairs, FolderPair{Local: "/ok", Remote: "a\x00b"})
	if _, ok := task.Validate()["sync.pairs[1].remote"]; !ok {
		t.Errorf("fields = %v", task.Validate())
	}
	b := backupTask()
	b.Backup.Sources = []string{"/ok", "not/abs"}
	if _, ok := b.Validate()["backup.sources[1]"]; !ok {
		t.Errorf("fields = %v", b.Validate())
	}
}

func TestValidateSchedule(t *testing.T) {
	cases := map[string]string{
		"0 3 * * *":    "",
		"":             "",
		"nope":         "invalid",
		"@daily":       "invalid",
		"0 0 3 * * *":  "invalid",
		"0 0 31 2 *":   "never_fires",
		"TZ=UTC 0 3 *": "invalid",
	}
	for expr, want := range cases {
		task := syncTask()
		task.Schedule = expr
		if got := task.Validate()["schedule"]; got != want {
			t.Errorf("%q: schedule = %q, want %q", expr, got, want)
		}
	}
}

func TestValidateHook(t *testing.T) {
	task := syncTask()
	task.Pre = Hook{Script: "true", Where: "ssh", TimeoutSec: 600}
	if _, ok := task.Validate()["pre.connection_id"]; !ok {
		t.Errorf("fields = %v", task.Validate())
	}
	task = syncTask()
	task.Post.TimeoutSec = 86401
	if _, ok := task.Validate()["post.timeout_sec"]; !ok {
		t.Errorf("fields = %v", task.Validate())
	}
}

func TestValidateMisc(t *testing.T) {
	cases := map[string]func(*Task){
		"name":                func(t *Task) { t.Name = "" },
		"type":                func(t *Task) { t.Backup = &BackupSpec{} },
		"sync.protocol":       func(t *Task) { t.Sync.Protocol = "ftp" },
		"sync.direction":      func(t *Task) { t.Sync.Direction = "both" },
		"sync.delete_mode":    func(t *Task) { t.Sync.DeleteMode = "wipe" },
		"sync.compare":        func(t *Task) { t.Sync.Compare = "magic" },
		"sync.pairs":          func(t *Task) { t.Sync.Pairs = nil },
		"sync.max_deletes":    func(t *Task) { t.Sync.MaxDeletes = -1 },
		"sync.connection_id":  func(t *Task) { t.Sync.ConnectionID = "" },
		"retry.retries":       func(t *Task) { t.Retry.Retries = 11 },
		"retry.delays_sec[1]": func(t *Task) { t.Retry.DelaysSec = []int{60, 0} },
		"max_runtime_sec":     func(t *Task) { t.MaxRuntimeSec = -1 },
	}
	for field, mutate := range cases {
		task := syncTask()
		mutate(&task)
		if _, ok := task.Validate()[field]; !ok {
			t.Errorf("want %q in %v", field, task.Validate())
		}
	}
	b := backupTask()
	b.Backup.Sources, b.Backup.RepoPath = nil, ""
	f := b.Validate()
	if f["backup.sources"] == "" || f["backup.repo_path"] == "" {
		t.Errorf("fields = %v", f)
	}
}

func TestConnectionIDs(t *testing.T) {
	task := backupTask() // main c1, pre hook c2
	task.Post = Hook{Script: "x", Where: "ssh", ConnectionID: "c1"}
	if got := task.ConnectionIDs(); !reflect.DeepEqual(got, []string{"c1", "c2"}) {
		t.Errorf("ConnectionIDs = %v", got)
	}
}

func TestStatusTerminal(t *testing.T) {
	for _, s := range []Status{StatusSuccess, StatusWarning, StatusPartial, StatusFailed, StatusCanceled, StatusSkipped, StatusInterrupted} {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range []Status{StatusQueued, StatusRunning, StatusRetryWait} {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
}

func jsonKeys(v any, into map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			into[k] = true
			jsonKeys(c, into)
		}
	case []any:
		for _, c := range x {
			jsonKeys(c, into)
		}
	}
}

func TestTaskJSONHasNoSecrets(t *testing.T) {
	for _, task := range []Task{syncTask(), backupTask()} {
		raw, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		json.Unmarshal(raw, &v)
		keys := map[string]bool{}
		jsonKeys(v, keys)
		if !keys["name"] || !keys["created_at"] || !keys["delays_sec"] {
			t.Fatalf("keys are not snake_case: %v", keys)
		}
		for k := range keys {
			for _, bad := range []string{"password", "private", "secret"} {
				if strings.Contains(strings.ToLower(k), bad) {
					t.Errorf("task JSON has key %q", k)
				}
			}
			if k != strings.ToLower(k) {
				t.Errorf("key %q is not snake_case", k)
			}
		}
	}
}

func TestRunJSON(t *testing.T) {
	raw, _ := json.Marshal(Run{ID: "r1", Status: StatusQueued})
	var m map[string]any
	json.Unmarshal(raw, &m)
	if m["finished_at"] != nil {
		t.Errorf("finished_at of an unfinished run = %v, want null", m["finished_at"])
	}
	if u, ok := m["units"].([]any); !ok || len(u) != 0 {
		t.Errorf("units = %v, want []", m["units"])
	}
	if m["status"] != "queued" || m["task_id"] != "" {
		t.Errorf("run JSON = %s", raw)
	}
}

func TestTaskValidateExcludes(t *testing.T) {
	s := syncTask()
	s.Sync.Excludes = []string{"a/b", "*.tmp"}
	if f := s.Validate(); f["sync.excludes[0]"] != "invalid" || len(f) != 1 {
		t.Errorf("sync fields = %v", f)
	}
	b := backupTask()
	b.Backup.Excludes = []string{"*.tmp", "!keep"}
	if f := b.Validate(); f["backup.excludes[1]"] != "invalid" || len(f) != 1 {
		t.Errorf("backup fields = %v", f)
	}
}
