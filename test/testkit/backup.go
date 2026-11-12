//go:build integration || acceptance

package testkit

import (
	"bytes"
	"encoding/json"
	"errors"
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

const RepoPassword = "itest-repo-password-1"

// BackupOpts are the parts of a backup task the tests vary.
type BackupOpts struct {
	Password    string // default RepoPassword
	Repo        string // default: a fresh remote directory
	Spec        map[string]any
	Task        map[string]any
	Maintenance bool // keep the automatic prune and check
}

// NewBackupTask creates a manual backup task for sources through the API
// and returns its ID and repository path. Unless asked otherwise the
// automatic prune and check are switched off, so tests see only the runs
// they start.
func (a *App) NewBackupTask(t *testing.T, connID string, sources []string, o BackupOpts) (taskID, repo string) {
	t.Helper()
	if o.Password == "" {
		o.Password = RepoPassword
	}
	if o.Repo == "" {
		o.Repo = filepath.Join(RemotePath(t), "repo")
	}
	spec := map[string]any{"connection_id": connID, "repo_path": o.Repo, "sources": sources}
	for k, v := range o.Spec {
		spec[k] = v
	}
	body := map[string]any{"type": "backup", "name": "backup-" + model.NewID(), "enabled": true, "backup": spec,
		"repo_password": o.Password, "recovery_confirmed": true}
	for k, v := range o.Task {
		body[k] = v
	}
	var created map[string]any
	a.Must(http.StatusCreated, "POST", "/api/v1/tasks", body, &created)
	taskID = created["id"].(string)
	if !o.Maintenance {
		a.UpdateTask(t, taskID, func(task map[string]any) {
			b := task["backup"].(map[string]any)
			b["prune_every_days"], b["check_every_days"] = 0, 0
		})
	}
	return taskID, o.Repo
}

// UpdateTask reads the task, lets change edit it and writes it back.
func (a *App) UpdateTask(t *testing.T, taskID string, change func(task map[string]any)) {
	t.Helper()
	var task map[string]any
	a.Must(http.StatusOK, "GET", "/api/v1/tasks/"+taskID, nil, &task)
	change(task)
	a.Must(http.StatusOK, "PUT", "/api/v1/tasks/"+taskID, task, nil)
}

// WantRun fails the test, with the run's log, unless the run ended as st.
func (a *App) WantRun(t *testing.T, run model.Run, st model.Status) {
	t.Helper()
	if run.Status != st {
		t.Fatalf("%s run ended %s (%s: %s), want %s\n%s", run.Kind, run.Status, run.ErrorKind, run.ErrorSummary, st, a.RunLog(run.ID))
	}
}

func (a *App) SnapshotTotal(t *testing.T, taskID string) int {
	t.Helper()
	var out struct {
		Total int `json:"total"`
	}
	a.Must(http.StatusOK, "GET", "/api/v1/tasks/"+taskID+"/snapshots", nil, &out)
	return out.Total
}

// Native is a plain restic, set up the way a person without tern would:
// repository address and password from the recovery information, an ssh
// configuration written by hand, and the private key in a file. Nothing
// here goes through tern's code.
type Native struct {
	t *testing.T
	// Env is the environment restic runs with (repository and password
	// last but one and last but two), Opts the options before every command.
	Env  []string
	Opts []string
}

func NewNative(t *testing.T, repository, password string, keyPEM []byte) *Native {
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
	return &Native{t: t,
		Env: append(os.Environ(), "RESTIC_REPOSITORY="+repository, "RESTIC_PASSWORD="+password,
			"RESTIC_CACHE_DIR="+filepath.Join(dir, "cache")),
		Opts: []string{"-o", "sftp.args=-F " + cfg},
	}
}

// Native returns a plain restic for the repository of a task, from the
// task's recovery information.
func (a *App) Native(t *testing.T, taskID, connID string) *Native {
	t.Helper()
	var info struct {
		Repository string   `json:"repository"`
		Password   string   `json:"password"`
		Commands   []string `json:"commands"`
	}
	a.Must(http.StatusOK, "POST", "/api/v1/tasks/"+taskID+"/recovery-info", map[string]any{"password": AdminPassword}, &info)
	a.mu.Lock()
	key := a.keys[connID]
	a.mu.Unlock()
	return NewNative(t, info.Repository, info.Password, key)
}

// Run calls restic and returns its output and exit code.
func (n *Native) Run(args ...string) (stdout, stderr string, exit int) {
	n.t.Helper()
	cmd := exec.Command("restic", append(append([]string{}, n.Opts...), args...)...)
	cmd.Env = n.Env
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

// OK calls restic and fails the test unless it exits 0.
func (n *Native) OK(args ...string) string {
	n.t.Helper()
	out, stderr, code := n.Run(args...)
	if code != 0 {
		n.t.Fatalf("restic %s: exit %d\n%s\n%s", strings.Join(args, " "), code, out, stderr)
	}
	return out
}

type NativeSnapshot struct {
	ID       string    `json:"id"`
	Parent   string    `json:"parent"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
	Time     time.Time `json:"time"`
}

// Snapshots lists the repository's snapshots, oldest first.
func (n *Native) Snapshots() []NativeSnapshot {
	n.t.Helper()
	var snaps []NativeSnapshot
	if err := json.Unmarshal([]byte(n.OK("snapshots", "--json")), &snaps); err != nil {
		n.t.Fatalf("restic snapshots --json: %v", err)
	}
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].Time.Before(snaps[j].Time) })
	return snaps
}

// SameTree fails the test unless every file under want exists under got
// with the same content, and got has no files beyond those.
func SameTree(t *testing.T, want, got string) {
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

// WaitProgress waits until the task's active run reports at least
// minBytes processed.
func (a *App) WaitProgress(t *testing.T, taskID, runID string, minBytes int64) {
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
		a.Must(http.StatusOK, "GET", "/api/v1/overview", nil, &ov)
		for _, it := range ov.Tasks {
			if it.TaskID == taskID && it.ActiveRun != nil && it.ActiveRun.Progress != nil && it.ActiveRun.Progress.BytesDone >= minBytes {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run never processed %d bytes\n%s", minBytes, a.RunLog(runID))
		}
		time.Sleep(200 * time.Millisecond)
	}
}
