package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tern/internal/model"
	"tern/internal/store"
)

func TestStartInterruptsLeftoverRuns(t *testing.T) {
	cfg := testCfg(t)
	a := start(t, cfg) // creates the database
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	left := model.Run{ID: model.NewID(), TaskID: "t", TaskName: "t", Kind: model.RunSync, Trigger: model.TriggerSchedule,
		Status: model.StatusQueued, StartedAt: time.Now()}
	if err := st.CreateRun(context.Background(), left); err != nil {
		t.Fatal(err)
	}
	st.Close()

	a = start(t, cfg)
	a.Shutdown(context.Background())
	st, err = store.Open(context.Background(), cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got, err := st.GetRun(context.Background(), left.ID); err != nil || got.Status != model.StatusInterrupted {
		t.Errorf("leftover run = %+v, %v; want interrupted", got, err)
	}
}

// client talks to a running App like the web UI does.
type client struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

func (c *client) call(method, path string, body any, want int) map[string]any {
	c.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s = %d %v, want %d", method, path, resp.StatusCode, m, want)
	}
	return m
}

// The whole chain through the real process wiring: setup resumes the
// scheduler, a task is queued and executed by the real sync engine, the
// result is stored and streamed, and an open event stream does not hold up
// shutdown. The WebDAV server answers the preflight; the local source does
// not exist, so the run fails with a config error before rclone is needed.
func TestRunThroughRealWiring(t *testing.T) {
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer dav.Close()
	a := start(t, testCfg(t))
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: "http://" + a.Addr(), c: &http.Client{Jar: jar}}
	c.csrf = c.call("POST", "/api/v1/setup", map[string]any{"password": "correct horse battery", "node_name": "nas-1"}, 201)["csrf_token"].(string)

	conn := c.call("POST", "/api/v1/connections", map[string]any{"type": "webdav", "name": "dav", "url": dav.URL + "/dav", "username": "me"}, 201)
	task := c.call("POST", "/api/v1/tasks", map[string]any{"type": "sync", "name": "photos", "enabled": true, "sync": map[string]any{
		"protocol": "webdav", "connection_id": conn["id"], "direction": "push", "pairs": []map[string]any{{"local": "/mnt/a", "remote": "a"}}}}, 201)

	resp, err := c.c.Get(c.base + "/api/v1/events")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET /events: %v %v", resp, err)
	}
	defer resp.Body.Close()
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	next := func() string {
		select {
		case l, ok := <-lines:
			if !ok {
				return "<closed>"
			}
			return l
		case <-time.After(10 * time.Second):
			t.Fatal("timed out reading the event stream")
		}
		return ""
	}
	if l := next(); l != ": ok" {
		t.Fatalf("first line %q", l)
	}

	runID := c.call("POST", "/api/v1/tasks/"+task["id"].(string)+"/run", nil, 202)["run_id"].(string)
	var final map[string]any
	for final == nil {
		l := next()
		if l == "<closed>" {
			t.Fatal("stream closed before the run finished")
		}
		if data, ok := strings.CutPrefix(l, "data: "); ok {
			var m map[string]any
			json.Unmarshal([]byte(data), &m)
			if m["id"] == runID && m["finished_at"] != nil {
				final = m
			}
		}
	}
	if final["status"] != "failed" || final["error_kind"] != "config" || final["seq"] != float64(1) ||
		!strings.Contains(final["error_summary"].(string), "local source missing: /mnt/a") {
		t.Errorf("run = %v", final)
	}
	got := c.call("GET", "/api/v1/runs/"+runID, nil, 200)
	if got["status"] != "failed" {
		t.Errorf("stored run = %v", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown with an event stream open: %v", err)
	}
	for l := next(); l != "<closed>"; l = next() {
	}
}
