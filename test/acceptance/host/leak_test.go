//go:build acceptance_host

package host

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// canaries are the secrets of one leak test, by name.
type canaries map[string]string

// find reports which canaries occur in b.
func (c canaries) find(b []byte) []string {
	var hit []string
	for name, v := range c {
		if bytes.Contains(b, []byte(v)) {
			hit = append(hit, name)
		}
	}
	sort.Strings(hit)
	return hit
}

// encryptedKey is an ed25519 key in OpenSSH format, encrypted with
// passphrase, with its public half and the middle line of its PEM body.
func encryptedKey(t *testing.T, passphrase string) (pemText, authorized, middleLine string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "leak-test", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	pemText = string(pem.EncodeToMemory(block))
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(pemText), "\n")
	body := lines[1 : len(lines)-1]
	return pemText, string(ssh.MarshalAuthorizedKey(sshPub)), body[len(body)/2]
}

// sampler copies, every 200 ms, the command line and environment of every
// process in the tern container. It only reads /proc.
type sampler struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	n    int
	stop chan struct{}
	done chan struct{}
}

func (s *stack) sample() *sampler {
	id := strings.TrimSpace(s.must(time.Minute, "ps", "-q", "tern"))
	sm := &sampler{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(sm.done)
		for {
			select {
			case <-sm.stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
			out, err := exec.Command("docker", "exec", id, "sh", "-c",
				`for p in /proc/[0-9]*; do cat "$p/cmdline" "$p/environ" 2>/dev/null; echo; done`).Output()
			if err != nil {
				continue // the container may be restarting
			}
			sm.mu.Lock()
			sm.buf.Write(out)
			sm.n++
			sm.mu.Unlock()
		}
	}()
	return sm
}

func (sm *sampler) finish() (samples []byte, count int) {
	close(sm.stop)
	<-sm.done
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.buf.Bytes(), sm.n
}

// recordEvents records the event stream until stop is called.
func (c *client) recordEvents() (stop func() []byte) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/api/v1/events", nil)
		resp, err := c.c.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 4<<20)
		for sc.Scan() {
			mu.Lock()
			buf.Write(sc.Bytes())
			buf.WriteByte('\n')
			mu.Unlock()
		}
	}()
	return func() []byte {
		cancel()
		<-done
		mu.Lock()
		defer mu.Unlock()
		return buf.Bytes()
	}
}

// getRoutes are the GET routes of the API, read from its source, as
// patterns like "/api/v1/tasks/{id}".
func getRoutes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(here(), "..", "..", "..", "internal", "api", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no API sources: %v", err)
	}
	re := regexp.MustCompile(`a\.Handle\("GET (/api/v1/[^"]*)"`)
	var routes []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllSubmatch(src, -1) {
			routes = append(routes, string(m[1]))
		}
	}
	sort.Strings(routes)
	return routes
}

// leakWorld is what the leak scenario created.
type leakWorld struct {
	s        *stack
	c        *client
	secrets  canaries
	ids      map[string][]string // route parameter kind → IDs
	backup   string
	sse      []byte
	samples  []byte
	nSamples int
}

// leakScenario sets a node up in mode and exercises everything that
// handles a secret while sampling the container's processes and recording
// the event stream.
func leakScenario(t *testing.T, mode string) *leakWorld {
	secrets := canaries{
		"admin password":      canary("admin"),
		"key passphrase":      canary("keypass"),
		"WebDAV password":     canary("dav"),
		"repository password": canary("repo"),
	}
	if mode == "passphrase" {
		secrets["vault passphrase"] = canary("vault")
	}
	keyPEM, authorized, middle := encryptedKey(t, secrets["key passphrase"])
	secrets["private key PEM line"] = middle

	s := up(t, map[string]string{"TERN_ACC_DAV_PASS": secrets["WebDAV password"]})
	c := s.api()
	c.setup(secrets["admin password"], mode, secrets["vault passphrase"])
	w := &leakWorld{s: s, c: c, secrets: secrets, ids: map[string][]string{}}

	sm := s.sample()
	stopSSE := c.recordEvents()

	var key struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, "POST", "/api/v1/ssh-keys", map[string]any{"name": "leak", "mode": "import",
		"private_key": keyPEM, "passphrase": secrets["key passphrase"]}, &key)
	sshConn := s.sshConnection(c, key.ID, authorized)
	var dav struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, "POST", "/api/v1/connections", map[string]any{"type": "webdav", "name": "dav",
		"url": "http://webdav:8081", "username": "dav", "password": secrets["WebDAV password"]}, &dav)
	for _, id := range []string{sshConn, dav.ID} {
		var res struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}
		c.must(http.StatusOK, "POST", "/api/v1/connections/"+id+"/test", nil, &res)
		if !res.OK {
			t.Fatalf("connection test of %s: %s", id, res.Message)
		}
	}
	w.ids["connection"] = []string{sshConn, dav.ID}
	w.ids["key"] = []string{key.ID}

	writeFiles(t, filepath.Join(s.dirs["sources"], "photos"), map[string]string{
		"a.txt": "alpha\n", "2024/b.txt": "beta\n", "2024/c.bin": strings.Repeat("c", 200_000),
	})
	hooks := map[string]any{"pre": map[string]any{"script": "env; ps aux", "where": "local"},
		"post": map[string]any{"script": "env; ps aux", "where": "local"}}
	withHooks := func(body map[string]any) map[string]any {
		for k, v := range hooks {
			body[k] = v
		}
		return body
	}

	w.backup = c.task(withHooks(map[string]any{"type": "backup", "name": "leak-backup", "enabled": true,
		"backup":        map[string]any{"connection_id": sshConn, "repo_path": "/srv/repo", "sources": []string{"/sources/photos"}},
		"repo_password": secrets["repository password"], "recovery_confirmed": true}))
	c.wantRun(c.runAndWait(w.backup, nil), "success")
	w.ids["task"] = append(w.ids["task"], w.backup)

	sync := func(name, protocol, conn, direction, local, remote string) string {
		id := c.task(withHooks(map[string]any{"type": "sync", "name": name, "enabled": true,
			"retry": map[string]any{"retries": 0, "delays_sec": []int{1}},
			"sync": map[string]any{"protocol": protocol, "connection_id": conn, "direction": direction,
				"pairs": []map[string]any{{"local": local, "remote": remote}}}}))
		w.ids["task"] = append(w.ids["task"], id)
		return id
	}
	for _, p := range []struct{ protocol, conn, remote string }{
		{"sftp", sshConn, "/srv/sftp-dst"}, {"rsync", sshConn, "/srv/rsync-dst"}, {"webdav", dav.ID, "/webdav-dst"},
	} {
		c.wantRun(c.runAndWait(sync("leak-"+p.protocol, p.protocol, p.conn, "push", "/sources/photos", p.remote), nil), "success")
	}
	// Doomed: the remote directory to pull from does not exist.
	if err := os.MkdirAll(filepath.Join(s.dirs["targets"], "doomed"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.wantRun(c.runAndWait(sync("leak-doomed", "sftp", sshConn, "pull", "/targets/doomed", "/srv/does-not-exist"), nil), "failed")

	var snaps struct {
		Snapshots []struct {
			ID string `json:"id"`
		} `json:"snapshots"`
	}
	c.must(http.StatusOK, "GET", "/api/v1/tasks/"+w.backup+"/snapshots", nil, &snaps)
	if len(snaps.Snapshots) == 0 {
		t.Fatal("the backup made no snapshot")
	}
	var restore struct {
		RunID string `json:"run_id"`
	}
	c.must(http.StatusAccepted, "POST", "/api/v1/tasks/"+w.backup+"/restore",
		map[string]any{"snapshot_id": snaps.Snapshots[0].ID, "target_dir": "/targets/restore-leak"}, &restore)
	c.wantRun(c.waitRun(restore.RunID, 5*time.Minute), "success")

	w.sse = stopSSE()
	w.samples, w.nSamples = sm.finish()
	for _, r := range c.allRuns() {
		w.ids["run"] = append(w.ids["run"], r.ID)
	}
	return w
}

// allRuns lists every run of the node.
func (c *client) allRuns() []run {
	c.t.Helper()
	var out []run
	c.must(http.StatusOK, "GET", "/api/v1/runs?limit=200", nil, &out)
	return out
}

// scanDir fails the test for every file under dir that contains a canary.
func (w *leakWorld) scanDir(t *testing.T, what, dir string) int {
	t.Helper()
	n := 0
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || !fi.Mode().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		if hit := w.secrets.find(b); len(hit) > 0 {
			t.Errorf("%s: %s contains %v", what, p, hit)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return n
}

func (w *leakWorld) scanDataAndLogs(t *testing.T) {
	t.Helper()
	// (a) every file under /data: database, -wal, -shm, run logs, restic cache
	n := w.scanDir(t, "data directory", w.s.dataDir())
	t.Logf("scanned %d files under the data directory", n)
	// (b) the container's own output
	logs := w.s.must(time.Minute, "logs", "--no-color", "tern")
	if hit := w.secrets.find([]byte(logs)); len(hit) > 0 {
		t.Errorf("docker logs contain %v", hit)
	}
}

// scanAPI fetches every GET route of the API with every ID the scenario
// made and scans the responses, the recorded event stream and every run
// log.
func (w *leakWorld) scanAPI(t *testing.T) {
	t.Helper()
	params := map[string]string{
		"/api/v1/connections/{id}":        "connection",
		"/api/v1/tasks/{id}":              "task",
		"/api/v1/tasks/{id}/snapshots":    "backup",
		"/api/v1/tasks/{id}/command-line": "task",
		"/api/v1/runs/{id}":               "run",
		"/api/v1/runs/{id}/log":           "run",
	}
	w.ids["backup"] = []string{w.backup}
	fetched := 0
	for _, route := range getRoutes(t) {
		var paths []string
		switch kind, ok := params[route]; {
		case route == "/api/v1/events":
			continue // a stream: recorded while the scenario ran, scanned below
		case ok:
			for _, id := range w.ids[kind] {
				paths = append(paths, strings.Replace(route, "{id}", id, 1))
			}
			if len(paths) == 0 {
				t.Fatalf("no %s IDs to fetch %s with", kind, route)
			}
		case strings.Contains(route, "{"):
			t.Fatalf("GET %s is new: tell this test which IDs to fetch it with", route)
		default:
			paths = []string{route}
		}
		for _, p := range paths {
			code, body, err := w.c.raw("GET", p, nil)
			if err != nil || code != http.StatusOK {
				t.Errorf("GET %s = %d %v", p, code, err)
			}
			fetched++
			if hit := w.secrets.find(body); len(hit) > 0 {
				t.Errorf("GET %s contains %v", p, hit)
			}
		}
	}
	t.Logf("scanned %d GET responses", fetched)
	if len(w.sse) == 0 {
		t.Error("the event stream recorded nothing")
	}
	if hit := w.secrets.find(w.sse); len(hit) > 0 {
		t.Errorf("the event stream contains %v", hit)
	}
}

func (w *leakWorld) scanSamples(t *testing.T) {
	t.Helper()
	if w.nSamples < 10 {
		t.Fatalf("only %d process samples", w.nSamples)
	}
	t.Logf("scanned %d samples of every process's command line and environment", w.nSamples)
	if hit := w.secrets.find(w.samples); len(hit) > 0 {
		t.Errorf("process command lines or environments contain %v", hit)
	}
}

// Spec §16 item 6: no plaintext secret in the database, logs, API
// responses or the process list.
func TestNoSecretLeak(t *testing.T) {
	w := leakScenario(t, "keyfile")
	w.scanDataAndLogs(t)
	w.scanAPI(t)
	w.scanSamples(t)

	// The one place that has to hand the repository password out.
	var info struct {
		Password string `json:"password"`
	}
	w.c.must(http.StatusOK, "POST", "/api/v1/tasks/"+w.backup+"/recovery-info", map[string]any{"password": w.secrets["admin password"]}, &info)
	if info.Password != w.secrets["repository password"] {
		t.Errorf("recovery info does not give the repository password back")
	}

	if left := strings.TrimSpace(w.s.exec("tern", "ls", "-A", "/dev/shm/tern")); left != "" {
		t.Errorf("the runtime directory is not empty after every run ended:\n%s", left)
	}
}

// The same in passphrase mode, with the vault passphrase as one more
// canary; the node is restarted and unlocked once on the way.
func TestNoSecretLeakPassphraseMode(t *testing.T) {
	w := leakScenario(t, "passphrase")
	w.s.restart("tern")
	c := w.s.api()
	c.login(w.secrets["admin password"])
	c.unlock(w.secrets["vault passphrase"])
	w.c = c
	c.wantRun(c.runAndWait(w.backup, nil), "success")
	w.scanDataAndLogs(t)
	w.scanAPI(t)
	w.scanSamples(t)
}
