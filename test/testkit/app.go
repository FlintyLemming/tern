//go:build integration || acceptance

// Package testkit holds what the integration tests (test/integration) and
// the weak-network acceptance tests (test/acceptance) share: a tern node
// started in the test process and driven through its API, connections to
// the compose environment's sshd and WebDAV server, and file helpers. It
// only builds with one of their tags and only works inside that
// environment (`make itest`, `make acceptance`).
package testkit

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

func Env(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s is not set; run these tests with `make itest`", key)
	}
	return v
}

// NewKey generates a key pair the remote end knows nothing about.
func NewKey(t *testing.T) sshx.Parsed {
	t.Helper()
	_, p, err := sshx.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var authorizedMu sync.Mutex

// AppendAuthorized adds p's public key to the authorized_keys file sshd
// reads, through the volume both containers share.
func AppendAuthorized(t *testing.T, p sshx.Parsed) {
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

// Authorize generates a key and lets it log in to the remote end.
func Authorize(t *testing.T) sshx.Parsed {
	t.Helper()
	p := NewKey(t)
	AppendAuthorized(t, p)
	return p
}

// Target is the remote sshd, with the host key it presents right now.
func Target(t *testing.T) sshx.Target {
	t.Helper()
	tg := sshx.Target{Host: Env(t, "TERN_ITEST_SSH_HOST"), Port: 22, User: Env(t, "TERN_ITEST_SSH_USER")}
	hk, err := sshx.ProbeHostKey(context.Background(), tg.Host, tg.Port)
	if err != nil {
		t.Fatalf("probe %s: %v", tg.Host, err)
	}
	tg.HostKey = hk.Authorized
	return tg
}

// OpenSSH does what access.Opener does for an SSH connection — agent plus
// generated ssh_config in a tmpfs run directory — and returns the config
// path. Use it as: ssh -F <cfgPath> tern-target.
func OpenSSH(t *testing.T, p sshx.Parsed, tg sshx.Target) (cfgPath string) {
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

// RunSSH runs a command on the remote end through real OpenSSH.
func RunSSH(t *testing.T, cfgPath string, remoteCmd ...string) (stdout, stderr string, exit int) {
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

// RemotePath creates and returns a new directory under /srv on the remote
// end, so tests never see each other's files.
func RemotePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("/srv", "t-"+model.NewID())
	if _, stderr, code := RunSSH(t, OpenSSH(t, Authorize(t), Target(t)), "mkdir", path); code != 0 {
		t.Fatalf("create %s on the remote end: exit %d, %s", path, code, stderr)
	}
	return path
}

// ---- application-level helpers: a whole tern node driven through its API ----

const AdminPassword = "correct horse battery"

// App is a running tern node and a logged-in API client for it.
type App struct {
	t       *testing.T
	base    string
	c       *http.Client
	csrf    string
	DataDir string

	mu   sync.Mutex
	keys map[string][]byte // connection ID → private key (PEM) of that connection
}

// MountedDir returns a new directory under /dev/shm. It lies on a mount,
// so it satisfies mounts.IsMounted: restore targets and pull destinations
// go here. It is world-readable so a non-root node can use it too.
func MountedDir(t *testing.T) string {
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

// Adopt runs the setup wizard (keyfile mode, node name "nas-1") against a
// node listening at base and returns a client for it.
func Adopt(t *testing.T, base, dataDir string) *App {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	a := &App{t: t, base: base, c: &http.Client{Jar: jar, Timeout: 5 * time.Minute}, DataDir: dataDir, keys: map[string][]byte{}}
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	if code := a.Do("POST", "/api/v1/setup", map[string]any{"password": AdminPassword, "node_name": "nas-1"}, &out); code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
	a.csrf = out.CSRF
	return a
}

// StartApp starts a node in this process, with data, secrets and runtime
// directories on /dev/shm, and completes its setup.
func StartApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Config{Listen: "127.0.0.1:0", DataDir: MountedDir(t), SecretsDir: MountedDir(t), RuntimeDir: MountedDir(t)}
	node, err := app.Start(context.Background(), cfg, "itest")
	if err != nil {
		t.Fatalf("start tern: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		node.Shutdown(ctx)
	})
	return Adopt(t, "http://"+node.Addr(), cfg.DataDir)
}

var (
	ternBinOnce sync.Once
	ternBin     string
	ternBinErr  error
)

// StartAppAs builds the tern binary and runs it as another user, which
// the in-process node (root in the test container) cannot do.
func StartAppAs(t *testing.T, uid, gid int) *App {
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
	dirs := []string{MountedDir(t), MountedDir(t), MountedDir(t)}
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
	return Adopt(t, "http://"+addr, dirs[0])
}

// Key returns the private key (PEM) of a connection SSHConnection made.
func (a *App) Key(connID string) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keys[connID]
}

// Do sends an API request with the CSRF header and returns the status
// code. body, when not nil, is sent as JSON; out, when not nil, receives
// the decoded response.
func (a *App) Do(method, path string, body any, out any) int {
	a.t.Helper()
	code, err := a.Try(method, path, body, out)
	if err != nil {
		a.t.Fatal(err)
	}
	return code
}

// Try is Do for goroutines other than the test's: it returns what went
// wrong instead of failing the test.
func (a *App) Try(method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.csrf != "" {
		req.Header.Set("X-CSRF-Token", a.csrf)
	}
	resp, err := a.c.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s = %d, body is not the expected JSON: %v\n%s", method, path, resp.StatusCode, err, raw)
		}
	}
	return resp.StatusCode, nil
}

// Must is do for calls that have to answer want.
func (a *App) Must(want int, method, path string, body any, out any) {
	a.t.Helper()
	var raw json.RawMessage
	if code := a.Do(method, path, body, &raw); code != want {
		a.t.Fatalf("%s %s = %d, want %d\n%s", method, path, code, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			a.t.Fatalf("%s %s: %v\n%s", method, path, err, raw)
		}
	}
}

// SSHConnection makes a key the remote sshd accepts, probes and confirms
// the host fingerprint and creates the connection; it returns its ID. The
// key is imported rather than generated inside tern so that tests can also
// hand it to plain ssh and restic.
func (a *App) SSHConnection(t *testing.T) string {
	t.Helper()
	host := Env(t, "TERN_ITEST_SSH_HOST")
	return a.SSHConnectionAt(t, host, host)
}

// SSHConnectionAt is sshConnection to host, confirming the host key that
// keyHost presents: a different keyHost makes a connection whose host key
// does not match, as after the remote end was reinstalled.
func (a *App) SSHConnectionAt(t *testing.T, host, keyHost string) string {
	t.Helper()
	privPEM, p, err := sshx.Generate()
	if err != nil {
		t.Fatal(err)
	}
	AppendAuthorized(t, p)
	var key, probe, conn struct {
		ID         string `json:"id"`
		Authorized string `json:"authorized"`
	}
	a.Must(http.StatusCreated, "POST", "/api/v1/ssh-keys", map[string]any{"name": "key-" + model.NewID(), "mode": "import", "private_key": string(privPEM)}, &key)
	user := Env(t, "TERN_ITEST_SSH_USER")
	a.Must(http.StatusOK, "POST", "/api/v1/connections/probe", map[string]any{"host": keyHost, "port": 22}, &probe)
	a.Must(http.StatusCreated, "POST", "/api/v1/connections", map[string]any{"type": "ssh", "name": "conn-" + model.NewID(),
		"host": host, "port": 22, "username": user, "ssh_key_id": key.ID, "host_key": probe.Authorized}, &conn)
	a.mu.Lock()
	a.keys[conn.ID] = privPEM
	a.mu.Unlock()
	return conn.ID
}

// WaitIdle waits until the task has no run queued or running.
func (a *App) WaitIdle(t *testing.T, taskID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		var ov struct {
			Tasks []struct {
				TaskID    string          `json:"task_id"`
				ActiveRun json.RawMessage `json:"active_run"`
			} `json:"tasks"`
		}
		a.Must(http.StatusOK, "GET", "/api/v1/overview", nil, &ov)
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

// WaitRun polls a run until it is final, for at most 5 minutes.
func (a *App) WaitRun(t *testing.T, runID string) model.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		var run model.Run
		a.Must(http.StatusOK, "GET", "/api/v1/runs/"+runID, nil, &run)
		if run.Status.Terminal() {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s still %s after 5 minutes", runID, run.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// StartRun starts a run of the task and returns its ID.
func (a *App) StartRun(t *testing.T, taskID string, body any) string {
	t.Helper()
	var out struct {
		RunID string `json:"run_id"`
	}
	a.Must(http.StatusAccepted, "POST", "/api/v1/tasks/"+taskID+"/run", body, &out)
	return out.RunID
}

// RunAndWait starts a run of the task (body as for POST …/run, nil for a
// plain run) and waits for its final state.
func (a *App) RunAndWait(t *testing.T, taskID string, body any) model.Run {
	t.Helper()
	run := a.WaitRun(t, a.StartRun(t, taskID, body))
	a.WaitIdle(t, taskID)
	return run
}

// RunLog returns the log of a run, for failure messages.
func (a *App) RunLog(runID string) string {
	raw, err := os.ReadFile(filepath.Join(a.DataDir, "logs", runID+".log"))
	if err != nil {
		var out json.RawMessage
		a.Do("GET", "/api/v1/runs/"+runID+"/log", nil, &out)
		return string(out)
	}
	return string(raw)
}

// WriteTree creates files under dir: relative path → content.
func WriteTree(t *testing.T, dir string, files map[string]string) {
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

// RandomFile writes size bytes of incompressible data to path.
func RandomFile(t *testing.T, path string, size int64) {
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

// WebDAVConnection creates a connection to the test WebDAV server, whose
// root is /srv/dav on the remote volume.
func (a *App) WebDAVConnection(t *testing.T) string {
	t.Helper()
	var conn struct {
		ID string `json:"id"`
	}
	a.Must(http.StatusCreated, "POST", "/api/v1/connections", map[string]any{"type": "webdav", "name": "dav-" + model.NewID(),
		"url": Env(t, "TERN_ITEST_WEBDAV_URL"), "username": Env(t, "TERN_ITEST_WEBDAV_USER"), "password": Env(t, "TERN_ITEST_WEBDAV_PASS")}, &conn)
	return conn.ID
}

// RemoteFS maps a path on the remote end, which lies under /srv, to where
// the runner sees the same volume.
func RemoteFS(t *testing.T, remotePath string) string {
	t.Helper()
	rel, ok := strings.CutPrefix(remotePath, "/srv")
	if !ok || (rel != "" && !strings.HasPrefix(rel, "/")) {
		t.Fatalf("%s is not under /srv", remotePath)
	}
	return "/remote" + rel
}

// CancelWhen follows the run on the event stream and cancels it as soon as
// a progress report satisfies cond. The run must still be going then.
func (a *App) CancelWhen(t *testing.T, runID string, cond func(model.Progress) bool) {
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
				a.Must(http.StatusAccepted, "POST", "/api/v1/runs/"+runID+"/cancel", nil, nil)
				return
			}
		case "run.updated":
			var run model.Run
			if json.Unmarshal([]byte(data), &run) == nil && run.ID == runID && run.Status.Terminal() {
				t.Fatalf("run %s ended %s before it could be canceled\n%s", runID, run.Status, a.RunLog(runID))
			}
		}
	}
	t.Fatalf("the event stream ended before run %s could be canceled: %v", runID, sc.Err())
}
