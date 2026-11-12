//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tern/internal/model"
	kit "tern/test/testkit"
)

func sampleTree(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "photos")
	kit.WriteTree(t, src, map[string]string{
		"a.txt":            "alpha\n",
		"2024/b.txt":       "beta\n",
		"2024/照片 one.jpg":  "not really a jpeg",
		"2025/deep/c.bin":  strings.Repeat("c", 100_000),
		"empty-file":       "",
		"with 'quote'.txt": "q",
	})
	return src
}

// Spec §16 item 2: what tern backed up can be restored with nothing but
// restic, the repository address and the password.
func TestBackupThenNativeRestore(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := sampleTree(t)
	task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{})
	run := a.RunAndWait(t, task, nil)
	a.WantRun(t, run, model.StatusSuccess)
	if run.Kind != model.RunBackup || run.Files != 6 || len(run.Units) != 1 || run.Units[0].Name != "backup" {
		t.Errorf("run = %+v", run)
	}

	n := a.Native(t, task, conn)
	snaps := n.Snapshots()
	if len(snaps) != 1 || snaps[0].Hostname != "nas-1" {
		t.Fatalf("native restic sees %+v", snaps)
	}
	target := t.TempDir()
	n.OK("restore", "latest", "--target", target)
	kit.SameTree(t, src, filepath.Join(target, src))
}

// Review focus 3: a password with quotes, a dollar sign, non-ASCII
// characters and a space inside works, and is the same password for restic.
func TestSpecialPassword(t *testing.T) {
	const pw = `p@$$'w"ö密码 x`
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := sampleTree(t)
	task, repo := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{Password: pw})
	a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
	n := a.Native(t, task, conn)
	if got := n.Env[len(n.Env)-2]; got != "RESTIC_PASSWORD="+pw {
		t.Fatalf("recovery info password: %q", got)
	}
	if snaps := n.Snapshots(); len(snaps) != 1 {
		t.Errorf("native restic with the same password sees %d snapshots", len(snaps))
	}
	// And a second task attaches to the same repository with it.
	var created map[string]any
	a.Must(http.StatusCreated, "POST", "/api/v1/tasks", map[string]any{"type": "backup", "name": "second", "enabled": true,
		"backup": map[string]any{"connection_id": conn, "repo_path": repo, "sources": []string{src}}, "repo_password": pw, "recovery_confirmed": true}, &created)
	if created["repo_state"] != "existing" {
		t.Errorf("repo_state = %v, want existing", created["repo_state"])
	}
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	code := a.Do("POST", "/api/v1/tasks", map[string]any{"type": "backup", "name": "third", "enabled": true,
		"backup": map[string]any{"connection_id": conn, "repo_path": repo, "sources": []string{src}}, "repo_password": pw + "x", "recovery_confirmed": true}, &e)
	if code != http.StatusBadRequest || e.Error.Code != "wrong_repo_password" {
		t.Errorf("attach with a wrong password = %d %s", code, e.Error.Code)
	}
}

func TestHostAndParent(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := sampleTree(t)
	task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{})
	a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
	second := a.RunAndWait(t, task, nil)
	a.WantRun(t, second, model.StatusSuccess)
	if second.Files != 0 {
		t.Errorf("second backup of unchanged files reports %d new or changed files", second.Files)
	}
	snaps := a.Native(t, task, conn).Snapshots()
	if len(snaps) != 2 {
		t.Fatalf("%d snapshots", len(snaps))
	}
	hostname, _ := os.Hostname()
	for _, s := range snaps {
		if s.Hostname != "nas-1" || s.Hostname == hostname {
			t.Errorf("snapshot hostname = %q (this container is %q), want nas-1", s.Hostname, hostname)
		}
		if len(s.Tags) != 1 || s.Tags[0] != "tern-"+task {
			t.Errorf("snapshot tags = %q", s.Tags)
		}
	}
	if snaps[1].Parent != snaps[0].ID {
		t.Errorf("second snapshot's parent = %q, want the first snapshot %q", snaps[1].Parent, snaps[0].ID)
	}
}

func TestTaggedRetention(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := sampleTree(t)
	task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{Spec: map[string]any{"keep": map[string]any{"last": 1}}})
	// Somebody else's snapshots in the same repository: same host name
	// even, but without the task's tag.
	n := a.Native(t, task, conn)
	for i := 0; i < 3; i++ {
		n.OK("backup", "--host", "nas-1", src)
	}
	for i := 0; i < 2; i++ {
		a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
	}
	tagged, untagged := 0, 0
	for _, s := range n.Snapshots() {
		if len(s.Tags) == 0 {
			untagged++
		} else {
			tagged++
		}
	}
	if tagged != 1 || untagged != 3 {
		t.Errorf("after two runs with keep-last 1: %d tagged and %d untagged snapshots, want 1 and 3", tagged, untagged)
	}
}

// remoteLs lists a directory on the remote end; a missing one is empty.
func remoteLs(t *testing.T, cfg, dir string) []string {
	t.Helper()
	out, _, code := kit.RunSSH(t, cfg, "ls", "-1", dir)
	if code != 0 {
		return nil
	}
	return strings.Fields(out)
}

// Review focus 2 and spec §8.6: a backup killed half way leaves its lock
// behind; the next run removes it and succeeds.
func TestStaleLockRecovered(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := filepath.Join(t.TempDir(), "big")
	kit.RandomFile(t, filepath.Join(src, "blob"), 24<<20)
	task, repo := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{
		Spec: map[string]any{"limit_upload_kib": 1024},
		Task: map[string]any{"retry": map[string]any{"retries": 0, "delays_sec": []int{1}}},
	})
	cfg := kit.OpenSSH(t, kit.Authorize(t), kit.Target(t))

	runID := a.StartRun(t, task, nil)
	a.WaitProgress(t, task, runID, 2<<20) // the backup itself is under way, holding its lock
	if out, err := exec.Command("pkill", "-9", "-x", "restic").CombinedOutput(); err != nil {
		t.Fatalf("kill restic: %v %s", err, out)
	}
	first := a.WaitRun(t, runID)
	a.WaitIdle(t, task)
	if first.Status != model.StatusFailed {
		t.Fatalf("killed run ended %s, want failed\n%s", first.Status, a.RunLog(runID))
	}
	if locks := remoteLs(t, cfg, repo+"/locks"); len(locks) == 0 {
		t.Fatal("the killed restic left no lock: this test does not test what it should")
	}

	a.UpdateTask(t, task, func(task map[string]any) { task["backup"].(map[string]any)["limit_upload_kib"] = 0 })
	second := a.RunAndWait(t, task, nil)
	a.WantRun(t, second, model.StatusSuccess)
	if locks := remoteLs(t, cfg, repo+"/locks"); len(locks) != 0 {
		t.Errorf("locks left after the successful run: %q", locks)
	}
}

// Spec §6: a backup that could not read some files is "finished with
// warnings", and everything else is in the snapshot. The node runs as an
// ordinary user here: root reads files whatever their mode.
func TestUnreadableFileIsWarning(t *testing.T) {
	const nobody = 65534
	a := kit.StartAppAs(t, nobody, nobody)
	conn := a.SSHConnection(t)
	src := filepath.Join(kit.MountedDir(t), "docs")
	kit.WriteTree(t, src, map[string]string{"readable.txt": "fine\n", "sub/also.txt": "fine too\n", "secret.txt": "not for nobody\n"})
	if err := os.Chmod(filepath.Join(src, "secret.txt"), 0); err != nil {
		t.Fatal(err)
	}
	task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{})
	run := a.RunAndWait(t, task, nil)
	a.WantRun(t, run, model.StatusWarning)
	if run.Attempts != 1 {
		t.Errorf("attempts = %d; an incomplete backup must not be retried", run.Attempts)
	}

	n := a.Native(t, task, conn)
	target := t.TempDir()
	n.OK("restore", "latest", "--target", target)
	restored := filepath.Join(target, src)
	for rel, want := range map[string]string{"readable.txt": "fine\n", "sub/also.txt": "fine too\n"} {
		if got, err := os.ReadFile(filepath.Join(restored, rel)); err != nil || string(got) != want {
			t.Errorf("%s restored as %q, %v", rel, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(restored, "secret.txt")); err == nil {
		t.Error("the unreadable file is in the snapshot")
	}
}

// Review focus 1: an empty source (the share is not mounted) fails the
// run and leaves the repository as it was.
func TestEmptySourceFails(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := sampleTree(t)
	task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{Spec: map[string]any{"keep": map[string]any{"last": 1}}})
	a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
	before := a.SnapshotTotal(t, task)

	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		os.RemoveAll(filepath.Join(src, e.Name()))
	}
	run := a.RunAndWait(t, task, nil)
	if run.Status != model.StatusFailed || run.ErrorKind != model.KindConfig || !strings.Contains(run.ErrorSummary, "source is empty") || run.Attempts != 1 {
		t.Errorf("run = %s, %s, %q, %d attempts", run.Status, run.ErrorKind, run.ErrorSummary, run.Attempts)
	}
	if after := a.SnapshotTotal(t, task); after != before || before != 1 {
		t.Errorf("snapshots: %d before, %d after", before, after)
	}
}

// Spec §16 item 8: the upload limit holds.
func TestLimitUpload(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := filepath.Join(t.TempDir(), "big")
	kit.RandomFile(t, filepath.Join(src, "blob"), 32<<20)
	task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{Spec: map[string]any{"limit_upload_kib": 4096}})
	began := time.Now()
	run := a.RunAndWait(t, task, nil)
	took := time.Since(began)
	a.WantRun(t, run, model.StatusSuccess)
	if took < 6*time.Second {
		t.Errorf("32 MiB at 4096 KiB/s took %s, want at least 6s", took)
	}
	if run.Bytes < 30<<20 {
		t.Errorf("run reports %d bytes added", run.Bytes)
	}
}

// Spec §8.5: what an interrupted backup already uploaded is not uploaded
// again.
func TestInterruptThenResume(t *testing.T) {
	const total = 64 << 20
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := filepath.Join(t.TempDir(), "big")
	for i := 0; i < 16; i++ {
		kit.RandomFile(t, filepath.Join(src, fmt.Sprintf("blob-%02d", i)), total/16)
	}
	task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{Spec: map[string]any{"limit_upload_kib": 4096}})

	runID := a.StartRun(t, task, nil)
	a.WaitProgress(t, task, runID, total/2)
	a.Must(http.StatusAccepted, "POST", "/api/v1/runs/"+runID+"/cancel", nil, nil)
	first := a.WaitRun(t, runID)
	a.WaitIdle(t, task)
	if first.Status != model.StatusCanceled {
		t.Fatalf("canceled run ended %s\n%s", first.Status, a.RunLog(runID))
	}

	second := a.RunAndWait(t, task, nil)
	a.WantRun(t, second, model.StatusSuccess)
	if second.Bytes >= total*7/10 {
		t.Errorf("the second run added %d bytes of %d; what the first one uploaded was sent again\n%s", second.Bytes, total, a.RunLog(second.ID))
	}
	if second.Bytes == 0 {
		t.Error("the second run added nothing: the first one was not interrupted")
	}
}

func TestRestoreViaAPI(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := sampleTree(t)
	task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{})
	a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
	// From here on a run that executed the pre-task command would fail.
	a.UpdateTask(t, task, func(task map[string]any) { task["pre"].(map[string]any)["script"] = "exit 9" })

	var list struct {
		Total     int `json:"total"`
		Snapshots []struct {
			ID   string `json:"id"`
			Mine bool   `json:"mine"`
			Host string `json:"host"`
		} `json:"snapshots"`
		Repository string `json:"repository"`
	}
	a.Must(http.StatusOK, "GET", "/api/v1/tasks/"+task+"/snapshots", nil, &list)
	if list.Total != 1 || len(list.Snapshots) != 1 || !list.Snapshots[0].Mine || list.Snapshots[0].Host != "nas-1" || !strings.HasPrefix(list.Repository, "sftp:") {
		t.Fatalf("snapshots = %+v", list)
	}

	target := filepath.Join(kit.MountedDir(t), "restore-1")
	body := map[string]any{"snapshot_id": list.Snapshots[0].ID, "target_dir": target}
	var out struct {
		RunID string `json:"run_id"`
	}
	a.Must(http.StatusAccepted, "POST", "/api/v1/tasks/"+task+"/restore", body, &out)
	run := a.WaitRun(t, out.RunID)
	a.WaitIdle(t, task)
	a.WantRun(t, run, model.StatusSuccess)
	if run.Kind != model.RunRestore || run.Trigger != model.TriggerManual || run.Files < 6 {
		t.Errorf("run = %+v", run)
	}
	kit.SameTree(t, src, filepath.Join(target, src))

	// The same directory again: it is no longer empty.
	var e struct {
		Error struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if code := a.Do("POST", "/api/v1/tasks/"+task+"/restore", body, &e); code != http.StatusBadRequest || e.Error.Fields["target_dir"] != "not_empty" {
		t.Errorf("second restore into the same directory = %d %+v", code, e.Error)
	}
	// Not on a mounted volume: the container's own file system.
	body["target_dir"] = filepath.Join(t.TempDir(), "restore-2")
	if code := a.Do("POST", "/api/v1/tasks/"+task+"/restore", body, &e); code != http.StatusBadRequest || e.Error.Fields["target_dir"] != "not_mounted" {
		t.Errorf("restore outside the mounted volumes = %d %+v", code, e.Error)
	}

	// Only part of the snapshot.
	sub := filepath.Join(kit.MountedDir(t), "restore-3")
	a.Must(http.StatusAccepted, "POST", "/api/v1/tasks/"+task+"/restore",
		map[string]any{"snapshot_id": list.Snapshots[0].ID, "subpath": filepath.Join(src, "2024"), "target_dir": sub}, &out)
	run = a.WaitRun(t, out.RunID)
	a.WaitIdle(t, task)
	a.WantRun(t, run, model.StatusSuccess)
	kit.SameTree(t, filepath.Join(src, "2024"), sub)
}

func TestPruneAndCheck(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := sampleTree(t)
	task, _ := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{})
	a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
	for _, kind := range []model.RunKind{model.RunPrune, model.RunCheck} {
		run := a.RunAndWait(t, task, map[string]any{"kind": kind})
		a.WantRun(t, run, model.StatusSuccess)
		if run.Kind != kind {
			t.Errorf("run kind = %s, want %s", run.Kind, kind)
		}
	}
}

// The first backup of a new task is followed by the prune and the check
// that are due, each a run of its own.
func TestAutomaticMaintenance(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	task, _ := a.NewBackupTask(t, conn, []string{sampleTree(t)}, kit.BackupOpts{Maintenance: true})
	a.WantRun(t, a.RunAndWait(t, task, nil), model.StatusSuccess)
	var runs []model.Run
	a.Must(http.StatusOK, "GET", "/api/v1/runs?task_id="+task, nil, &runs)
	got := map[model.RunKind]model.Status{}
	for _, r := range runs {
		got[r.Kind] = r.Status
	}
	if len(runs) != 3 || got[model.RunBackup] != model.StatusSuccess || got[model.RunPrune] != model.StatusSuccess || got[model.RunCheck] != model.StatusSuccess {
		t.Errorf("runs = %v", got)
	}
}

// Index §6: restic's exit codes are what the plan's table says. If this
// fails, stop and report: the error classification is built on it.
func TestExitCodesAsDocumented(t *testing.T) {
	a := kit.StartApp(t)
	conn := a.SSHConnection(t)
	src := filepath.Join(t.TempDir(), "big")
	kit.RandomFile(t, filepath.Join(src, "blob"), 16<<20)
	task, repo := a.NewBackupTask(t, conn, []string{src}, kit.BackupOpts{})
	n := a.Native(t, task, conn)
	key := a.Key(conn)
	host, user := kit.Env(t, "TERN_ITEST_SSH_HOST"), kit.Env(t, "TERN_ITEST_SSH_USER")
	repoURL := func(path string) string { return "sftp:" + user + "@" + host + ":" + path }

	if _, stderr, code := n.Run("cat", "config"); code != 0 {
		t.Errorf("existing repository, right password: exit %d, want 0\n%s", code, stderr)
	}
	if _, stderr, code := kit.NewNative(t, repoURL(repo), "not the password", key).Run("cat", "config"); code != 12 {
		t.Errorf("wrong password: exit %d, want 12\n%s", code, stderr)
	}
	if _, stderr, code := kit.NewNative(t, repoURL(repo+"-nowhere"), kit.RepoPassword, key).Run("cat", "config"); code != 10 {
		t.Errorf("no repository at the path: exit %d, want 10\n%s", code, stderr)
	}

	// 11: an operation that needs the exclusive lock while a backup holds
	// its lock.
	cfg := kit.OpenSSH(t, kit.Authorize(t), kit.Target(t))
	slow := exec.Command("restic", append(append([]string{}, n.Opts...), "backup", "--limit-upload", "512", src)...)
	slow.Env = n.Env
	if err := slow.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { slow.Process.Kill(); slow.Wait() }()
	deadline := time.Now().Add(60 * time.Second)
	for len(remoteLs(t, cfg, repo+"/locks")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the slow backup never took its lock")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, stderr, code := n.Run("prune"); code != 11 {
		t.Errorf("prune while a backup is running: exit %d, want 11\n%s", code, stderr)
	}

	// 3: a source file that cannot be read, next to one that can. Root
	// reads everything, so the unreadable one is a directory entry that
	// restic cannot open: a file inside a source list that does not exist.
	other := filepath.Join(t.TempDir(), "docs")
	kit.WriteTree(t, other, map[string]string{"ok.txt": "fine\n"})
	slow.Process.Kill()
	slow.Wait()
	n.OK("unlock")
	if _, stderr, code := n.Run("backup", other, filepath.Join(other, "does-not-exist")); code != 3 {
		t.Errorf("backup with one unreadable source: exit %d, want 3\n%s", code, stderr)
	}
}
