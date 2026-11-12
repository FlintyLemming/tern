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

// hookWorld is a node with an SSH connection and one remote directory per
// scenario for the pre- and post-task commands to leave their traces in.
type hookWorld struct {
	t    *testing.T
	s    *stack
	c    *client
	conn string
}

// task makes a sync task whose pre-task command runs on the remote end and
// creates <dir>/stopped (as if it stopped a service) and adds a line to
// <dir>/count; the post-task command removes stopped and writes
// $TERN_RESULT to <dir>/result. pre replaces the pre-task command when set.
func (w *hookWorld) task(name, protocol, direction, local, remote, pre string, extra map[string]any) (taskID, dir string) {
	w.t.Helper()
	dir = "/srv/hooks-" + name
	if pre == "" {
		pre = "mkdir -p " + dir + " && touch " + dir + "/stopped && echo pre >> " + dir + "/count"
	}
	post := "mkdir -p " + dir + " && rm -f " + dir + "/stopped && echo \"$TERN_RESULT\" > " + dir + "/result"
	body := map[string]any{"type": "sync", "name": "hooks-" + name, "enabled": true,
		"retry": map[string]any{"retries": 0, "delays_sec": []int{1}},
		"pre":   map[string]any{"script": pre, "where": "ssh", "connection_id": w.conn},
		"post":  map[string]any{"script": post, "where": "ssh", "connection_id": w.conn},
		"sync": map[string]any{"protocol": protocol, "connection_id": w.conn, "direction": direction,
			"pairs": []map[string]any{{"local": local, "remote": remote}}}}
	for k, v := range extra {
		body[k] = v
	}
	return w.c.task(body), dir
}

// remote is where the host sees a path of the remote end.
func (w *hookWorld) remote(path string) string {
	return filepath.Join(w.s.dirs["remote"], strings.TrimPrefix(path, "/srv/"))
}

// traces reports what the commands left in dir: whether stopped exists,
// the result, and the lines in count.
func (w *hookWorld) traces(dir string) (stopped bool, result string, count int) {
	d := w.remote(dir)
	stopped = exists(filepath.Join(d, "stopped"))
	if b, err := os.ReadFile(filepath.Join(d, "result")); err == nil {
		result = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(d, "count")); err == nil {
		count = len(strings.Fields(string(b)))
	}
	return
}

// want checks the traces: no stopped left behind, the result, and that the
// pre-task command added pres lines to count.
func (w *hookWorld) want(t *testing.T, dir, result string, pres int) {
	t.Helper()
	stopped, got, count := w.traces(dir)
	if stopped {
		t.Errorf("%s/stopped is still there: the post-task command did not undo the pre-task command", dir)
	}
	if got != result {
		t.Errorf("%s/result = %q, want %q", dir, got, result)
	}
	if count != pres {
		t.Errorf("%s/count has %d lines, want %d: the pre-task command ran %d times", dir, count, pres, count)
	}
}

// Spec §16 item 9 and §10: pre- and post-task commands on the remote end.
func TestHooksEndToEnd(t *testing.T) {
	s := up(t, nil)
	c := s.api()
	c.setup(adminPassword, "keyfile", "")
	keyID, pub := c.generatedKey()
	w := &hookWorld{t: t, s: s, c: c, conn: s.sshConnection(c, keyID, pub)}
	src := filepath.Join(s.dirs["sources"], "hooks")
	writeFiles(t, src, map[string]string{"a.txt": "alpha\n", "b/c.txt": "gamma\n"})

	t.Run("success", func(t *testing.T) {
		task, dir := w.task("ok", "sftp", "push", "/sources/hooks", "/srv/hooks-ok-dst", "", nil)
		c.wantRun(c.runAndWait(task, nil), "success")
		w.want(t, dir, "success", 1)
	})

	t.Run("failure", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(s.dirs["targets"], "hooks-fail"), 0o755); err != nil {
			t.Fatal(err)
		}
		task, dir := w.task("fail", "sftp", "pull", "/targets/hooks-fail", "/srv/hooks-does-not-exist", "", nil)
		c.wantRun(c.runAndWait(task, nil), "failed")
		w.want(t, dir, "failed", 1)
	})

	t.Run("canceled", func(t *testing.T) {
		randomFile(t, filepath.Join(s.dirs["sources"], "hooks-slow", "big.bin"), 32<<20)
		task, dir := w.task("cancel", "rsync", "push", "/sources/hooks-slow", "/srv/hooks-cancel-dst", "",
			nil)
		w.limit(task, 256)
		runID := c.startRun(task, nil)
		waitUploading(t, c, task, runID)
		c.must(http.StatusAccepted, "POST", "/api/v1/runs/"+runID+"/cancel", nil, nil)
		c.wantRun(c.waitRun(runID, 3*time.Minute), "canceled")
		w.want(t, dir, "canceled", 1)
	})

	t.Run("pre fails", func(t *testing.T) {
		task, dir := w.task("prefail", "sftp", "push", "/sources/hooks", "/srv/hooks-prefail-dst", "exit 1", nil)
		c.wantRun(c.runAndWait(task, nil), "failed")
		if exists(w.remote("/srv/hooks-prefail-dst")) {
			t.Error("the sync ran although its pre-task command failed")
		}
		w.want(t, dir, "failed", 0) // its only line was exit 1
	})

	t.Run("retried", func(t *testing.T) {
		randomFile(t, filepath.Join(s.dirs["sources"], "hooks-retry", "big.bin"), 32<<20)
		task, dir := w.task("retry", "rsync", "push", "/sources/hooks-retry", "/srv/hooks-retry-dst", "",
			map[string]any{"retry": map[string]any{"retries": 2, "delays_sec": []int{5, 5}}})
		w.limit(task, 1024)
		runID := c.startRun(task, nil)
		waitUploading(t, c, task, runID)
		s.restart("sshd") // the connection breaks; the run waits and tries again
		r := c.waitRun(runID, 5*time.Minute)
		c.wantRun(r, "success")
		if r.Attempts < 2 {
			t.Fatalf("attempts = %d: restarting sshd did not make the run try again\n%s", r.Attempts, c.runLog(runID))
		}
		w.want(t, dir, "success", 1) // pre ran once although the run tried twice
	})

	t.Run("dry run", func(t *testing.T) {
		task, dir := w.task("dry", "sftp", "push", "/sources/hooks", "/srv/hooks-dry-dst", "", nil)
		c.wantRun(c.runAndWait(task, map[string]any{"kind": "dry_run"}), "success")
		if exists(w.remote(dir)) {
			t.Errorf("a dry run ran the pre- or post-task command: %s exists", dir)
		}
	})
}

// limit sets the task's rate limit in KiB/s.
func (w *hookWorld) limit(taskID string, kib int) {
	w.t.Helper()
	var body map[string]any
	w.c.must(http.StatusOK, "GET", "/api/v1/tasks/"+taskID, nil, &body)
	body["sync"].(map[string]any)["limit_kib"] = kib
	w.c.must(http.StatusOK, "PUT", "/api/v1/tasks/"+taskID, body, nil)
}
