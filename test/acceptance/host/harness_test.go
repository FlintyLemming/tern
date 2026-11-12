//go:build acceptance_host

// Package host holds the acceptance tests that need the real image: the
// secret leak scan, restarts and hooks end to end. They run on the build
// host and drive a compose stack (compose.yaml next to this file) with the
// docker command line and HTTP:
//
//	make image VERSION=acceptance && make acceptance-host
package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// stack is one running compose project.
type stack struct {
	t       *testing.T
	project string
	files   []string // compose files, -f in this order
	env     []string // the environment docker compose runs with
	dirs    map[string]string
}

var projects atomic.Int64

func here() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// canary returns a string that occurs nowhere unless this test put it there.
func canary(kind string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return "canary-" + kind + "-" + hex.EncodeToString(b)
}

// up starts the stack and waits until every service is healthy. env adds
// to (or overrides) the compose variables; extra names more compose files
// next to compose.yaml. The stack and its directories go away when the
// test ends; the logs are printed first if it failed.
func up(t *testing.T, env map[string]string, extra ...string) *stack {
	t.Helper()
	root := t.TempDir()
	s := &stack{t: t, project: fmt.Sprintf("tern-acc-%d-%d", os.Getpid(), projects.Add(1)), dirs: map[string]string{}}
	for _, d := range []string{"data", "secrets", "sources", "targets", "remote", "keys", "hostkey"} {
		s.dirs[d] = filepath.Join(root, d)
		if err := os.MkdirAll(s.dirs[d], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hostKey := filepath.Join(s.dirs["hostkey"], "ssh_host_ed25519_key")
	writeHostKey(t, hostKey)
	vars := map[string]string{
		"TERN_VERSION":      envOr("TERN_VERSION", "acceptance"),
		"TERN_ACC_DATA":     s.dirs["data"],
		"TERN_ACC_SECRETS":  s.dirs["secrets"],
		"TERN_ACC_SOURCES":  s.dirs["sources"],
		"TERN_ACC_TARGETS":  s.dirs["targets"],
		"TERN_ACC_REMOTE":   s.dirs["remote"],
		"TERN_ACC_KEYS":     s.dirs["keys"],
		"TERN_ACC_HOSTKEY":  hostKey,
		"TERN_ACC_DAV_PASS": canary("dav"),
	}
	for k, v := range env {
		vars[k] = v
	}
	s.env = os.Environ()
	for k, v := range vars {
		s.env = append(s.env, k+"="+v)
	}
	s.files = []string{filepath.Join(here(), "compose.yaml")}
	for _, f := range extra {
		s.files = append(s.files, filepath.Join(here(), f))
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := s.compose(time.Minute, "logs", "--no-color")
			t.Logf("logs of %s:\n%s", s.project, out)
		}
		s.compose(3*time.Minute, "down", "-v", "--remove-orphans", "-t", "5")
	})
	if out, err := s.compose(5*time.Minute, "up", "-d", "--wait", "--wait-timeout", "120"); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}
	return s
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// compose runs docker compose for this project.
func (s *stack) compose(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	all := []string{"compose", "-p", s.project}
	for _, f := range s.files {
		all = append(all, "-f", f)
	}
	cmd := exec.CommandContext(ctx, "docker", append(all, args...)...)
	cmd.Env = s.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	return out.String(), err
}

func (s *stack) must(timeout time.Duration, args ...string) string {
	s.t.Helper()
	out, err := s.compose(timeout, args...)
	if err != nil {
		s.t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// exec runs a command in a service's container. For the tern container it
// is only used for read-only sampling (the leak scan).
func (s *stack) exec(service string, cmd ...string) string {
	s.t.Helper()
	return s.must(2*time.Minute, append([]string{"exec", "-T", service}, cmd...)...)
}

// restart restarts a service and waits until it is healthy again.
func (s *stack) restart(service string) {
	s.t.Helper()
	s.must(3*time.Minute, "restart", "-t", "120", service)
	s.waitHealthy(service)
}

// stop stops a service with docker's normal SIGTERM and grace period.
func (s *stack) stop(service string) {
	s.t.Helper()
	s.must(3*time.Minute, "stop", "-t", "120", service)
}

// kill stops a service with SIGKILL: no chance to wind down.
func (s *stack) kill(service string) {
	s.t.Helper()
	s.must(time.Minute, "kill", "-s", "SIGKILL", service)
}

// start starts a stopped service and waits until it is healthy.
func (s *stack) start(service string) {
	s.t.Helper()
	s.must(3*time.Minute, "start", service)
	s.waitHealthy(service)
}

func (s *stack) waitHealthy(service string) {
	s.t.Helper()
	id := strings.TrimSpace(s.must(time.Minute, "ps", "-q", "-a", service))
	deadline := time.Now().Add(2 * time.Minute)
	last := ""
	for time.Now().Before(deadline) {
		out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", id).Output()
		last = strings.TrimSpace(string(out))
		if last == "running healthy" {
			return
		}
		time.Sleep(time.Second)
	}
	s.t.Fatalf("%s is not healthy: %s", service, last)
}

// dataDir is the host directory bound to /data.
func (s *stack) dataDir() string { return s.dirs["data"] }

// baseURL is where the host reaches tern's port 8080.
func (s *stack) baseURL() string {
	s.t.Helper()
	out := strings.TrimSpace(s.must(time.Minute, "port", "tern", "8080"))
	return "http://" + out
}

// ---- the API client ----

// client talks to tern's API like the web interface does: a session
// cookie and the CSRF header.
type client struct {
	t       *testing.T
	base    string
	c       *http.Client
	csrf    string
	dataDir string
}

func (s *stack) api() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: s.t, base: s.baseURL(), c: &http.Client{Jar: jar, Timeout: 5 * time.Minute}, dataDir: s.dataDir()}
}

// raw sends a request and returns the status code and the body as is.
func (c *client) raw(method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

// do sends a request; out, when not nil, receives the decoded response.
func (c *client) do(method, path string, body, out any) int {
	c.t.Helper()
	code, b, err := c.raw(method, path, body)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s = %d: %v\n%s", method, path, code, err, b)
		}
	}
	return code
}

// must is do for calls that have to answer want.
func (c *client) must(want int, method, path string, body, out any) {
	c.t.Helper()
	var raw json.RawMessage
	if code := c.do(method, path, body, &raw); code != want {
		c.t.Fatalf("%s %s = %d, want %d\n%s", method, path, code, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: %v\n%s", method, path, err, raw)
		}
	}
}

func (c *client) adoptCSRF(out struct {
	CSRF string `json:"csrf_token"`
}) {
	c.csrf = out.CSRF
}

// setup walks the wizard's request; passphrase only in passphrase mode.
func (c *client) setup(password, mode, passphrase string) {
	c.t.Helper()
	body := map[string]any{"password": password, "node_name": "acceptance", "unlock_mode": mode}
	if mode == "passphrase" {
		body["passphrase"] = passphrase
	}
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	c.must(http.StatusCreated, "POST", "/api/v1/setup", body, &out)
	c.adoptCSRF(out)
}

func (c *client) login(password string) {
	c.t.Helper()
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	c.must(http.StatusOK, "POST", "/api/v1/login", map[string]any{"password": password}, &out)
	c.adoptCSRF(out)
}

func (c *client) unlock(passphrase string) {
	c.t.Helper()
	c.must(http.StatusNoContent, "POST", "/api/v1/unlock", map[string]any{"passphrase": passphrase}, nil)
}

// status is GET /api/v1/status, which needs no session.
func (c *client) status() map[string]any {
	c.t.Helper()
	var st map[string]any
	c.must(http.StatusOK, "GET", "/api/v1/status", nil, &st)
	return st
}

// ---- things a test sets up through the API ----

const sshUser = "backup" // the account on the sshd service

// sshConnection lets pub log in to the sshd service, confirms its host key
// and creates a connection with the key keyID; it returns the connection ID.
func (s *stack) sshConnection(c *client, keyID, authorizedKey string) string {
	s.t.Helper()
	s.authorize(authorizedKey)
	var probe struct {
		Authorized string `json:"authorized"`
	}
	c.must(http.StatusOK, "POST", "/api/v1/connections/probe", map[string]any{"host": "sshd", "port": 22}, &probe)
	var conn struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, "POST", "/api/v1/connections", map[string]any{"type": "ssh", "name": "sshd-" + canary("c")[7:17],
		"host": "sshd", "port": 22, "username": sshUser, "ssh_key_id": keyID, "host_key": probe.Authorized}, &conn)
	return conn.ID
}

// authorize appends a public key to the sshd service's authorized_keys,
// through the host directory bound to /keys.
func (s *stack) authorize(authorizedKey string) {
	s.t.Helper()
	f, err := os.OpenFile(filepath.Join(s.dirs["keys"], "authorized_keys"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		s.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.TrimSpace(authorizedKey) + "\n"); err != nil {
		s.t.Fatal(err)
	}
}

// generatedKey has tern generate a key pair and returns its ID and public key.
func (c *client) generatedKey() (id, authorized string) {
	c.t.Helper()
	var key struct {
		ID        string `json:"id"`
		PublicKey string `json:"public_key"`
	}
	c.must(http.StatusCreated, "POST", "/api/v1/ssh-keys", map[string]any{"name": "key-" + canary("k")[7:17], "mode": "generate"}, &key)
	return key.ID, key.PublicKey
}

// task creates a task from body and returns its ID.
func (c *client) task(body map[string]any) string {
	c.t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, "POST", "/api/v1/tasks", body, &created)
	return created.ID
}

// run is the part of a run record the tests look at.
type run struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Trigger      string `json:"trigger"`
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	ErrorSummary string `json:"error_summary"`
}

var terminal = map[string]bool{"success": true, "warning": true, "partial": true, "failed": true, "canceled": true, "skipped": true, "interrupted": true}

// startRun starts a run (body as for POST …/run, nil for a plain one).
func (c *client) startRun(taskID string, body any) string {
	c.t.Helper()
	var out struct {
		RunID string `json:"run_id"`
	}
	c.must(http.StatusAccepted, "POST", "/api/v1/tasks/"+taskID+"/run", body, &out)
	return out.RunID
}

// waitRun polls a run until it is final.
func (c *client) waitRun(runID string, within time.Duration) run {
	c.t.Helper()
	deadline := time.Now().Add(within)
	for {
		var r run
		c.must(http.StatusOK, "GET", "/api/v1/runs/"+runID, nil, &r)
		if terminal[r.Status] {
			return r
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("run %s still %s after %s\n%s", runID, r.Status, within, c.runLog(runID))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// runAndWait starts a run and waits up to 5 minutes for its end.
func (c *client) runAndWait(taskID string, body any) run {
	c.t.Helper()
	return c.waitRun(c.startRun(taskID, body), 5*time.Minute)
}

// wantRun fails the test, with the run's log, unless it ended as status.
func (c *client) wantRun(r run, status string) {
	c.t.Helper()
	if r.Status != status {
		c.t.Fatalf("%s run ended %s (%s), want %s\n%s", r.Kind, r.Status, r.ErrorSummary, status, c.runLog(r.ID))
	}
}

// runLog reads a run's log from the data directory on the host.
func (c *client) runLog(runID string) string {
	b, err := os.ReadFile(filepath.Join(c.dataDir, "logs", runID+".log"))
	if err != nil {
		return "(no log: " + err.Error() + ")"
	}
	return string(b)
}

// runs lists the task's runs, newest first.
func (c *client) runs(taskID string) []run {
	c.t.Helper()
	var out []run
	c.must(http.StatusOK, "GET", "/api/v1/runs?task_id="+taskID, nil, &out)
	return out
}

// eventually polls cond every second until it holds or within passes.
func eventually(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Second)
	}
	return cond()
}

// writeFiles creates files under dir on the host: relative path → content.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// sameFiles fails the test unless got holds exactly want's regular files
// with the same contents.
func sameFiles(t *testing.T, want, got string) {
	t.Helper()
	w, g := readTree(t, want), readTree(t, got)
	if len(w) == 0 {
		t.Fatalf("%s has no files to compare", want)
	}
	for rel, content := range w {
		if have, ok := g[rel]; !ok {
			t.Errorf("%s is missing from %s", rel, got)
		} else if have != content {
			t.Errorf("%s differs between %s and %s", rel, want, got)
		}
	}
	for rel := range g {
		if _, ok := w[rel]; !ok {
			t.Errorf("%s has an extra file %s", got, rel)
		}
	}
}

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			files[rel] = string(b)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

// randomFile writes size bytes of incompressible data to path on the host.
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

// writeHostKey writes an ed25519 host key for the sshd service: path in
// OpenSSH format (0600) and path.pub.
func writeHostKey(t *testing.T, path string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		t.Fatal(err)
	}
}
