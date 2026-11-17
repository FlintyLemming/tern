//go:build acceptance_host

package host

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const adminPassword = "acceptance admin password"

// scheduledSync makes the sync task that runs every minute: sftp push of
// sources/<name> to the remote's <name>-dst, with the key tern generated.
func scheduledSync(t *testing.T, s *stack, c *client, name string) (taskID, remote string) {
	t.Helper()
	keyID, pub := c.generatedKey()
	conn := s.sshConnection(c, keyID, pub)
	writeFiles(t, filepath.Join(s.dirs["sources"], name), map[string]string{"first.txt": "first\n"})
	taskID = c.task(map[string]any{"type": "sync", "name": name, "enabled": true, "schedule": "* * * * *",
		"retry": map[string]any{"retries": 0, "delays_sec": []int{1}},
		"sync": map[string]any{"protocol": "sftp", "connection_id": conn, "direction": "push",
			"pairs": []map[string]any{{"local": "/sources/" + name, "remote": "/srv/" + name + "-dst"}}}})
	return taskID, filepath.Join(s.dirs["remote"], name+"-dst")
}

// waitScheduledSuccess waits for a scheduled run of the task to succeed.
func waitScheduledSuccess(t *testing.T, c *client, taskID string) {
	t.Helper()
	ok := eventually(150*time.Second, func() bool {
		for _, r := range c.runs(taskID) {
			if r.Trigger == "schedule" && r.Status == "success" {
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("no scheduled run succeeded within 150 s: %+v", c.runs(taskID))
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Spec §16 item 7: a restart needs nobody: the schedule goes on by itself.
func TestRestartResumesSchedule(t *testing.T) {
	s := up(t, nil)
	c := s.api()
	c.setup(adminPassword)
	task, remote := scheduledSync(t, s, c, "resume")
	waitScheduledSuccess(t, c, task)

	writeFiles(t, filepath.Join(s.dirs["sources"], "resume"), map[string]string{"after-restart.txt": "second\n"})
	s.restart("tern")
	// From here on nothing logs in or unlocks: only the remote end is watched.
	if !eventually(150*time.Second, func() bool { return exists(filepath.Join(remote, "after-restart.txt")) }) {
		t.Fatalf("150 s after the restart the new file has not reached the remote end")
	}
	if st := s.api().status(); st["vault_state"] != "unlocked" {
		t.Errorf("vault_state = %v after a restart", st["vault_state"])
	}
}

// slowBackup makes a backup task whose run takes minutes: 64 MiB of
// incompressible data at 512 KiB/s.
func slowBackup(t *testing.T, s *stack, c *client) string {
	t.Helper()
	keyID, pub := c.generatedKey()
	conn := s.sshConnection(c, keyID, pub)
	src := filepath.Join(s.dirs["sources"], "slow")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	randomFile(t, filepath.Join(src, "blob.bin"), 64<<20)
	task := c.task(map[string]any{"type": "backup", "name": "slow", "enabled": true,
		"retry":         map[string]any{"retries": 0, "delays_sec": []int{1}},
		"backup":        map[string]any{"connection_id": conn, "repo_path": "/srv/slow-repo", "sources": []string{"/sources/slow"}, "limit_upload_kib": 512},
		"repo_password": "acceptance repository password", "recovery_confirmed": true})
	// Only the runs this test starts.
	var body map[string]any
	c.must(http.StatusOK, "GET", "/api/v1/tasks/"+task, nil, &body)
	b := body["backup"].(map[string]any)
	b["prune_every_days"], b["check_every_days"] = 0, 0
	c.must(http.StatusOK, "PUT", "/api/v1/tasks/"+task, body, nil)
	return task
}

// waitUploading waits until the run has processed some data: restic holds
// the repository lock by then.
func waitUploading(t *testing.T, c *client, taskID, runID string) {
	t.Helper()
	ok := eventually(2*time.Minute, func() bool {
		var ov struct {
			Tasks []struct {
				TaskID    string `json:"task_id"`
				ActiveRun *struct {
					RunID    string `json:"run_id"`
					Progress *struct {
						BytesDone int64 `json:"bytes_done"`
					} `json:"progress"`
				} `json:"active_run"`
			} `json:"tasks"`
		}
		c.must(http.StatusOK, "GET", "/api/v1/overview", nil, &ov)
		for _, it := range ov.Tasks {
			if it.TaskID == taskID && it.ActiveRun != nil && it.ActiveRun.RunID == runID && it.ActiveRun.Progress != nil && it.ActiveRun.Progress.BytesDone > 4<<20 {
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("the backup did not get going\n%s", c.runLog(runID))
	}
}

// Review focus 4: docker stop in the middle of a backup.
func TestStopMarksInterrupted(t *testing.T) {
	s := up(t, nil)
	c := s.api()
	c.setup(adminPassword)
	task := slowBackup(t, s, c)
	runID := c.startRun(task, nil)
	waitUploading(t, c, task, runID)

	s.stop("tern")
	s.start("tern")
	c = s.api()
	c.login(adminPassword)
	if r := c.waitRun(runID, time.Minute); r.Status != "interrupted" {
		t.Fatalf("the run cut by docker stop is %s, want interrupted\n%s", r.Status, c.runLog(runID))
	}
	c.wantRun(c.waitRun(c.startRun(task, nil), 10*time.Minute), "success")
}

// Review focus 4: the same with SIGKILL, so nothing could wind down.
func TestKillThenRecover(t *testing.T) {
	s := up(t, nil)
	c := s.api()
	c.setup(adminPassword)
	task := slowBackup(t, s, c)
	runID := c.startRun(task, nil)
	waitUploading(t, c, task, runID)

	s.kill("tern")
	s.start("tern")
	c = s.api()
	c.login(adminPassword)
	if r := c.waitRun(runID, time.Minute); r.Status != "interrupted" {
		t.Fatalf("the run cut by SIGKILL is %s, want interrupted\n%s", r.Status, c.runLog(runID))
	}
	if left := strings.TrimSpace(s.exec("tern", "ls", "-A", "/dev/shm/tern")); left != "" {
		t.Errorf("the runtime directory still holds the killed run's leftovers:\n%s", left)
	}
	c.wantRun(c.waitRun(c.startRun(task, nil), 10*time.Minute), "success")
}

// Key file mode with the key file gone: tern says so and makes no new one.
func TestKeyfileRemoved(t *testing.T) {
	s := up(t, nil)
	c := s.api()
	c.setup(adminPassword)
	key := filepath.Join(s.dirs["secrets"], "master.key")
	if !exists(key) {
		t.Fatalf("no %s after setup", key)
	}
	s.stop("tern")
	aside := filepath.Join(t.TempDir(), "master.key")
	if err := os.Rename(key, aside); err != nil {
		t.Fatal(err)
	}
	s.start("tern")
	if st := s.api().status(); st["vault_state"] != "keyfile_missing" {
		t.Errorf("vault_state = %v without the key file, want keyfile_missing", st["vault_state"])
	}
	if entries, _ := os.ReadDir(s.dirs["secrets"]); len(entries) != 0 {
		t.Errorf("tern wrote %d new entries into the secrets directory", len(entries))
	}

	s.stop("tern")
	if err := os.Rename(aside, key); err != nil {
		t.Fatal(err)
	}
	s.start("tern")
	if st := s.api().status(); st["vault_state"] != "unlocked" {
		t.Errorf("vault_state = %v with the key file back", st["vault_state"])
	}
}

// Review focus 5: BBR is reported as it is, and tern runs either way. With
// compose's sysctls line the container starts only on a kernel with BBR.
func TestBBRReported(t *testing.T) {
	type bbr struct {
		Available []string `json:"available"`
		Current   string   `json:"current"`
		Supported bool     `json:"supported"`
		Active    bool     `json:"active"`
	}
	system := func(c *client) bbr {
		var sys struct {
			BBR bbr `json:"bbr"`
		}
		c.must(http.StatusOK, "GET", "/api/v1/system", nil, &sys)
		return sys.BBR
	}

	// Without the sysctls line: healthy, and the report is consistent.
	s := up(t, nil)
	c := s.api()
	c.setup(adminPassword)
	b := system(c)
	t.Logf("without sysctls: %+v", b)
	if b.Active && !b.Supported {
		t.Errorf("active without being supported: %+v", b)
	}
	if b.Active != (b.Current == "bbr") || b.Current == "" {
		t.Errorf("active does not follow the current algorithm: %+v", b)
	}

	// With it.
	if !b.Supported {
		// This kernel has no tcp_bbr: spec §13 says the container cannot start.
		project := up2(t, s)
		if out, err := project.compose(3*time.Minute, "up", "-d", "--wait", "--wait-timeout", "60"); err == nil {
			t.Fatalf("the stack came up with net.ipv4.tcp_congestion_control=bbr on a kernel without BBR:\n%s", out)
		} else {
			t.Logf("as documented, the container does not start with the BBR sysctl here: %v\n%s", err, out)
		}
		return
	}
	s2 := up(t, nil, "compose.bbr.yaml")
	c2 := s2.api()
	c2.setup(adminPassword)
	if b := system(c2); !b.Active || !b.Supported || b.Current != "bbr" {
		t.Errorf("with the sysctls line: %+v, want bbr active", b)
	}
}

// up2 is a second project like s but with compose.bbr.yaml, not yet started.
func up2(t *testing.T, s *stack) *stack {
	t.Helper()
	s2 := &stack{t: t, project: s.project + "-bbr", env: s.env, dirs: s.dirs,
		files: append(append([]string{}, s.files...), filepath.Join(here(), "compose.bbr.yaml"))}
	// Its own directories: the first project still runs on s.dirs.
	root := t.TempDir()
	for _, d := range []string{"data", "secrets"} {
		p := filepath.Join(root, d)
		os.MkdirAll(p, 0o755)
		s2.env = append(s2.env, "TERN_ACC_"+strings.ToUpper(d)+"="+p)
	}
	t.Cleanup(func() { s2.compose(3*time.Minute, "down", "-v", "--remove-orphans", "-t", "5") })
	return s2
}
