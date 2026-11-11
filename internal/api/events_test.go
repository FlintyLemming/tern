package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tern/internal/engine"
)

func engineRequest() engine.Request { return engine.Request{} }

// sseStream is an open GET /api/v1/events.
type sseStream struct {
	t      *testing.T
	lines  chan string
	cancel context.CancelFunc
	srv    *httptest.Server
}

func (h *harness) openSSE() *sseStream {
	h.t.Helper()
	srv := httptest.NewServer(h.api)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events", nil)
	req.AddCookie(&http.Cookie{Name: "tern_session", Value: h.cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		h.t.Fatalf("GET /events = %d, Content-Type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	s := &sseStream{t: h.t, lines: make(chan string, 256), cancel: cancel, srv: srv}
	go func() {
		defer close(s.lines)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
	}()
	h.t.Cleanup(s.close)
	if got := s.line(); got != ": ok" { // the subscription is in place
		h.t.Fatalf("first line = %q, want the ': ok' comment", got)
	}
	return s
}

// close disconnects and waits for the handler to return.
func (s *sseStream) close() {
	s.cancel()
	s.srv.Close()
}

// line returns the next non-empty line.
func (s *sseStream) line() string {
	s.t.Helper()
	for {
		select {
		case l, ok := <-s.lines:
			if !ok {
				s.t.Fatal("event stream ended")
			}
			if l != "" {
				return l
			}
		case <-time.After(10 * time.Second):
			s.t.Fatal("timed out reading the event stream")
		}
	}
}

// event returns the next event's name and decoded data.
func (s *sseStream) event() (string, map[string]any) {
	s.t.Helper()
	name := s.line()
	for strings.HasPrefix(name, ":") {
		name = s.line()
	}
	data := s.line()
	if !strings.HasPrefix(name, "event: ") || !strings.HasPrefix(data, "data: ") {
		s.t.Fatalf("malformed event: %q / %q", name, data)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &m); err != nil {
		s.t.Fatalf("event data is not JSON: %v", err)
	}
	return strings.TrimPrefix(name, "event: "), m
}

func TestSSEStreamsRunUpdates(t *testing.T) {
	h := readyHarness(t)
	task := h.newSyncTask("photos")
	s := h.openSSE()
	id := h.runNow(task, nil)
	var statuses []string
	for len(statuses) < 3 {
		name, data := s.event()
		if name != "run.updated" {
			continue
		}
		if data["id"] != id || data["task_name"] != "photos" {
			t.Fatalf("run.updated for another run: %v", data)
		}
		statuses = append(statuses, data["status"].(string))
	}
	if strings.Join(statuses, ",") != "queued,running,success" {
		t.Errorf("statuses = %v", statuses)
	}
}

func TestSSETaskUpdated(t *testing.T) {
	h := readyHarness(t)
	connID := h.sshConnID()
	s := h.openSSE()
	task := h.newTask(syncBody("photos", connID))
	if name, data := s.event(); name != "task.updated" || data["task_id"] != task["id"] {
		t.Errorf("event = %s %v", name, data)
	}
}

func TestSSEPing(t *testing.T) {
	h := readyHarness(t)
	s := h.openSSE()
	h.clk.BlockUntil(2) // the scheduler's timer and this stream's ping timer
	h.clk.Advance(20 * time.Second)
	if got := s.line(); got != ": ping" {
		t.Errorf("line = %q, want ': ping'", got)
	}
}

func TestSSEClientDisconnect(t *testing.T) {
	h := readyHarness(t)
	before := h.hub.Subscribers()
	s := h.openSSE()
	if n := h.hub.Subscribers(); n != before+1 {
		t.Fatalf("Subscribers = %d with one stream open, want %d", n, before+1)
	}
	s.close()
	if n := h.hub.Subscribers(); n != before {
		t.Errorf("Subscribers = %d after the client left, want %d", n, before)
	}
}

func TestSSENeedsSession(t *testing.T) {
	h := readyHarness(t)
	h.cookie = ""
	wantError(t, h.do("GET", "/api/v1/events", nil), http.StatusUnauthorized, "unauthorized")
}
