package qts

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeQTS answers authLogin.cgi like QTS does: sessions maps a sid to
// "admin" or "user"; anything else is not signed in.
type fakeQTS struct {
	sessions map[string]string
	calls    atomic.Int32
	status   int
	body     string // when set, answered verbatim
}

func (f *fakeQTS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if f.body != "" {
		fmt.Fprint(w, f.body)
		return
	}
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8" ?>`+"\n"+`<QDocRoot version="1.0">`)
	switch f.sessions[r.URL.Query().Get("sid")] {
	case "admin":
		fmt.Fprint(w, `<authPassed><![CDATA[1]]></authPassed><username><![CDATA[alice]]></username><isAdmin><![CDATA[1]]></isAdmin>`)
	case "user":
		fmt.Fprint(w, `<authPassed><![CDATA[1]]></authPassed><username><![CDATA[bob]]></username><isAdmin><![CDATA[0]]></isAdmin>`)
	default:
		fmt.Fprint(w, `<authPassed><![CDATA[0]]></authPassed>`)
	}
	fmt.Fprint(w, `</QDocRoot>`)
}

func newChecker(t *testing.T, f *fakeQTS) (*Checker, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	return New(srv.URL+"/cgi-bin/authLogin.cgi", func() time.Time { return now }), &now
}

func TestAdmin(t *testing.T) {
	f := &fakeQTS{sessions: map[string]string{"adminsid1": "admin", "usersid1": "user"}}
	c, _ := newChecker(t, f)
	for _, tc := range []struct {
		sid, user string
		ok        bool
	}{
		{"adminsid1", "alice", true},
		{"usersid1", "bob", false},
		{"expired1", "", false},
	} {
		user, ok, err := c.Admin(context.Background(), tc.sid)
		if err != nil || ok != tc.ok || user != tc.user {
			t.Errorf("Admin(%q) = %q, %v, %v; want %q, %v", tc.sid, user, ok, err, tc.user, tc.ok)
		}
	}
}

func TestMalformedSIDNeverAsked(t *testing.T) {
	f := &fakeQTS{sessions: map[string]string{}}
	c, _ := newChecker(t, f)
	long := make([]byte, maxSIDLen+1)
	for i := range long {
		long[i] = 'a'
	}
	for _, sid := range []string{"", "a&sid=b", "x y", "../x", string(long)} {
		if _, ok, err := c.Admin(context.Background(), sid); ok || err != nil {
			t.Errorf("Admin(%q) = %v, %v; want not signed in", sid, ok, err)
		}
	}
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("authLogin.cgi asked %d times, want 0", n)
	}
}

func TestAnswersAreCached(t *testing.T) {
	f := &fakeQTS{sessions: map[string]string{"adminsid1": "admin"}}
	c, now := newChecker(t, f)
	ctx := context.Background()
	for range 3 {
		if _, ok, _ := c.Admin(ctx, "adminsid1"); !ok {
			t.Fatal("admin not signed in")
		}
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("authLogin.cgi asked %d times within the TTL, want 1", n)
	}

	// Signed out of QTS: noticed once the cached answer has expired.
	delete(f.sessions, "adminsid1")
	*now = now.Add(CacheTTL - time.Second)
	if _, ok, _ := c.Admin(ctx, "adminsid1"); !ok {
		t.Fatal("cached answer dropped before the TTL")
	}
	*now = now.Add(time.Second)
	if _, ok, _ := c.Admin(ctx, "adminsid1"); ok {
		t.Fatal("still signed in after the TTL although QTS says no")
	}
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("authLogin.cgi asked %d times, want 2", n)
	}
}

func TestCacheStaysBounded(t *testing.T) {
	f := &fakeQTS{sessions: map[string]string{}}
	c, _ := newChecker(t, f)
	for i := range maxCached * 3 {
		c.Admin(context.Background(), fmt.Sprintf("sid%d", i))
	}
	c.mu.Lock()
	n := len(c.cache)
	c.mu.Unlock()
	if n > maxCached {
		t.Fatalf("cache holds %d entries, want at most %d", n, maxCached)
	}
}

func TestFailuresAreErrorsAndNotCached(t *testing.T) {
	for name, f := range map[string]*fakeQTS{
		"http error":    {status: http.StatusInternalServerError},
		"not xml":       {body: "<html>oops"},
		"no authPassed": {body: `<QDocRoot version="1.0"><hostname>x</hostname></QDocRoot>`},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := newChecker(t, f)
			for range 2 {
				if _, ok, err := c.Admin(context.Background(), "adminsid1"); ok || err == nil {
					t.Fatalf("Admin = %v, %v; want an error", ok, err)
				}
			}
			if n := f.calls.Load(); n != 2 {
				t.Fatalf("authLogin.cgi asked %d times, want 2 (failures are not cached)", n)
			}
		})
	}
}

func TestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c := New(srv.URL, nil)
	if _, ok, err := c.Admin(context.Background(), "adminsid1"); ok || err == nil {
		t.Fatalf("Admin = %v, %v; want an error", ok, err)
	}
}
