package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tern/internal/access"
	"tern/internal/auth"
	"tern/internal/clock"
	"tern/internal/config"
	"tern/internal/engine"
	"tern/internal/engine/fake"
	"tern/internal/events"
	"tern/internal/hooks"
	"tern/internal/model"
	"tern/internal/runner"
	"tern/internal/scheduler"
	"tern/internal/store"
	"tern/internal/vault"
)

// Cron expressions are read in time.Local; pin it for the schedule tests.
func init() { time.Local = time.UTC }

const (
	adminPass = "correct horse battery"
	vaultPass = "another long passphrase"
	wantCSP   = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
)

type harness struct {
	t       *testing.T
	st      *store.Store
	secrets string
	now     time.Time
	api     *API
	vault   *vault.Vault

	cookie string // sent on every request when non-empty
	csrf   string // sent on every non-GET request when non-empty

	// Execution core, rebuilt by restart like the rest of the process.
	dataDir, runtimeDir string
	clk                 *clock.Fake // starts at now; move both with advance
	hub                 *events.Hub
	rec                 *recorder
	runner              *runner.Runner
	sched               *scheduler.Scheduler
	stopCore            func()

	enginesMu sync.Mutex
	engines   map[string]*fake.Engine // by task name
}

// engine returns the fake engine runs of the task called name are built
// from; by default one that succeeds at once.
func (h *harness) engine(name string) *fake.Engine {
	h.enginesMu.Lock()
	defer h.enginesMu.Unlock()
	if h.engines[name] == nil {
		h.engines[name] = &fake.Engine{DescribeLines: []string{"fake-tool --for " + name}}
	}
	return h.engines[name]
}

// blocked makes runs of the task called name hang until Block is closed.
func (h *harness) blocked(name string) *fake.Engine {
	e := &fake.Engine{Block: make(chan struct{})}
	h.enginesMu.Lock()
	h.engines[name] = e
	h.enginesMu.Unlock()
	return e
}

// advance moves time forward and lets the scheduler act on it.
func (h *harness) advance(d time.Duration) {
	h.now = h.now.Add(d)
	h.clk.Advance(d)
	h.sched.Sync()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &harness{t: t, st: st, secrets: t.TempDir(), now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
		dataDir: t.TempDir(), runtimeDir: t.TempDir(), engines: map[string]*fake.Engine{}}
	t.Cleanup(func() { h.stopCore() }) // before the store closes
	h.restart()
	return h
}

// restart builds a new Vault and API over the same store, as after a process restart.
func (h *harness) restart() {
	h.t.Helper()
	h.vault = vault.New(h.st, h.secrets)
	if _, err := h.vault.Load(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	now := func() time.Time { return h.now }

	if h.stopCore != nil {
		h.stopCore()
	}
	h.clk = clock.NewFake(h.now)
	h.hub = events.NewHub()
	recCtx, recCancel := context.WithCancel(context.Background())
	h.rec = newRecorder(recCtx, h.hub)
	reg := engine.NewRegistry()
	build := func(t model.Task, req engine.Request) (engine.Engine, error) {
		return fake.Builder(h.engine(t.Name))(t, req)
	}
	reg.Register(model.TaskSync, build)
	reg.Register(model.TaskBackup, build)
	h.runner = &runner.Runner{
		Store: h.st, Registry: reg, Events: h.hub, Clock: h.clk, DataDir: h.dataDir, RuntimeDir: h.runtimeDir,
		Hooks: hooks.Runner{Access: access.Opener{Store: h.st, Vault: h.vault}, RuntimeDir: h.runtimeDir},
	}
	sched := scheduler.New(scheduler.Deps{Store: h.st, Runner: h.runner, Clock: h.clk, Events: h.hub})
	sched.SetPaused(h.vault.State() != vault.StateUnlocked)
	if err := sched.Start(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.sched = sched
	h.stopCore = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sched.Shutdown(ctx)
		recCancel()
	}

	h.api = New(Deps{
		Cfg:       config.Config{SecretsDir: h.secrets, DataDir: h.dataDir},
		Store:     h.st,
		Vault:     h.vault,
		Sessions:  auth.NewSessions(h.st.DB(), now),
		Limiter:   auth.NewLimiter(),
		Version:   "test",
		Now:       now,
		Clock:     h.clk,
		Scheduler: sched,
		Runner:    h.runner,
		Registry:  reg,
		Events:    h.hub,
	})
	h.cookie, h.csrf = "", ""
}

type reqOpt func(*http.Request)

func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func noCSRF() reqOpt            { return func(r *http.Request) { r.Header.Del("X-CSRF-Token") } }

// do sends a request. body may be nil, a string (sent verbatim) or any JSON-able value.
func (h *harness) do(method, path string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if h.cookie != "" {
		r.AddCookie(&http.Cookie{Name: "tern_session", Value: h.cookie})
	}
	if h.csrf != "" && method != http.MethodGet {
		r.Header.Set("X-CSRF-Token", h.csrf)
	}
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.api.ServeHTTP(w, r)
	return w
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == "tern_session" {
			return c
		}
	}
	return nil
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, w.Body)
	}
	return m
}

// adopt makes later requests use the session a setup or login response issued.
func (h *harness) adopt(w *httptest.ResponseRecorder) {
	h.t.Helper()
	c := sessionCookie(w)
	if c == nil {
		h.t.Fatalf("no tern_session cookie in response %d %s", w.Code, w.Body)
	}
	h.cookie = c.Value
	h.csrf, _ = decode(h.t, w)["csrf_token"].(string)
	if h.csrf == "" {
		h.t.Fatalf("no csrf_token in response %s", w.Body)
	}
}

func setupBody(mode string) map[string]any {
	b := map[string]any{"password": adminPass, "node_name": "nas-1", "unlock_mode": mode}
	if mode == "passphrase" {
		b["passphrase"] = vaultPass
	}
	return b
}

func (h *harness) setup(mode string) {
	h.t.Helper()
	w := h.do("POST", "/api/v1/setup", setupBody(mode))
	if w.Code != http.StatusCreated {
		h.t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	h.adopt(w)
}

func (h *harness) login() {
	h.t.Helper()
	w := h.do("POST", "/api/v1/login", map[string]any{"password": adminPass})
	if w.Code != http.StatusOK {
		h.t.Fatalf("login = %d %s", w.Code, w.Body)
	}
	h.adopt(w)
}

// wantError asserts status and error code and returns the error object.
func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body %s", w.Code, status, w.Body)
	}
	e, _ := decode(t, w)["error"].(map[string]any)
	if e == nil || e["code"] != code {
		t.Fatalf("error = %v, want code %q; body %s", e, code, w.Body)
	}
	if msg, _ := e["message"].(string); msg == "" {
		t.Errorf("error has no message: %s", w.Body)
	}
	return e
}

func wantField(t *testing.T, e map[string]any, field string) {
	t.Helper()
	f, _ := e["fields"].(map[string]any)
	if _, ok := f[field]; !ok {
		t.Fatalf("fields = %v, want an entry for %q", f, field)
	}
}

func (h *harness) status() map[string]any {
	h.t.Helper()
	w := h.do("GET", "/api/v1/status", nil)
	if w.Code != http.StatusOK {
		h.t.Fatalf("status = %d %s", w.Code, w.Body)
	}
	return decode(h.t, w)
}

func (h *harness) metaRows() int {
	h.t.Helper()
	var n int
	if err := h.st.DB().QueryRow("SELECT COUNT(*) FROM meta").Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// ---- status and setup ----

func TestStatusBeforeSetup(t *testing.T) {
	h := newHarness(t)
	s := h.status()
	if s["setup_required"] != true || s["authenticated"] != false || s["vault_state"] != "uninitialized" || s["version"] != "test" {
		t.Fatalf("status = %v", s)
	}
}

func TestSetupThenStatus(t *testing.T) {
	h := newHarness(t)
	w := h.do("POST", "/api/v1/setup", setupBody("keyfile"))
	if w.Code != http.StatusCreated {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	c := sessionCookie(w)
	if c == nil || c.Value == "" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie = %+v, want tern_session with HttpOnly, SameSite=Strict, Path=/", c)
	}
	h.adopt(w)
	s := h.status()
	if s["setup_required"] != false || s["vault_state"] != "unlocked" || s["authenticated"] != true {
		t.Fatalf("status after setup = %v", s)
	}
	id, _, _ := h.st.GetMeta(context.Background(), "node.id")
	name, _, _ := h.st.GetMeta(context.Background(), "node.name")
	if len(id) != 32 || name != "nas-1" {
		t.Errorf("node.id=%q node.name=%q", id, name)
	}
}

func TestSetupTwice(t *testing.T) {
	h := newHarness(t)
	h.setup("keyfile")
	wantError(t, h.do("POST", "/api/v1/setup", setupBody("keyfile")), http.StatusConflict, "already_setup")
}

func TestSetupConcurrent(t *testing.T) {
	h := newHarness(t)
	codes := make([]int, 10)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = h.do("POST", "/api/v1/setup", setupBody("keyfile")).Code
		}()
	}
	wg.Wait()
	count := map[int]int{}
	for _, c := range codes {
		count[c]++
	}
	if count[http.StatusCreated] != 1 || count[http.StatusConflict] != 9 {
		t.Fatalf("status codes = %v, want one 201 and nine 409", count)
	}
}

func TestSetupWeakPassword(t *testing.T) {
	h := newHarness(t)
	b := setupBody("keyfile")
	b["password"] = "123456789"
	wantField(t, wantError(t, h.do("POST", "/api/v1/setup", b), http.StatusBadRequest, "validation"), "password")
}

func TestSetupHugePassword(t *testing.T) {
	h := newHarness(t)
	b := setupBody("keyfile")
	b["password"] = strings.Repeat("x", 1025)
	wantField(t, wantError(t, h.do("POST", "/api/v1/setup", b), http.StatusBadRequest, "validation"), "password")
	if n := h.metaRows(); n != 0 {
		t.Errorf("meta has %d rows after a rejected setup, want 0", n)
	}
	if h.status()["setup_required"] != true {
		t.Error("setup_required flipped after a rejected setup")
	}
}

func TestSetupBadNodeName(t *testing.T) {
	h := newHarness(t)
	b := setupBody("keyfile")
	b["node_name"] = "NAS 1"
	wantField(t, wantError(t, h.do("POST", "/api/v1/setup", b), http.StatusBadRequest, "validation"), "node_name")
}

func TestSetupPassphraseMode(t *testing.T) {
	h := newHarness(t)
	b := setupBody("passphrase")
	delete(b, "passphrase")
	wantField(t, wantError(t, h.do("POST", "/api/v1/setup", b), http.StatusBadRequest, "validation"), "passphrase")
	b["unlock_mode"] = "telepathy"
	wantField(t, wantError(t, h.do("POST", "/api/v1/setup", b), http.StatusBadRequest, "validation"), "unlock_mode")
	if n := h.metaRows(); n != 0 {
		t.Fatalf("meta has %d rows after rejected setups", n)
	}
	h.setup("passphrase")
	if s := h.status(); s["vault_state"] != "unlocked" {
		t.Errorf("status = %v", s)
	}
	if entries, _ := os.ReadDir(h.secrets); len(entries) != 0 {
		t.Error("passphrase mode wrote into the secrets dir")
	}
}

// ---- login and sessions ----

func TestLoginWrong(t *testing.T) {
	h := newHarness(t)
	h.setup("keyfile")
	h.cookie, h.csrf = "", ""
	w := h.do("POST", "/api/v1/login", map[string]any{"password": "wrong password!"})
	wantError(t, w, http.StatusUnauthorized, "unauthorized")
	if sessionCookie(w) != nil {
		t.Error("a failed login set a session cookie")
	}
	h.login()
	if h.status()["authenticated"] != true {
		t.Error("not authenticated after a correct login")
	}
}

func TestLoginRateLimited(t *testing.T) {
	h := newHarness(t)
	h.setup("keyfile")
	h.cookie, h.csrf = "", ""
	bad := map[string]any{"password": "wrong password!"}
	for i := range 5 {
		if w := h.do("POST", "/api/v1/login", bad); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i+1, w.Code)
		}
	}
	w := h.do("POST", "/api/v1/login", bad)
	wantError(t, w, http.StatusTooManyRequests, "rate_limited")
	if got := w.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}
	// The right password is refused too while locked, and works afterwards.
	wantError(t, h.do("POST", "/api/v1/login", map[string]any{"password": adminPass}), http.StatusTooManyRequests, "rate_limited")
	h.now = h.now.Add(61 * time.Second)
	h.login()
}

func TestLoginConcurrentWrong(t *testing.T) {
	h := newHarness(t)
	h.setup("keyfile")
	h.cookie, h.csrf = "", ""
	codes := make([]int, 20)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = h.do("POST", "/api/v1/login", map[string]any{"password": "wrong password!"}).Code
		}()
	}
	wg.Wait()
	count := map[int]int{}
	for _, c := range codes {
		count[c]++
	}
	if count[http.StatusUnauthorized] != 5 || count[http.StatusTooManyRequests] != 15 {
		t.Fatalf("status codes = %v, want five 401 and fifteen 429: parallel guesses must not outrun the limiter", count)
	}
}

func TestLoginHugePassword(t *testing.T) {
	h := newHarness(t)
	h.setup("keyfile")
	h.cookie, h.csrf = "", ""
	huge := map[string]any{"password": strings.Repeat("x", 1025)}
	for range 6 {
		wantField(t, wantError(t, h.do("POST", "/api/v1/login", huge), http.StatusBadRequest, "validation"), "password")
	}
	h.login() // six oversized attempts did not count as failures
}

func TestProtectedNeedsSession(t *testing.T) {
	h := newHarness(t)
	h.setup("keyfile")
	h.cookie, h.csrf = "", ""
	wantError(t, h.do("GET", "/api/v1/session", nil), http.StatusUnauthorized, "unauthorized")
	h.cookie = "forged-token"
	wantError(t, h.do("GET", "/api/v1/session", nil), http.StatusUnauthorized, "unauthorized")
}

func TestSessionReturnsCSRF(t *testing.T) {
	h := newHarness(t)
	h.setup("keyfile")
	w := h.do("GET", "/api/v1/session", nil)
	if w.Code != http.StatusOK || decode(t, w)["csrf_token"] != h.csrf {
		t.Fatalf("session = %d %s, want csrf_token %q", w.Code, w.Body, h.csrf)
	}
}

func TestWriteNeedsCSRF(t *testing.T) {
	h := newHarness(t)
	h.setup("keyfile")
	wantError(t, h.do("POST", "/api/v1/logout", nil, noCSRF()), http.StatusForbidden, "csrf")
	wantError(t, h.do("POST", "/api/v1/logout", nil, header("X-CSRF-Token", "wrong")), http.StatusForbidden, "csrf")
	if w := h.do("POST", "/api/v1/logout", nil); w.Code != http.StatusNoContent {
		t.Fatalf("logout with CSRF = %d %s", w.Code, w.Body)
	}
}

func TestLogoutInvalidates(t *testing.T) {
	h := newHarness(t)
	h.setup("keyfile")
	if w := h.do("POST", "/api/v1/logout", nil); w.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", w.Code)
	}
	wantError(t, h.do("GET", "/api/v1/session", nil), http.StatusUnauthorized, "unauthorized")
}

func TestSecureCookieBehindTLS(t *testing.T) {
	h := newHarness(t)
	w := h.do("POST", "/api/v1/setup", setupBody("keyfile"))
	if c := sessionCookie(w); c == nil || c.Secure {
		t.Fatalf("plain HTTP cookie = %+v, want one without Secure", c)
	}
	w = h.do("POST", "/api/v1/login", map[string]any{"password": adminPass}, header("X-Forwarded-Proto", "https"))
	if c := sessionCookie(w); c == nil || !c.Secure {
		t.Fatalf("cookie behind TLS = %+v, want Secure", c)
	}
}

// ---- vault guards ----

func (h *harness) addGuardedRoute() {
	h.api.Handle("GET /api/v1/test-unlocked", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}, NeedSession, NeedUnlocked)
}

func TestUnlockFlow(t *testing.T) {
	h := newHarness(t)
	h.setup("passphrase")
	h.restart()
	h.addGuardedRoute()
	if s := h.status(); s["vault_state"] != "locked" {
		t.Fatalf("status after restart = %v", s)
	}
	h.login()
	wantError(t, h.do("GET", "/api/v1/test-unlocked", nil), http.StatusServiceUnavailable, "vault_locked")
	wantError(t, h.do("POST", "/api/v1/unlock", map[string]any{"passphrase": "not the passphrase"}), http.StatusForbidden, "forbidden")
	wantField(t, wantError(t, h.do("POST", "/api/v1/unlock", map[string]any{"passphrase": strings.Repeat("x", 1025)}), http.StatusBadRequest, "validation"), "passphrase")
	if w := h.do("POST", "/api/v1/unlock", map[string]any{"passphrase": vaultPass}); w.Code != http.StatusNoContent {
		t.Fatalf("unlock = %d %s", w.Code, w.Body)
	}
	if w := h.do("GET", "/api/v1/test-unlocked", nil); w.Code != http.StatusOK {
		t.Fatalf("guarded route after unlock = %d %s", w.Code, w.Body)
	}
}

func TestUnlockNeedsSession(t *testing.T) {
	h := newHarness(t)
	h.setup("passphrase")
	h.restart()
	wantError(t, h.do("POST", "/api/v1/unlock", map[string]any{"passphrase": vaultPass}), http.StatusUnauthorized, "unauthorized")
}

func TestVaultUnavailable(t *testing.T) {
	h := newHarness(t)
	h.setup("keyfile")
	if err := os.Remove(filepath.Join(h.secrets, "master.key")); err != nil {
		t.Fatal(err)
	}
	h.restart()
	h.addGuardedRoute()
	if s := h.status(); s["vault_state"] != "keyfile_missing" {
		t.Fatalf("status = %v", s)
	}
	h.login()
	wantError(t, h.do("GET", "/api/v1/test-unlocked", nil), http.StatusServiceUnavailable, "vault_unavailable")
	wantError(t, h.do("POST", "/api/v1/unlock", map[string]any{"passphrase": vaultPass}), http.StatusServiceUnavailable, "vault_unavailable")
	if _, err := os.Stat(filepath.Join(h.secrets, "master.key")); !os.IsNotExist(err) {
		t.Error("a key file was created while the vault was unavailable")
	}
}

// ---- cross-cutting ----

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	for _, w := range []*httptest.ResponseRecorder{
		h.do("GET", "/api/v1/status", nil),
		h.do("GET", "/api/v1/session", nil),
		h.do("GET", "/api/v1/no-such-route", nil),
		h.do("POST", "/api/v1/setup", "{"),
	} {
		if got := w.Header().Get("Content-Security-Policy"); got != wantCSP {
			t.Errorf("CSP = %q", got)
		}
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q", got)
		}
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	h := newHarness(t)
	wantError(t, h.do("GET", "/api/v1/no-such-route", nil), http.StatusNotFound, "not_found")
	wantError(t, h.do("DELETE", "/api/v1/status", nil), http.StatusNotFound, "not_found")
}

func TestPanicBecomes500(t *testing.T) {
	h := newHarness(t)
	h.api.Handle("GET /api/v1/test-panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
	wantError(t, h.do("GET", "/api/v1/test-panic", nil), http.StatusInternalServerError, "internal")
}

func TestNoHashInResponses(t *testing.T) {
	h := newHarness(t)
	var bodies []string
	w := h.do("POST", "/api/v1/setup", setupBody("keyfile"))
	h.adopt(w)
	bodies = append(bodies, w.Body.String())
	bodies = append(bodies, h.do("POST", "/api/v1/login", map[string]any{"password": adminPass}).Body.String())
	bodies = append(bodies, h.do("GET", "/api/v1/status", nil).Body.String())
	bodies = append(bodies, h.do("GET", "/api/v1/session", nil).Body.String())
	for i, b := range bodies {
		if b == "" {
			t.Errorf("response %d is empty", i)
		}
		if strings.Contains(b, "$argon2id$") || strings.Contains(b, adminPass) {
			t.Errorf("response %d leaks the password or its hash: %s", i, b)
		}
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	h := newHarness(t)
	b := setupBody("keyfile")
	b["is_admin"] = true
	wantError(t, h.do("POST", "/api/v1/setup", b), http.StatusBadRequest, "bad_request")
	wantError(t, h.do("POST", "/api/v1/setup", `{"password":"x"} trailing`), http.StatusBadRequest, "bad_request")
	raw, _ := json.Marshal(setupBody("keyfile"))
	wantError(t, h.do("POST", "/api/v1/setup", string(raw), header("Content-Type", "text/plain")), http.StatusBadRequest, "bad_request")
	if n := h.metaRows(); n != 0 {
		t.Errorf("meta has %d rows after rejected requests", n)
	}
}

func TestBodyTooLarge(t *testing.T) {
	h := newHarness(t)
	big := `{"password":"` + strings.Repeat("x", 2<<20) + `"}`
	wantError(t, h.do("POST", "/api/v1/setup", big), http.StatusBadRequest, "bad_request")
}

// ---- interrupted setup (final review, Important 1) ----

// A client that disconnects mid-request must not leave setup half done.
func TestSetupSurvivesClientDisconnect(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.do("POST", "/api/v1/setup", setupBody("keyfile"), func(r *http.Request) { *r = *r.WithContext(ctx) })
	if n := h.metaRows(); n != 0 && h.status()["setup_required"] != false {
		t.Fatalf("setup stopped half way: %d meta rows but setup_required is still true", n)
	}
	if h.status()["setup_required"] != false {
		t.Fatal("setup did not complete for a request whose client went away")
	}
}

// The vault was initialized by an attempt that died before the admin password
// was stored. A retry must finish the job with the retry's choices.
func TestSetupResumesAfterHalfFinishedAttempt(t *testing.T) {
	ctx := context.Background()
	t.Run("same process, new passphrase wins", func(t *testing.T) {
		h := newHarness(t)
		if err := h.vault.Init(ctx, vault.ModePassphrase, "the first attempt's phrase"); err != nil {
			t.Fatal(err)
		}
		h.setup("passphrase")
		h.restart()
		h.login()
		wantError(t, h.do("POST", "/api/v1/unlock", map[string]any{"passphrase": "the first attempt's phrase"}), http.StatusForbidden, "forbidden")
		if w := h.do("POST", "/api/v1/unlock", map[string]any{"passphrase": vaultPass}); w.Code != http.StatusNoContent {
			t.Fatalf("unlock with the retry's passphrase = %d %s", w.Code, w.Body)
		}
	})
	t.Run("same process, mode can change", func(t *testing.T) {
		h := newHarness(t)
		if err := h.vault.Init(ctx, vault.ModePassphrase, "the first attempt's phrase"); err != nil {
			t.Fatal(err)
		}
		h.setup("keyfile")
		h.restart()
		if s := h.status(); s["vault_state"] != "unlocked" || s["setup_required"] != false {
			t.Fatalf("status = %v", s)
		}
	})
	t.Run("after restart, keyfile", func(t *testing.T) {
		h := newHarness(t)
		if err := h.vault.Init(ctx, vault.ModeKeyfile, ""); err != nil {
			t.Fatal(err)
		}
		h.restart()
		h.setup("keyfile")
	})
	t.Run("after restart, locked vault needs the original passphrase", func(t *testing.T) {
		h := newHarness(t)
		if err := h.vault.Init(ctx, vault.ModePassphrase, vaultPass); err != nil {
			t.Fatal(err)
		}
		h.restart()
		b := setupBody("passphrase")
		b["passphrase"] = "not the original one"
		wantError(t, h.do("POST", "/api/v1/setup", b), http.StatusConflict, "conflict")
		wantError(t, h.do("POST", "/api/v1/setup", setupBody("keyfile")), http.StatusConflict, "conflict")
		if h.status()["setup_required"] != true {
			t.Fatal("a refused retry completed setup")
		}
		h.setup("passphrase")
	})
}
