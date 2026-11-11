//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"tern/internal/model"
)

const itestRepoPassword = "itest-repo-password-1"

// backupOpts are the parts of a backup task the tests vary.
type backupOpts struct {
	password    string // default itestRepoPassword
	repo        string // default: a fresh remote directory
	spec        map[string]any
	task        map[string]any
	maintenance bool // keep the automatic prune and check
}

// newBackupTask creates a manual backup task for sources through the API
// and returns its ID and repository path. Unless asked otherwise the
// automatic prune and check are switched off, so tests see only the runs
// they start.
func (a *testApp) newBackupTask(t *testing.T, connID string, sources []string, o backupOpts) (taskID, repo string) {
	t.Helper()
	if o.password == "" {
		o.password = itestRepoPassword
	}
	if o.repo == "" {
		o.repo = filepath.Join(remotePath(t), "repo")
	}
	spec := map[string]any{"connection_id": connID, "repo_path": o.repo, "sources": sources}
	for k, v := range o.spec {
		spec[k] = v
	}
	body := map[string]any{"type": "backup", "name": "backup-" + model.NewID(), "enabled": true, "backup": spec,
		"repo_password": o.password, "recovery_confirmed": true}
	for k, v := range o.task {
		body[k] = v
	}
	var created map[string]any
	a.must(http.StatusCreated, "POST", "/api/v1/tasks", body, &created)
	taskID = created["id"].(string)
	if !o.maintenance {
		a.updateTask(t, taskID, func(task map[string]any) {
			b := task["backup"].(map[string]any)
			b["prune_every_days"], b["check_every_days"] = 0, 0
		})
	}
	return taskID, o.repo
}

// updateTask reads the task, lets change edit it and writes it back.
func (a *testApp) updateTask(t *testing.T, taskID string, change func(task map[string]any)) {
	t.Helper()
	var task map[string]any
	a.must(http.StatusOK, "GET", "/api/v1/tasks/"+taskID, nil, &task)
	change(task)
	a.must(http.StatusOK, "PUT", "/api/v1/tasks/"+taskID, task, nil)
}

// wantRun fails the test, with the run's log, unless the run ended as st.
func (a *testApp) wantRun(t *testing.T, run model.Run, st model.Status) {
	t.Helper()
	if run.Status != st {
		t.Fatalf("%s run ended %s (%s: %s), want %s\n%s", run.Kind, run.Status, run.ErrorKind, run.ErrorSummary, st, a.runLog(run.ID))
	}
}

func (a *testApp) snapshotTotal(t *testing.T, taskID string) int {
	t.Helper()
	var out struct {
		Total int `json:"total"`
	}
	a.must(http.StatusOK, "GET", "/api/v1/tasks/"+taskID+"/snapshots", nil, &out)
	return out.Total
}

// native is a plain restic, set up the way a person without tern would:
// repository address and password from the recovery information, an ssh
// configuration written by hand, and the private key in a file. Nothing
// here goes through tern's code.
type native struct {
	t    *testing.T
	env  []string
	opts []string
}

func newNative(t *testing.T, repository, password string, keyPEM []byte) *native {
	t.Helper()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "id_key")
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "ssh_config")
	content := "Host *\n  IdentityFile " + keyFile + "\n  IdentitiesOnly yes\n  BatchMode yes\n" +
		"  UserKnownHostsFile " + filepath.Join(dir, "known_hosts") + "\n  StrictHostKeyChecking accept-new\n  LogLevel ERROR\n"
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return &native{t: t,
		env: append(os.Environ(), "RESTIC_REPOSITORY="+repository, "RESTIC_PASSWORD="+password,
			"RESTIC_CACHE_DIR="+filepath.Join(dir, "cache")),
		opts: []string{"-o", "sftp.args=-F " + cfg},
	}
}

// native returns a plain restic for the repository of a task, from the
// task's recovery information.
func (a *testApp) native(t *testing.T, taskID, connID string) *native {
	t.Helper()
	var info struct {
		Repository string   `json:"repository"`
		Password   string   `json:"password"`
		Commands   []string `json:"commands"`
	}
	a.must(http.StatusOK, "POST", "/api/v1/tasks/"+taskID+"/recovery-info", map[string]any{"password": itestAdminPassword}, &info)
	a.mu.Lock()
	key := a.keys[connID]
	a.mu.Unlock()
	return newNative(t, info.Repository, info.Password, key)
}

// run calls restic and returns its output and exit code.
func (n *native) run(args ...string) (stdout, stderr string, exit int) {
	n.t.Helper()
	cmd := exec.Command("restic", append(append([]string{}, n.opts...), args...)...)
	cmd.Env = n.env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		exit = ee.ExitCode()
	default:
		n.t.Fatalf("run restic: %v", err)
	}
	return out.String(), errb.String(), exit
}

// ok calls restic and fails the test unless it exits 0.
func (n *native) ok(args ...string) string {
	n.t.Helper()
	out, stderr, code := n.run(args...)
	if code != 0 {
		n.t.Fatalf("restic %s: exit %d\n%s\n%s", strings.Join(args, " "), code, out, stderr)
	}
	return out
}

type nativeSnapshot struct {
	ID       string    `json:"id"`
	Parent   string    `json:"parent"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
	Time     time.Time `json:"time"`
}

// snapshots lists the repository's snapshots, oldest first.
func (n *native) snapshots() []nativeSnapshot {
	n.t.Helper()
	var snaps []nativeSnapshot
	if err := json.Unmarshal([]byte(n.ok("snapshots", "--json")), &snaps); err != nil {
		n.t.Fatalf("restic snapshots --json: %v", err)
	}
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].Time.Before(snaps[j].Time) })
	return snaps
}

// sameTree fails the test unless every file under want exists under got
// with the same content, and got has no files beyond those.
func sameTree(t *testing.T, want, got string) {
	t.Helper()
	list := func(root string) map[string]string {
		files := map[string]string{}
		err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fi.Mode().IsRegular() {
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(root, path)
				files[rel] = string(raw)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
		return files
	}
	w, g := list(want), list(got)
	if len(w) == 0 {
		t.Fatalf("%s has no files to compare", want)
	}
	for rel, content := range w {
		have, ok := g[rel]
		switch {
		case !ok:
			t.Errorf("%s is missing from %s", rel, got)
		case have != content:
			t.Errorf("%s differs: %d bytes restored, %d bytes in the source", rel, len(have), len(content))
		}
	}
	for rel := range g {
		if _, ok := w[rel]; !ok {
			t.Errorf("%s has an extra file %s", got, rel)
		}
	}
}

func sampleTree(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "photos")
	writeTree(t, src, map[string]string{
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
	a := startApp(t)
	conn := a.sshConnection(t)
	src := sampleTree(t)
	task, _ := a.newBackupTask(t, conn, []string{src}, backupOpts{})
	run := a.runAndWait(t, task, nil)
	a.wantRun(t, run, model.StatusSuccess)
	if run.Kind != model.RunBackup || run.Files != 6 || len(run.Units) != 1 || run.Units[0].Name != "backup" {
		t.Errorf("run = %+v", run)
	}

	n := a.native(t, task, conn)
	snaps := n.snapshots()
	if len(snaps) != 1 || snaps[0].Hostname != "nas-1" {
		t.Fatalf("native restic sees %+v", snaps)
	}
	target := t.TempDir()
	n.ok("restore", "latest", "--target", target)
	sameTree(t, src, filepath.Join(target, src))
}

// Review focus 3: a password with quotes, a dollar sign, non-ASCII
// characters and a space inside works, and is the same password for restic.
func TestSpecialPassword(t *testing.T) {
	const pw = `p@$$'w"ö密码 x`
	a := startApp(t)
	conn := a.sshConnection(t)
	src := sampleTree(t)
	task, repo := a.newBackupTask(t, conn, []string{src}, backupOpts{password: pw})
	a.wantRun(t, a.runAndWait(t, task, nil), model.StatusSuccess)
	n := a.native(t, task, conn)
	if got := n.env[len(n.env)-2]; got != "RESTIC_PASSWORD="+pw {
		t.Fatalf("recovery info password: %q", got)
	}
	if snaps := n.snapshots(); len(snaps) != 1 {
		t.Errorf("native restic with the same password sees %d snapshots", len(snaps))
	}
	// And a second task attaches to the same repository with it.
	var created map[string]any
	a.must(http.StatusCreated, "POST", "/api/v1/tasks", map[string]any{"type": "backup", "name": "second", "enabled": true,
		"backup": map[string]any{"connection_id": conn, "repo_path": repo, "sources": []string{src}}, "repo_password": pw, "recovery_confirmed": true}, &created)
	if created["repo_state"] != "existing" {
		t.Errorf("repo_state = %v, want existing", created["repo_state"])
	}
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	code := a.do("POST", "/api/v1/tasks", map[string]any{"type": "backup", "name": "third", "enabled": true,
		"backup": map[string]any{"connection_id": conn, "repo_path": repo, "sources": []string{src}}, "repo_password": pw + "x", "recovery_confirmed": true}, &e)
	if code != http.StatusBadRequest || e.Error.Code != "wrong_repo_password" {
		t.Errorf("attach with a wrong password = %d %s", code, e.Error.Code)
	}
}

func TestHostAndParent(t *testing.T) {
	a := startApp(t)
	conn := a.sshConnection(t)
	src := sampleTree(t)
	task, _ := a.newBackupTask(t, conn, []string{src}, backupOpts{})
	a.wantRun(t, a.runAndWait(t, task, nil), model.StatusSuccess)
	second := a.runAndWait(t, task, nil)
	a.wantRun(t, second, model.StatusSuccess)
	if second.Files != 0 {
		t.Errorf("second backup of unchanged files reports %d new or changed files", second.Files)
	}
	snaps := a.native(t, task, conn).snapshots()
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
	a := startApp(t)
	conn := a.sshConnection(t)
	src := sampleTree(t)
	task, _ := a.newBackupTask(t, conn, []string{src}, backupOpts{spec: map[string]any{"keep": map[string]any{"last": 1}}})
	// Somebody else's snapshots in the same repository: same host name
	// even, but without the task's tag.
	n := a.native(t, task, conn)
	for i := 0; i < 3; i++ {
		n.ok("backup", "--host", "nas-1", src)
	}
	for i := 0; i < 2; i++ {
		a.wantRun(t, a.runAndWait(t, task, nil), model.StatusSuccess)
	}
	tagged, untagged := 0, 0
	for _, s := range n.snapshots() {
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

// waitProgress waits until the task's active run reports at least
// minBytes processed.
func (a *testApp) waitProgress(t *testing.T, taskID, runID string, minBytes int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var ov struct {
			Tasks []struct {
				TaskID    string `json:"task_id"`
				ActiveRun *struct {
					Progress *model.Progress `json:"progress"`
				} `json:"active_run"`
			} `json:"tasks"`
		}
		a.must(http.StatusOK, "GET", "/api/v1/overview", nil, &ov)
		for _, it := range ov.Tasks {
			if it.TaskID == taskID && it.ActiveRun != nil && it.ActiveRun.Progress != nil && it.ActiveRun.Progress.BytesDone >= minBytes {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run never processed %d bytes\n%s", minBytes, a.runLog(runID))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// remoteLs lists a directory on the remote end; a missing one is empty.
func remoteLs(t *testing.T, cfg, dir string) []string {
	t.Helper()
	out, _, code := runSSH(t, cfg, "ls", "-1", dir)
	if code != 0 {
		return nil
	}
	return strings.Fields(out)
}

// Review focus 2 and spec §8.6: a backup killed half way leaves its lock
// behind; the next run removes it and succeeds.
func TestStaleLockRecovered(t *testing.T) {
	a := startApp(t)
	conn := a.sshConnection(t)
	src := filepath.Join(t.TempDir(), "big")
	randomFile(t, filepath.Join(src, "blob"), 24<<20)
	task, repo := a.newBackupTask(t, conn, []string{src}, backupOpts{
		spec: map[string]any{"limit_upload_kib": 1024},
		task: map[string]any{"retry": map[string]any{"retries": 0, "delays_sec": []int{1}}},
	})
	cfg := openSSH(t, authorize(t), target(t))

	runID := a.startRun(t, task, nil)
	a.waitProgress(t, task, runID, 2<<20) // the backup itself is under way, holding its lock
	if out, err := exec.Command("pkill", "-9", "-x", "restic").CombinedOutput(); err != nil {
		t.Fatalf("kill restic: %v %s", err, out)
	}
	first := a.waitRun(t, runID)
	a.waitIdle(t, task)
	if first.Status != model.StatusFailed {
		t.Fatalf("killed run ended %s, want failed\n%s", first.Status, a.runLog(runID))
	}
	if locks := remoteLs(t, cfg, repo+"/locks"); len(locks) == 0 {
		t.Fatal("the killed restic left no lock: this test does not test what it should")
	}

	a.updateTask(t, task, func(task map[string]any) { task["backup"].(map[string]any)["limit_upload_kib"] = 0 })
	second := a.runAndWait(t, task, nil)
	a.wantRun(t, second, model.StatusSuccess)
	if locks := remoteLs(t, cfg, repo+"/locks"); len(locks) != 0 {
		t.Errorf("locks left after the successful run: %q", locks)
	}
}

// Spec §6: a backup that could not read some files is "finished with
// warnings", and everything else is in the snapshot. The node runs as an
// ordinary user here: root reads files whatever their mode.
func TestUnreadableFileIsWarning(t *testing.T) {
	const nobody = 65534
	a := startAppAs(t, nobody, nobody)
	conn := a.sshConnection(t)
	src := filepath.Join(mountedDir(t), "docs")
	writeTree(t, src, map[string]string{"readable.txt": "fine\n", "sub/also.txt": "fine too\n", "secret.txt": "not for nobody\n"})
	if err := os.Chmod(filepath.Join(src, "secret.txt"), 0); err != nil {
		t.Fatal(err)
	}
	task, _ := a.newBackupTask(t, conn, []string{src}, backupOpts{})
	run := a.runAndWait(t, task, nil)
	a.wantRun(t, run, model.StatusWarning)
	if run.Attempts != 1 {
		t.Errorf("attempts = %d; an incomplete backup must not be retried", run.Attempts)
	}

	n := a.native(t, task, conn)
	target := t.TempDir()
	n.ok("restore", "latest", "--target", target)
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
	a := startApp(t)
	conn := a.sshConnection(t)
	src := sampleTree(t)
	task, _ := a.newBackupTask(t, conn, []string{src}, backupOpts{spec: map[string]any{"keep": map[string]any{"last": 1}}})
	a.wantRun(t, a.runAndWait(t, task, nil), model.StatusSuccess)
	before := a.snapshotTotal(t, task)

	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		os.RemoveAll(filepath.Join(src, e.Name()))
	}
	run := a.runAndWait(t, task, nil)
	if run.Status != model.StatusFailed || run.ErrorKind != model.KindConfig || !strings.Contains(run.ErrorSummary, "source is empty") || run.Attempts != 1 {
		t.Errorf("run = %s, %s, %q, %d attempts", run.Status, run.ErrorKind, run.ErrorSummary, run.Attempts)
	}
	if after := a.snapshotTotal(t, task); after != before || before != 1 {
		t.Errorf("snapshots: %d before, %d after", before, after)
	}
}

// Spec §16 item 8: the upload limit holds.
func TestLimitUpload(t *testing.T) {
	a := startApp(t)
	conn := a.sshConnection(t)
	src := filepath.Join(t.TempDir(), "big")
	randomFile(t, filepath.Join(src, "blob"), 32<<20)
	task, _ := a.newBackupTask(t, conn, []string{src}, backupOpts{spec: map[string]any{"limit_upload_kib": 4096}})
	began := time.Now()
	run := a.runAndWait(t, task, nil)
	took := time.Since(began)
	a.wantRun(t, run, model.StatusSuccess)
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
	a := startApp(t)
	conn := a.sshConnection(t)
	src := filepath.Join(t.TempDir(), "big")
	for i := 0; i < 16; i++ {
		randomFile(t, filepath.Join(src, fmt.Sprintf("blob-%02d", i)), total/16)
	}
	task, _ := a.newBackupTask(t, conn, []string{src}, backupOpts{spec: map[string]any{"limit_upload_kib": 4096}})

	runID := a.startRun(t, task, nil)
	a.waitProgress(t, task, runID, total/2)
	a.must(http.StatusAccepted, "POST", "/api/v1/runs/"+runID+"/cancel", nil, nil)
	first := a.waitRun(t, runID)
	a.waitIdle(t, task)
	if first.Status != model.StatusCanceled {
		t.Fatalf("canceled run ended %s\n%s", first.Status, a.runLog(runID))
	}

	second := a.runAndWait(t, task, nil)
	a.wantRun(t, second, model.StatusSuccess)
	if second.Bytes >= total*7/10 {
		t.Errorf("the second run added %d bytes of %d; what the first one uploaded was sent again\n%s", second.Bytes, total, a.runLog(second.ID))
	}
	if second.Bytes == 0 {
		t.Error("the second run added nothing: the first one was not interrupted")
	}
}

func TestRestoreViaAPI(t *testing.T) {
	a := startApp(t)
	conn := a.sshConnection(t)
	src := sampleTree(t)
	task, _ := a.newBackupTask(t, conn, []string{src}, backupOpts{})
	a.wantRun(t, a.runAndWait(t, task, nil), model.StatusSuccess)
	// From here on a run that executed the pre-task command would fail.
	a.updateTask(t, task, func(task map[string]any) { task["pre"].(map[string]any)["script"] = "exit 9" })

	var list struct {
		Total     int `json:"total"`
		Snapshots []struct {
			ID   string `json:"id"`
			Mine bool   `json:"mine"`
			Host string `json:"host"`
		} `json:"snapshots"`
		Repository string `json:"repository"`
	}
	a.must(http.StatusOK, "GET", "/api/v1/tasks/"+task+"/snapshots", nil, &list)
	if list.Total != 1 || len(list.Snapshots) != 1 || !list.Snapshots[0].Mine || list.Snapshots[0].Host != "nas-1" || !strings.HasPrefix(list.Repository, "sftp:") {
		t.Fatalf("snapshots = %+v", list)
	}

	target := filepath.Join(mountedDir(t), "restore-1")
	body := map[string]any{"snapshot_id": list.Snapshots[0].ID, "target_dir": target}
	var out struct {
		RunID string `json:"run_id"`
	}
	a.must(http.StatusAccepted, "POST", "/api/v1/tasks/"+task+"/restore", body, &out)
	run := a.waitRun(t, out.RunID)
	a.waitIdle(t, task)
	a.wantRun(t, run, model.StatusSuccess)
	if run.Kind != model.RunRestore || run.Trigger != model.TriggerManual || run.Files < 6 {
		t.Errorf("run = %+v", run)
	}
	sameTree(t, src, filepath.Join(target, src))

	// The same directory again: it is no longer empty.
	var e struct {
		Error struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if code := a.do("POST", "/api/v1/tasks/"+task+"/restore", body, &e); code != http.StatusBadRequest || e.Error.Fields["target_dir"] != "not_empty" {
		t.Errorf("second restore into the same directory = %d %+v", code, e.Error)
	}
	// Not on a mounted volume: the container's own file system.
	body["target_dir"] = filepath.Join(t.TempDir(), "restore-2")
	if code := a.do("POST", "/api/v1/tasks/"+task+"/restore", body, &e); code != http.StatusBadRequest || e.Error.Fields["target_dir"] != "not_mounted" {
		t.Errorf("restore outside the mounted volumes = %d %+v", code, e.Error)
	}

	// Only part of the snapshot.
	sub := filepath.Join(mountedDir(t), "restore-3")
	a.must(http.StatusAccepted, "POST", "/api/v1/tasks/"+task+"/restore",
		map[string]any{"snapshot_id": list.Snapshots[0].ID, "subpath": filepath.Join(src, "2024"), "target_dir": sub}, &out)
	run = a.waitRun(t, out.RunID)
	a.waitIdle(t, task)
	a.wantRun(t, run, model.StatusSuccess)
	sameTree(t, filepath.Join(src, "2024"), sub)
}

func TestPruneAndCheck(t *testing.T) {
	a := startApp(t)
	conn := a.sshConnection(t)
	src := sampleTree(t)
	task, _ := a.newBackupTask(t, conn, []string{src}, backupOpts{})
	a.wantRun(t, a.runAndWait(t, task, nil), model.StatusSuccess)
	for _, kind := range []model.RunKind{model.RunPrune, model.RunCheck} {
		run := a.runAndWait(t, task, map[string]any{"kind": kind})
		a.wantRun(t, run, model.StatusSuccess)
		if run.Kind != kind {
			t.Errorf("run kind = %s, want %s", run.Kind, kind)
		}
	}
}

// The first backup of a new task is followed by the prune and the check
// that are due, each a run of its own.
func TestAutomaticMaintenance(t *testing.T) {
	a := startApp(t)
	conn := a.sshConnection(t)
	task, _ := a.newBackupTask(t, conn, []string{sampleTree(t)}, backupOpts{maintenance: true})
	a.wantRun(t, a.runAndWait(t, task, nil), model.StatusSuccess)
	var runs []model.Run
	a.must(http.StatusOK, "GET", "/api/v1/runs?task_id="+task, nil, &runs)
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
	a := startApp(t)
	conn := a.sshConnection(t)
	src := filepath.Join(t.TempDir(), "big")
	randomFile(t, filepath.Join(src, "blob"), 16<<20)
	task, repo := a.newBackupTask(t, conn, []string{src}, backupOpts{})
	n := a.native(t, task, conn)
	a.mu.Lock()
	key := a.keys[conn]
	a.mu.Unlock()
	host, user := env(t, "TERN_ITEST_SSH_HOST"), env(t, "TERN_ITEST_SSH_USER")
	repoURL := func(path string) string { return "sftp:" + user + "@" + host + ":" + path }

	if _, stderr, code := n.run("cat", "config"); code != 0 {
		t.Errorf("existing repository, right password: exit %d, want 0\n%s", code, stderr)
	}
	if _, stderr, code := newNative(t, repoURL(repo), "not the password", key).run("cat", "config"); code != 12 {
		t.Errorf("wrong password: exit %d, want 12\n%s", code, stderr)
	}
	if _, stderr, code := newNative(t, repoURL(repo+"-nowhere"), itestRepoPassword, key).run("cat", "config"); code != 10 {
		t.Errorf("no repository at the path: exit %d, want 10\n%s", code, stderr)
	}

	// 11: an operation that needs the exclusive lock while a backup holds
	// its lock.
	cfg := openSSH(t, authorize(t), target(t))
	slow := exec.Command("restic", append(append([]string{}, n.opts...), "backup", "--limit-upload", "512", src)...)
	slow.Env = n.env
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
	if _, stderr, code := n.run("prune"); code != 11 {
		t.Errorf("prune while a backup is running: exit %d, want 11\n%s", code, stderr)
	}

	// 3: a source file that cannot be read, next to one that can. Root
	// reads everything, so the unreadable one is a directory entry that
	// restic cannot open: a file inside a source list that does not exist.
	other := filepath.Join(t.TempDir(), "docs")
	writeTree(t, other, map[string]string{"ok.txt": "fine\n"})
	slow.Process.Kill()
	slow.Wait()
	n.ok("unlock")
	if _, stderr, code := n.run("backup", other, filepath.Join(other, "does-not-exist")); code != 3 {
		t.Errorf("backup with one unreadable source: exit %d, want 3\n%s", code, stderr)
	}
}
