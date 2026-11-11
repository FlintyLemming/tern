//go:build integration

// Package integration holds tests that need the real tools and a real remote
// end. They run inside the compose environment: `make itest`.
package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tern/internal/app"
	"tern/internal/config"
	"tern/internal/model"
	"tern/internal/rundir"
	"tern/internal/sshx"
)

func env(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s is not set; run these tests with `make itest`", key)
	}
	return v
}

// newKey generates a key pair the remote end knows nothing about.
func newKey(t *testing.T) sshx.Parsed {
	t.Helper()
	_, p, err := sshx.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var authorizedMu sync.Mutex

// appendAuthorized adds p's public key to the authorized_keys file sshd
// reads, through the volume both containers share.
func appendAuthorized(t *testing.T, p sshx.Parsed) {
	t.Helper()
	authorizedMu.Lock()
	defer authorizedMu.Unlock()
	f, err := os.OpenFile("/keys/authorized_keys", os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(p.Authorized + "\n"); err != nil {
		t.Fatal(err)
	}
}

// authorize generates a key and lets it log in to the remote end.
func authorize(t *testing.T) sshx.Parsed {
	t.Helper()
	p := newKey(t)
	appendAuthorized(t, p)
	return p
}

// target is the remote sshd, with the host key it presents right now.
func target(t *testing.T) sshx.Target {
	t.Helper()
	tg := sshx.Target{Host: env(t, "TERN_ITEST_SSH_HOST"), Port: 22, User: env(t, "TERN_ITEST_SSH_USER")}
	hk, err := sshx.ProbeHostKey(context.Background(), tg.Host, tg.Port)
	if err != nil {
		t.Fatalf("probe %s: %v", tg.Host, err)
	}
	tg.HostKey = hk.Authorized
	return tg
}

// openSSH does what access.Opener does for an SSH connection — agent plus
// generated ssh_config in a tmpfs run directory — and returns the config
// path. Use it as: ssh -F <cfgPath> tern-target.
func openSSH(t *testing.T, p sshx.Parsed, tg sshx.Target) (cfgPath string) {
	t.Helper()
	base, err := os.MkdirTemp("/dev/shm", "ti")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	if err := rundir.VerifyTmpfs(base); err != nil {
		t.Fatal(err)
	}
	dir, err := rundir.New(base, "r")
	if err != nil {
		t.Fatal(err)
	}
	ag, err := sshx.StartAgent(dir, p.Raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ag.Close() })
	cfgPath, err = sshx.WriteConfig(dir, tg, ag.Socket())
	if err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// runSSH runs a command on the remote end through real OpenSSH.
func runSSH(t *testing.T, cfgPath string, remoteCmd ...string) (stdout, stderr string, exit int) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command("ssh", "-F", cfgPath, sshx.Alias, strings.Join(remoteCmd, " "))
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		exit = ee.ExitCode()
	default:
		t.Fatalf("run ssh: %v", err)
	}
	return out.String(), errb.String(), exit
}

// remotePath creates and returns a new directory under /srv on the remote
// end, so tests never see each other's files.
func remotePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("/srv", "t-"+model.NewID())
	if _, stderr, code := runSSH(t, openSSH(t, authorize(t), target(t)), "mkdir", path); code != 0 {
		t.Fatalf("create %s on the remote end: exit %d, %s", path, code, stderr)
	}
	return path
}

// ---- application-level helpers: a whole tern node driven through its API ----

const itestAdminPassword = "correct horse battery"

// testApp is a running tern node and a logged-in API client for it.
type testApp struct {
	t       *testing.T
	base    string
	c       *http.Client
	csrf    string
	dataDir string

	mu   sync.Mutex
	keys map[string][]byte // connection ID → private key (PEM) of that connection
}

// mountedDir returns a new directory under /dev/shm. It lies on a mount,
// so it satisfies mounts.IsMounted: restore targets and pull destinations
// go here. It is world-readable so a non-root node can use it too.
func mountedDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/dev/shm", "tern-it")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// adopt runs the setup wizard (keyfile mode, node name "nas-1") against a
// node listening at base and returns a client for it.
func adopt(t *testing.T, base, dataDir string) *testApp {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	a := &testApp{t: t, base: base, c: &http.Client{Jar: jar, Timeout: 5 * time.Minute}, dataDir: dataDir, keys: map[string][]byte{}}
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	if code := a.do("POST", "/api/v1/setup", map[string]any{"password": itestAdminPassword, "node_name": "nas-1", "unlock_mode": "keyfile"}, &out); code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
	a.csrf = out.CSRF
	return a
}

// startApp starts a node in this process, with data, secrets and runtime
// directories on /dev/shm, and completes its setup.
func startApp(t *testing.T) *testApp {
	t.Helper()
	cfg := config.Config{Listen: "127.0.0.1:0", DataDir: mountedDir(t), SecretsDir: mountedDir(t), RuntimeDir: mountedDir(t)}
	node, err := app.Start(context.Background(), cfg, "itest")
	if err != nil {
		t.Fatalf("start tern: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		node.Shutdown(ctx)
	})
	return adopt(t, "http://"+node.Addr(), cfg.DataDir)
}

var (
	ternBinOnce sync.Once
	ternBin     string
	ternBinErr  error
)

// startAppAs builds the tern binary and runs it as another user, which
// the in-process node (root in the test container) cannot do.
func startAppAs(t *testing.T, uid, gid int) *testApp {
	t.Helper()
	ternBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tern-bin") // not /dev/shm: that is mounted noexec
		if err != nil {
			ternBinErr = err
			return
		}
		os.Chmod(dir, 0o755)
		ternBin = filepath.Join(dir, "tern")
		out, err := exec.Command("go", "build", "-o", ternBin, "tern/cmd/tern").CombinedOutput()
		if err != nil {
			ternBinErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if ternBinErr != nil {
		t.Fatal(ternBinErr)
	}
	dirs := []string{mountedDir(t), mountedDir(t), mountedDir(t)}
	for _, d := range dirs {
		if err := os.Chown(d, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	var logs bytes.Buffer
	cmd := exec.Command(ternBin)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dirs[0],
		"TERN_LISTEN=" + addr, "TERN_DATA_DIR=" + dirs[0], "TERN_SECRETS_DIR=" + dirs[1], "TERN_RUNTIME_DIR=" + dirs[2]}
	cmd.Stdout, cmd.Stderr = &logs, &logs
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		if t.Failed() {
			t.Logf("tern (uid %d) output:\n%s", uid, logs.String())
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/api/v1/status")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tern as uid %d did not come up: %v\n%s", uid, err, logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return adopt(t, "http://"+addr, dirs[0])
}

// do sends an API request with the CSRF header and returns the status
// code. body, when not nil, is sent as JSON; out, when not nil, receives
// the decoded response.
func (a *testApp) do(method, path string, body any, out any) int {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.csrf != "" {
		req.Header.Set("X-CSRF-Token", a.csrf)
	}
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			a.t.Fatalf("%s %s = %d, body is not the expected JSON: %v\n%s", method, path, resp.StatusCode, err, raw)
		}
	}
	return resp.StatusCode
}

// must is do for calls that have to answer want.
func (a *testApp) must(want int, method, path string, body any, out any) {
	a.t.Helper()
	var raw json.RawMessage
	if code := a.do(method, path, body, &raw); code != want {
		a.t.Fatalf("%s %s = %d, want %d\n%s", method, path, code, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			a.t.Fatalf("%s %s: %v\n%s", method, path, err, raw)
		}
	}
}

// sshConnection makes a key the remote sshd accepts, probes and confirms
// the host fingerprint and creates the connection; it returns its ID. The
// key is imported rather than generated inside tern so that tests can also
// hand it to plain ssh and restic.
func (a *testApp) sshConnection(t *testing.T) string {
	t.Helper()
	host := env(t, "TERN_ITEST_SSH_HOST")
	return a.sshConnectionAt(t, host, host)
}

// sshConnectionAt is sshConnection to host, confirming the host key that
// keyHost presents: a different keyHost makes a connection whose host key
// does not match, as after the remote end was reinstalled.
func (a *testApp) sshConnectionAt(t *testing.T, host, keyHost string) string {
	t.Helper()
	privPEM, p, err := sshx.Generate()
	if err != nil {
		t.Fatal(err)
	}
	appendAuthorized(t, p)
	var key, probe, conn struct {
		ID         string `json:"id"`
		Authorized string `json:"authorized"`
	}
	a.must(http.StatusCreated, "POST", "/api/v1/ssh-keys", map[string]any{"name": "key-" + model.NewID(), "mode": "import", "private_key": string(privPEM)}, &key)
	user := env(t, "TERN_ITEST_SSH_USER")
	a.must(http.StatusOK, "POST", "/api/v1/connections/probe", map[string]any{"host": keyHost, "port": 22}, &probe)
	a.must(http.StatusCreated, "POST", "/api/v1/connections", map[string]any{"type": "ssh", "name": "conn-" + model.NewID(),
		"host": host, "port": 22, "username": user, "ssh_key_id": key.ID, "host_key": probe.Authorized}, &conn)
	a.mu.Lock()
	a.keys[conn.ID] = privPEM
	a.mu.Unlock()
	return conn.ID
}

// waitIdle waits until the task has no run queued or running.
func (a *testApp) waitIdle(t *testing.T, taskID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		var ov struct {
			Tasks []struct {
				TaskID    string          `json:"task_id"`
				ActiveRun json.RawMessage `json:"active_run"`
			} `json:"tasks"`
		}
		a.must(http.StatusOK, "GET", "/api/v1/overview", nil, &ov)
		busy := false
		for _, it := range ov.Tasks {
			if it.TaskID == taskID && string(it.ActiveRun) != "null" {
				busy = true
			}
		}
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s still busy after 5 minutes", taskID)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitRun polls a run until it is final, for at most 5 minutes.
func (a *testApp) waitRun(t *testing.T, runID string) model.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		var run model.Run
		a.must(http.StatusOK, "GET", "/api/v1/runs/"+runID, nil, &run)
		if run.Status.Terminal() {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s still %s after 5 minutes", runID, run.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// startRun starts a run of the task and returns its ID.
func (a *testApp) startRun(t *testing.T, taskID string, body any) string {
	t.Helper()
	var out struct {
		RunID string `json:"run_id"`
	}
	a.must(http.StatusAccepted, "POST", "/api/v1/tasks/"+taskID+"/run", body, &out)
	return out.RunID
}

// runAndWait starts a run of the task (body as for POST …/run, nil for a
// plain run) and waits for its final state.
func (a *testApp) runAndWait(t *testing.T, taskID string, body any) model.Run {
	t.Helper()
	run := a.waitRun(t, a.startRun(t, taskID, body))
	a.waitIdle(t, taskID)
	return run
}

// runLog returns the log of a run, for failure messages.
func (a *testApp) runLog(runID string) string {
	raw, err := os.ReadFile(filepath.Join(a.dataDir, "logs", runID+".log"))
	if err != nil {
		var out json.RawMessage
		a.do("GET", "/api/v1/runs/"+runID+"/log", nil, &out)
		return string(out)
	}
	return string(raw)
}

// writeTree creates files under dir: relative path → content.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// randomFile writes size bytes of incompressible data to path.
func randomFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := io.CopyN(f, rand.Reader, size); err != nil {
		t.Fatal(err)
	}
}

// webdavConnection creates a connection to the test WebDAV server, whose
// root is /srv/dav on the remote volume.
func (a *testApp) webdavConnection(t *testing.T) string {
	t.Helper()
	var conn struct {
		ID string `json:"id"`
	}
	a.must(http.StatusCreated, "POST", "/api/v1/connections", map[string]any{"type": "webdav", "name": "dav-" + model.NewID(),
		"url": env(t, "TERN_ITEST_WEBDAV_URL"), "username": env(t, "TERN_ITEST_WEBDAV_USER"), "password": env(t, "TERN_ITEST_WEBDAV_PASS")}, &conn)
	return conn.ID
}

// remoteFS maps a path on the remote end, which lies under /srv, to where
// the runner sees the same volume.
func remoteFS(t *testing.T, remotePath string) string {
	t.Helper()
	rel, ok := strings.CutPrefix(remotePath, "/srv")
	if !ok || (rel != "" && !strings.HasPrefix(rel, "/")) {
		t.Fatalf("%s is not under /srv", remotePath)
	}
	return "/remote" + rel
}

// cancelWhen follows the run on the event stream and cancels it as soon as
// a progress report satisfies cond. The run must still be going then.
func (a *testApp) cancelWhen(t *testing.T, runID string, cond func(model.Progress) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", a.base+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.c.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 4<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			event = name
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		switch event {
		case "run.progress":
			var p struct {
				RunID    string         `json:"run_id"`
				Progress model.Progress `json:"progress"`
			}
			if json.Unmarshal([]byte(data), &p) == nil && p.RunID == runID && cond(p.Progress) {
				a.must(http.StatusAccepted, "POST", "/api/v1/runs/"+runID+"/cancel", nil, nil)
				return
			}
		case "run.updated":
			var run model.Run
			if json.Unmarshal([]byte(data), &run) == nil && run.ID == runID && run.Status.Terminal() {
				t.Fatalf("run %s ended %s before it could be canceled\n%s", runID, run.Status, a.runLog(runID))
			}
		}
	}
	t.Fatalf("the event stream ended before run %s could be canceled: %v", runID, sc.Err())
}
