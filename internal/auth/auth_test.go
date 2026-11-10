package auth

import (
	"context"
	"database/sql"
	"runtime"
	"strings"
	"testing"
	"time"

	"tern/internal/store"
)

// ---- passwords ----

func TestHashVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "correct horse battery") {
		t.Error("Verify rejected the right password")
	}
	if VerifyPassword(h, "correct horse batterz") {
		t.Error("Verify accepted a wrong password")
	}
}

func TestHashFormat(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash %q has the wrong prefix", h)
	}
	if n := strings.Count(h, "$"); n != 5 {
		t.Errorf("hash has %d '$' separators, want 5", n)
	}
}

func TestHashesDiffer(t *testing.T) {
	a, _ := HashPassword("correct horse battery")
	b, _ := HashPassword("correct horse battery")
	if a == b {
		t.Error("two hashes of one password are identical")
	}
}

func TestVerifyGarbage(t *testing.T) {
	for _, enc := range []string{
		"", "not-a-hash", "$argon2id$", "$argon2id$v=19$m=65536,t=3,p=4$$",
		"$argon2id$v=19$m=65536,t=3,p=4$!!!$!!!",
		"$argon2id$v=19$m=0,t=0,p=0$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=4294967295,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2i$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if VerifyPassword(enc, "x") {
			t.Errorf("Verify(%q) = true", enc)
		}
	}
}

func allocDelta(fn func()) uint64 {
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	fn()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func TestHashRejectsHuge(t *testing.T) {
	huge := strings.Repeat("x", MaxSecretLen+1)
	var err error
	d := allocDelta(func() { _, err = HashPassword(huge) })
	if err == nil {
		t.Fatal("HashPassword accepted 1025 bytes")
	}
	if d >= 1<<20 {
		t.Errorf("allocated %d bytes: the input reached Argon2", d)
	}
	if _, err := HashPassword(strings.Repeat("x", MaxSecretLen)); err != nil {
		t.Errorf("HashPassword rejected exactly %d bytes: %v", MaxSecretLen, err)
	}
}

func TestVerifyRejectsHuge(t *testing.T) {
	h, _ := HashPassword("correct horse battery")
	huge := strings.Repeat("x", MaxSecretLen+1)
	var ok bool
	d := allocDelta(func() { ok = VerifyPassword(h, huge) })
	if ok {
		t.Fatal("Verify accepted 1025 bytes")
	}
	if d >= 1<<20 {
		t.Errorf("allocated %d bytes: the input reached Argon2", d)
	}
}

// ---- sessions ----

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newSessions(t *testing.T) (*Sessions, *clock, *sql.DB) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := &clock{t: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	return NewSessions(st.DB(), c.now), c, st.DB()
}

func rowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSessions(t)
	token, csrf, err := s.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 43 || len(csrf) < 43 || token == csrf {
		t.Fatalf("token=%q csrf=%q: want two distinct 32-byte base64url values", token, csrf)
	}
	got, ok, err := s.Lookup(ctx, token)
	if err != nil || !ok || got != csrf {
		t.Fatalf("Lookup = (%q, %v, %v), want (%q, true, nil)", got, ok, err, csrf)
	}
	if _, ok, _ := s.Lookup(ctx, "no-such-token"); ok {
		t.Error("Lookup accepted an unknown token")
	}
	if _, ok, _ := s.Lookup(ctx, ""); ok {
		t.Error("Lookup accepted an empty token")
	}
	if err := s.Delete(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Lookup(ctx, token); ok {
		t.Error("Lookup succeeded after Delete")
	}
}

func TestSessionTokenNotStored(t *testing.T) {
	ctx := context.Background()
	s, _, db := newSessions(t)
	token, _, err := s.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hash, csrf string
	var created, seen int64
	if err := db.QueryRow("SELECT token_hash, csrf, created_at, last_seen_at FROM sessions").Scan(&hash, &csrf, &created, &seen); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, token) || strings.Contains(csrf, token) {
		t.Error("the sessions table contains the raw token")
	}
	if len(hash) != 64 {
		t.Errorf("token_hash %q is not a sha256 hex digest", hash)
	}
}

func TestSessionIdleExpiry(t *testing.T) {
	ctx := context.Background()
	s, c, db := newSessions(t)
	token, _, _ := s.Create(ctx)
	c.advance(23 * time.Hour)
	if _, ok, _ := s.Lookup(ctx, token); !ok {
		t.Fatal("session expired before 24h of idleness")
	}
	c.advance(24*time.Hour + time.Minute)
	if _, ok, _ := s.Lookup(ctx, token); ok {
		t.Fatal("session survived 24h1m of idleness")
	}
	if n := rowCount(t, db); n != 0 {
		t.Errorf("%d rows left, want the expired row deleted", n)
	}
}

func TestSessionAbsoluteExpiry(t *testing.T) {
	ctx := context.Background()
	s, c, db := newSessions(t)
	token, _, _ := s.Create(ctx)
	for i := 1; i <= 7; i++ { // 7 × 23h = 161h < 7 days
		c.advance(23 * time.Hour)
		if _, ok, _ := s.Lookup(ctx, token); !ok {
			t.Fatalf("session expired after %dh, before 7 days", i*23)
		}
	}
	c.advance(23 * time.Hour) // 184h > 168h
	if _, ok, _ := s.Lookup(ctx, token); ok {
		t.Fatal("session outlived 7 days")
	}
	if n := rowCount(t, db); n != 0 {
		t.Errorf("%d rows left, want the expired row deleted", n)
	}
}

func TestSessionDeleteAll(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSessions(t)
	a, _, _ := s.Create(ctx)
	b, _, _ := s.Create(ctx)
	if err := s.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{a, b} {
		if _, ok, _ := s.Lookup(ctx, tok); ok {
			t.Error("a session survived DeleteAll")
		}
	}
}

// ---- limiter ----

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func failN(l *Limiter, ip string, n int, now time.Time) {
	for range n {
		l.Fail(ip, now)
	}
}

func TestLimiterLocksAfterFive(t *testing.T) {
	l := NewLimiter()
	failN(l, "10.0.0.1", 4, t0)
	if ok, _ := l.Allow("10.0.0.1", t0); !ok {
		t.Fatal("locked after only 4 failures")
	}
	l.Fail("10.0.0.1", t0)
	if ok, wait := l.Allow("10.0.0.1", t0); ok || wait != time.Minute {
		t.Fatalf("Allow = (%v, %v), want (false, 1m)", ok, wait)
	}
	if ok, wait := l.Allow("10.0.0.1", t0.Add(20*time.Second)); ok || wait != 40*time.Second {
		t.Fatalf("Allow at +20s = (%v, %v), want (false, 40s)", ok, wait)
	}
	if ok, _ := l.Allow("10.0.0.1", t0.Add(time.Minute)); !ok {
		t.Fatal("still locked after 1m")
	}
}

func TestLimiterDoubles(t *testing.T) {
	l := NewLimiter()
	now := t0
	failN(l, "ip", 5, now)
	now = now.Add(time.Minute)
	for _, want := range []time.Duration{2, 4, 8, 15, 15} {
		want *= time.Minute
		if ok, _ := l.Allow("ip", now); !ok {
			t.Fatalf("not released before the %v lock", want)
		}
		l.Fail("ip", now)
		if ok, wait := l.Allow("ip", now); ok || wait != want {
			t.Fatalf("Allow = (%v, %v), want (false, %v)", ok, wait, want)
		}
		now = now.Add(want)
	}
}

func TestLimiterReset(t *testing.T) {
	l := NewLimiter()
	failN(l, "ip", 4, t0)
	l.Reset("ip")
	failN(l, "ip", 4, t0)
	if ok, _ := l.Allow("ip", t0); !ok {
		t.Fatal("locked although Reset cleared the count")
	}
}

func TestLimiterPerIP(t *testing.T) {
	l := NewLimiter()
	failN(l, "10.0.0.1", 5, t0)
	if ok, _ := l.Allow("10.0.0.1", t0); ok {
		t.Fatal("10.0.0.1 is not locked")
	}
	if ok, _ := l.Allow("10.0.0.2", t0); !ok {
		t.Fatal("10.0.0.2 is locked by 10.0.0.1's failures")
	}
}
