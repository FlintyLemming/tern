package webdavx

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tern/internal/model"
)

func wantKind(t *testing.T, err error, want model.ErrKind) {
	t.Helper()
	ke, ok := err.(*model.KindError)
	if !ok {
		t.Fatalf("err = %v (%T), want *model.KindError of kind %s", err, err, want)
	}
	if ke.Kind != want {
		t.Fatalf("kind = %s (%v), want %s", ke.Kind, err, want)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaks the password: %v", err)
	}
}

func statusServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebDAVOK(t *testing.T) {
	var seen *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer srv.Close()
	if err := Check(context.Background(), srv.URL+"/dav/", "me", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if seen.Method != "PROPFIND" || seen.Header.Get("Depth") != "0" || seen.URL.Path != "/dav/" {
		t.Errorf("request = %s %s, Depth %q", seen.Method, seen.URL.Path, seen.Header.Get("Depth"))
	}
	if u, p, ok := seen.BasicAuth(); !ok || u != "me" || p != "s3cret" {
		t.Errorf("basic auth = %q %q %v", u, p, ok)
	}
}

func TestWebDAVAnonymous(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hadAuth = r.Header.Get("Authorization") != ""
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer srv.Close()
	if err := Check(context.Background(), srv.URL, "", ""); err != nil || hadAuth {
		t.Errorf("err = %v, hadAuth = %v", err, hadAuth)
	}
}

func TestWebDAVAuth(t *testing.T) {
	for _, st := range []int{401, 403} {
		wantKind(t, Check(context.Background(), statusServer(t, st).URL, "me", "s3cret"), model.KindAuth)
	}
}

func TestWebDAVNotDAV(t *testing.T) {
	for _, st := range []int{405, 404, 200, 500} {
		wantKind(t, Check(context.Background(), statusServer(t, st).URL, "me", "s3cret"), model.KindConfig)
	}
}

func TestWebDAVGateway(t *testing.T) {
	for _, st := range []int{502, 503, 504} {
		wantKind(t, Check(context.Background(), statusServer(t, st).URL, "me", "s3cret"), model.KindNetwork)
	}
}

func TestWebDAVDown(t *testing.T) {
	srv := statusServer(t, 207)
	url := srv.URL
	srv.Close()
	wantKind(t, Check(context.Background(), url, "me", "s3cret"), model.KindNetwork)
}

func TestWebDAVHang(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	wantKind(t, Check(ctx, srv.URL, "me", "s3cret"), model.KindNetwork)
	if d := time.Since(start); d >= 2*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestWebDAVBadCert(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(207) }))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshake is the point
	srv.StartTLS()
	defer srv.Close()
	wantKind(t, Check(context.Background(), srv.URL, "me", "s3cret"), model.KindConfig)
}

func TestWebDAVNoRedirectLeak(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(207)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/dav/", http.StatusFound)
	}))
	defer srv.Close()
	wantKind(t, Check(context.Background(), srv.URL, "me", "s3cret"), model.KindConfig)
	if hits.Load() != 0 {
		t.Error("the redirect was followed to another host")
	}
}

func TestWebDAVBadURL(t *testing.T) {
	wantKind(t, Check(context.Background(), "://nope", "me", "s3cret"), model.KindConfig)
}
