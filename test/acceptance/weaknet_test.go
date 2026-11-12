//go:build acceptance

package acceptance

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tern/internal/model"
	kit "tern/test/testkit"
)

// Spec §16 item 4: 5% packet loss, and the link gone half way. The spec
// says a minute; this outage is two, longer than it takes to notice a dead
// link (SSH keepalive 15 s × 4, I/O timeouts 60 s), so the retry and resume
// paths really run. A one-minute outage is ridden out on the open
// connection: nothing is sent twice, but nothing is retried either (the
// user's decision of 2026-11-12, recorded in docs/acceptance.md).
const (
	loss       = "loss 5%"
	outage     = 120 * time.Second
	transferIn = 120 * time.Second // what the rate limits aim the transfers at
)

// weakRetry is the retry policy of these tasks: three retries, after 10, 20
// and 60 seconds instead of the default 1, 5 and 15 minutes.
var weakRetry = map[string]any{"retry": map[string]any{"retries": 3, "delays_sec": []int{10, 20, 60}}}

// limitKiB is the rate that moves total bytes in about transferIn.
func limitKiB(total int64) int64 { return total / 1024 / int64(transferIn/time.Second) }

type activeRun struct {
	RunID    string          `json:"run_id"`
	Status   model.Status    `json:"status"`
	Progress *model.Progress `json:"progress"`
}

// active is the overview's view of the task's run in progress, nil when
// the task is idle.
func active(a *kit.App, taskID string) *activeRun {
	var ov struct {
		Tasks []struct {
			TaskID    string     `json:"task_id"`
			ActiveRun *activeRun `json:"active_run"`
		} `json:"tasks"`
	}
	a.Must(http.StatusOK, "GET", "/api/v1/overview", nil, &ov)
	for _, it := range ov.Tasks {
		if it.TaskID == taskID {
			return it.ActiveRun
		}
	}
	return nil
}

// waitFraction waits until the run has done at least frac of its bytes.
func waitFraction(t *testing.T, a *kit.App, taskID, runID string, frac float64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		ar := active(a, taskID)
		if ar == nil || ar.RunID != runID {
			t.Fatalf("run %s ended before %.0f%% was done\n%s", runID, frac*100, a.RunLog(runID))
		}
		if p := ar.Progress; p != nil && p.BytesTotal > 0 && float64(p.BytesDone) >= frac*float64(p.BytesTotal) {
			t.Logf("%d of %d bytes done", p.BytesDone, p.BytesTotal)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach %.0f%% in 5 minutes\n%s", runID, frac*100, a.RunLog(runID))
}

// waitBytes waits until the run has done at least n bytes.
func waitBytes(t *testing.T, a *kit.App, taskID, runID string, n int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		ar := active(a, taskID)
		if ar == nil || ar.RunID != runID {
			t.Fatalf("run %s ended before %d bytes were done\n%s", runID, n, a.RunLog(runID))
		}
		if p := ar.Progress; p != nil && p.BytesDone >= n {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("run %s did not do %d bytes in 3 minutes\n%s", runID, n, a.RunLog(runID))
}

// waitStored waits until the repository at repoFS (as the runner sees it)
// holds at least n bytes of finished pack files.
func waitStored(t *testing.T, a *kit.App, taskID, runID, repoFS string, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if ar := active(a, taskID); ar == nil || ar.RunID != runID {
			t.Fatalf("run %s ended before %d bytes were stored\n%s", runID, n, a.RunLog(runID))
		}
		var stored int64
		filepath.Walk(filepath.Join(repoFS, "data"), func(_ string, fi os.FileInfo, err error) error {
			if err == nil && fi.Mode().IsRegular() && !strings.Contains(fi.Name(), "-restic-temp-") {
				stored += fi.Size()
			}
			return nil
		})
		if stored >= n {
			t.Logf("%d bytes stored in the repository", stored)
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("the repository did not reach %d bytes in 5 minutes\n%s", n, a.RunLog(runID))
}

// pidOf returns the PID of the one process called name; tests run one
// engine at a time.
func pidOf(t *testing.T, name string) string {
	t.Helper()
	out, _ := exec.Command("pgrep", "-x", name).Output()
	pids := strings.Fields(string(out))
	if len(pids) != 1 {
		t.Fatalf("pgrep -x %s: %q, want exactly one process", name, pids)
	}
	return pids[0]
}

func alive(pid string) bool { return exec.Command("kill", "-0", pid).Run() == nil }

// watchExit reports how long after its start the process pid was gone, or
// a negative duration if it was still there after limit.
func watchExit(pid string, limit time.Duration) <-chan time.Duration {
	ch := make(chan time.Duration, 1)
	began := time.Now()
	go func() {
		for time.Since(began) < limit {
			if !alive(pid) {
				ch <- time.Since(began)
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		ch <- -1
	}()
	return ch
}

// logBetween returns the part of a run log from the line containing from up
// to the one containing to.
func logBetween(log, from, to string) string {
	if i := strings.Index(log, from); i >= 0 {
		log = log[i:]
	}
	if i := strings.Index(log, to); i >= 0 {
		log = log[:i]
	}
	return log
}

// pushTree writes the source of a push: name → size.
func pushTree(t *testing.T, files map[string]int64) (dir string, total int64) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "src")
	for name, size := range files {
		kit.RandomFile(t, filepath.Join(dir, name), size)
		total += size
	}
	return dir, total
}

func manyFiles(n int, size int64, prefix string) map[string]int64 {
	files := map[string]int64{}
	for i := 0; i < n; i++ {
		files[fmt.Sprintf("%s%03d.bin", prefix, i)] = size
	}
	return files
}

// weakPush runs a push of files over protocol through loss and an outage
// once 30% is done, and returns the final run with the local source and
// the remote destination.
func weakPush(t *testing.T, protocol string, files map[string]int64) (a *kit.App, run model.Run, local, remote string, total int64) {
	t.Helper()
	a = kit.StartApp(t)
	local, total = pushTree(t, files)
	dst := kit.NewRemoteDir(t, protocol).Sub("dst")
	task := a.NewSyncTask(t, a.ConnFor(t, protocol), kit.SyncOpts{Protocol: protocol,
		Pairs: []model.FolderPair{{Local: local, Remote: dst.Task}},
		Spec:  map[string]any{"limit_kib": limitKiB(total)}, Task: weakRetry})

	netem(t, loss)
	runID := a.StartRun(t, task, nil)
	waitFraction(t, a, task, runID, 0.3)
	blackout(t, outage)
	run = a.WaitRun(t, runID)
	a.WaitIdle(t, task)
	t.Logf("%s: %s after %d attempts, last attempt %d bytes and %d files of %d bytes", protocol, run.Status, run.Attempts, run.Bytes, run.Files, total)
	return a, run, local, dst.FS, total
}

// Spec §16 item 4 for rsync: the big file goes on where it stopped.
func TestWeakNetRsync(t *testing.T) {
	files := manyFiles(200, 100<<10, "small/f")
	files["big.bin"] = 200 << 20 // sorts first: the outage hits it
	a, run, local, remote, total := weakPush(t, "rsync", files)
	a.WantRun(t, run, model.StatusSuccess)
	if run.Attempts < 2 {
		t.Errorf("attempts = %d, want at least 2: the outage was not noticed\n%s", run.Attempts, a.RunLog(run.ID))
	}
	kit.SameTree(t, local, remote)
	if run.Bytes >= total*8/10 {
		t.Errorf("the last attempt sent %d of %d bytes: the big file was sent again, not resumed\n%s", run.Bytes, total, a.RunLog(run.ID))
	}
}

// Spec §16 item 4 for SFTP: files done before the outage are not sent again.
func TestWeakNetSFTP(t *testing.T) {
	a, run, local, remote, _ := weakPush(t, "sftp", manyFiles(300, 1<<20, "f"))
	a.WantRun(t, run, model.StatusSuccess)
	if run.Attempts < 2 {
		t.Errorf("attempts = %d, want at least 2: the outage was not noticed\n%s", run.Attempts, a.RunLog(run.ID))
	}
	kit.SameTree(t, local, remote)
	if run.Files >= 300 {
		t.Errorf("the last attempt sent %d of 300 files: completed files were sent again\n%s", run.Files, a.RunLog(run.ID))
	}
}

func TestWeakNetWebDAV(t *testing.T) {
	a, run, local, remote, _ := weakPush(t, "webdav", manyFiles(300, 1<<20, "f"))
	a.WantRun(t, run, model.StatusSuccess)
	kit.SameTree(t, local, remote)
}

// Spec §16 item 4 for backups, and spec §15's open question: how restic's
// SFTP backend behaves when the SSH connection dies under it.
func TestWeakNetRestic(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src, total := pushTree(t, manyFiles(20, 10<<20, "blob"))
	task, repo := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{
		Spec: map[string]any{"limit_upload_kib": limitKiB(total)}, Task: weakRetry})

	netem(t, loss)
	runID := a.StartRun(t, task, nil)
	// restic reads far ahead of what it has uploaded: count what is stored.
	waitStored(t, a, task, runID, kit.RemoteFS(t, repo), total*3/10)
	exited := watchExit(pidOf(t, "restic"), 5*time.Minute)
	blackout(t, outage)
	run := a.WaitRun(t, runID)
	a.WaitIdle(t, task)
	log := a.RunLog(run.ID)

	gone := <-exited
	t.Logf("restic over SFTP, link down for %s: the process exited %s after the outage began (negative: not within 5 minutes)", outage, gone.Round(time.Second))
	t.Logf("the attempt the outage hit:\n%s", logBetween(log, "attempt 1", "attempt 2"))
	t.Logf("%s after %d attempts; the last attempt added %d of %d bytes", run.Status, run.Attempts, run.Bytes, total)

	a.WantRun(t, run, model.StatusSuccess)
	if run.Attempts < 2 {
		t.Errorf("attempts = %d, want at least 2: the outage was not noticed\n%s", run.Attempts, log)
	}
	if run.Bytes >= total*8/10 {
		t.Errorf("the last attempt added %d of %d bytes: what was uploaded before the outage was uploaded again\n%s", run.Bytes, total, log)
	}
	// What the cut-off backup uploaded is indexed before the next backup
	// (at the end of the failed attempt, or at the start of the next one
	// when the link is still down then).
	firstBackup, lastBackup := strings.Index(log, "$ restic backup"), strings.LastIndex(log, "$ restic backup")
	if repair := strings.Index(log, "$ restic repair index"); repair < firstBackup || repair > lastBackup {
		t.Errorf("no `restic repair index` between the cut-off backup and the one that succeeded\n%s", log)
	}
	a.Native(t, task, conn).OK("check")
}

// Spec §8.2: a dead link is noticed within about a minute (SSH keepalive
// 15 s × 4, rsync and rclone I/O timeouts 60 s). rsync and restic then give
// up: their process is gone within two minutes. rclone notices it as fast
// (the transfers in flight fail with "connection lost"), but spec §8.3 keeps
// its operation-level retries, so it goes on trying the files left, each
// with a new ssh that waits out its connect timeout; for rclone the test
// asks for the failed transfers in the run log within two minutes instead.
func TestDisconnectDetectedInTime(t *testing.T) {
	cases := []struct {
		name, process string
		noticedIn     string // the run log line that says the link died, "" to wait for the process to exit
		start         func(t *testing.T, a *kit.App, src string, limit int64) (taskID, runID string)
	}{
		{"rsync", "rsync", "", syncStarter("rsync")},
		{"sftp", "rclone", "connection lost", syncStarter("sftp")},
		{"restic", "restic", "", func(t *testing.T, a *kit.App, src string, limit int64) (string, string) {
			task, _ := a.NewBackupTask(t, a.SSHConnection(t), []string{src}, kit.BackupOpts{
				Spec: map[string]any{"limit_upload_kib": limit}, Task: map[string]any{"retry": map[string]any{"retries": 0, "delays_sec": []int{1}}}})
			return task, a.StartRun(t, task, nil)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := kit.StartApp(t)
			src, _ := pushTree(t, manyFiles(10, 5<<20, "f"))
			task, runID := c.start(t, a, src, 512)
			waitBytes(t, a, task, runID, 4<<20)
			var noticed <-chan time.Duration
			if c.noticedIn == "" {
				noticed = watchExit(pidOf(t, c.process), 2*time.Minute)
			} else {
				noticed = watchLog(a, runID, c.noticedIn, 2*time.Minute)
			}
			blackout(t, 2*time.Minute+5*time.Second) // down for the whole window
			switch took := <-noticed; {
			case took < 0 && c.noticedIn == "":
				t.Errorf("%s was still running 2 minutes into the outage\n%s", c.process, a.RunLog(runID))
			case took < 0:
				t.Errorf("2 minutes into the outage the run log does not say %q\n%s", c.noticedIn, a.RunLog(runID))
			case c.noticedIn == "":
				t.Logf("%s exited %s after the link went down", c.process, took.Round(time.Second))
			default:
				t.Logf("%s reported %q %s after the link went down", c.process, c.noticedIn, took.Round(time.Second))
			}
			a.WaitRun(t, runID)
			a.WaitIdle(t, task)
		})
	}
}

// watchLog reports how long after its start the run log contained want,
// or a negative duration if it did not within limit.
func watchLog(a *kit.App, runID, want string, limit time.Duration) <-chan time.Duration {
	ch := make(chan time.Duration, 1)
	began := time.Now()
	go func() {
		for time.Since(began) < limit {
			// The file itself: RunLog may fail the test, which only the
			// test's goroutine may do.
			if raw, err := os.ReadFile(filepath.Join(a.DataDir, "logs", runID+".log")); err == nil && strings.Contains(string(raw), want) {
				ch <- time.Since(began)
				return
			}
			time.Sleep(time.Second)
		}
		ch <- -1
	}()
	return ch
}

func syncStarter(protocol string) func(t *testing.T, a *kit.App, src string, limit int64) (string, string) {
	return func(t *testing.T, a *kit.App, src string, limit int64) (string, string) {
		dst := kit.NewRemoteDir(t, protocol).Sub("dst")
		task := a.NewSyncTask(t, a.ConnFor(t, protocol), kit.SyncOpts{Protocol: protocol,
			Pairs: []model.FolderPair{{Local: src, Remote: dst.Task}}, Spec: map[string]any{"limit_kib": limit}})
		return task, a.StartRun(t, task, nil)
	}
}

// Pulls through a lossy link arrive complete.
func TestPullUnderLoss(t *testing.T) {
	for _, protocol := range []string{"rsync", "sftp"} {
		t.Run(protocol, func(t *testing.T) {
			a := kit.StartApp(t)
			src := kit.NewRemoteDir(t, protocol).Sub("src")
			for name, size := range manyFiles(50, 1<<20, "f") {
				kit.RandomFile(t, filepath.Join(src.FS, name), size)
			}
			local := kit.MountedDir(t) // a pull lands in an existing directory on a volume
			task := a.NewSyncTask(t, a.ConnFor(t, protocol), kit.SyncOpts{Protocol: protocol, Direction: "pull",
				Pairs: []model.FolderPair{{Local: local, Remote: src.Task}}})
			netem(t, loss)
			a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
			kit.SameTree(t, src.FS, local)
		})
	}
}

// Spec §8.4: while a run waits for its retry the overview says so.
func TestRetryWaitVisible(t *testing.T) {
	a := kit.StartApp(t)
	src, _ := pushTree(t, manyFiles(10, 5<<20, "f"))
	dst := kit.NewRemoteDir(t, "rsync").Sub("dst")
	task := a.NewSyncTask(t, a.SSHConnection(t), kit.SyncOpts{Protocol: "rsync",
		Pairs: []model.FolderPair{{Local: src, Remote: dst.Task}}, Spec: map[string]any{"limit_kib": 512}, Task: weakRetry})
	runID := a.StartRun(t, task, nil)
	waitBytes(t, a, task, runID, 4<<20)

	var mu sync.Mutex
	seen := map[model.Status]bool{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
			var ov struct {
				Tasks []struct {
					TaskID    string     `json:"task_id"`
					ActiveRun *activeRun `json:"active_run"`
				} `json:"tasks"`
			}
			if code, err := a.Try("GET", "/api/v1/overview", nil, &ov); err != nil || code != http.StatusOK {
				continue // the next poll will do
			}
			for _, it := range ov.Tasks {
				if it.TaskID == task && it.ActiveRun != nil {
					mu.Lock()
					seen[it.ActiveRun.Status] = true
					mu.Unlock()
				}
			}
		}
	}()
	// Long enough to be noticed (about a minute) and to wait 10 seconds.
	blackout(t, 2*time.Minute)
	close(stop)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if !seen[model.StatusRetryWait] {
		t.Errorf("the overview never showed retry_wait during the outage; it showed %v\n%s", seen, a.RunLog(runID))
	}
	a.Must(http.StatusAccepted, "POST", "/api/v1/runs/"+runID+"/cancel", nil, nil)
	a.WaitRun(t, runID)
	a.WaitIdle(t, task)
}
