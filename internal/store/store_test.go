package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func pragma(t *testing.T, s *Store, name string) string {
	t.Helper()
	var v string
	if err := s.DB().QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

func migrationCount(t *testing.T) int {
	t.Helper()
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestOpenCreatesAndMigrates(t *testing.T) {
	s := open(t, t.TempDir())
	n := migrationCount(t)
	if n == 0 {
		t.Fatal("no embedded migrations")
	}
	if got := pragma(t, s, "user_version"); got != itoa(n) {
		t.Errorf("user_version = %s, want %d", got, n)
	}
	if got := pragma(t, s, "journal_mode"); got != "wal" {
		t.Errorf("journal_mode = %s, want wal", got)
	}
	if got := pragma(t, s, "foreign_keys"); got != "1" {
		t.Errorf("foreign_keys = %s, want 1", got)
	}
}

func itoa(n int) string {
	var v string
	if n == 0 {
		return "0"
	}
	for n > 0 {
		v = string(rune('0'+n%10)) + v
		n /= 10
	}
	return v
}

func TestOpenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir)
	if err := s.SetMeta(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	before := pragma(t, s, "user_version")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := open(t, dir)
	if got := pragma(t, s2, "user_version"); got != before {
		t.Errorf("user_version changed: %s → %s", before, got)
	}
	v, ok, err := s2.GetMeta(ctx, "k")
	if err != nil || !ok || v != "v" {
		t.Errorf("GetMeta = (%q, %v, %v), want (v, true, nil)", v, ok, err)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	if err := s.SetMeta(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.GetMeta(ctx, "a"); err != nil || !ok || v != "1" {
		t.Errorf("GetMeta(a) = (%q, %v, %v)", v, ok, err)
	}
	if v, ok, err := s.GetMeta(ctx, "none"); err != nil || ok || v != "" {
		t.Errorf("GetMeta(none) = (%q, %v, %v)", v, ok, err)
	}
	if err := s.SetMeta(ctx, "a", "2"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.GetMeta(ctx, "a"); v != "2" {
		t.Errorf("after overwrite GetMeta(a) = %q, want 2", v)
	}
}

func TestDBFileMode(t *testing.T) {
	dir := t.TempDir()
	open(t, dir)
	fi, err := os.Stat(filepath.Join(dir, "tern.db"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("tern.db mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestSecondOpenFailsWithLock(t *testing.T) {
	dir := t.TempDir()
	open(t, dir)
	s2, err := Open(context.Background(), dir)
	if !errors.Is(err, ErrLockHeld) {
		if s2 != nil {
			s2.Close()
		}
		t.Fatalf("second Open err = %v, want ErrLockHeld", err)
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if _, err := s.DB().Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(context.Background(), dir)
	if !errors.Is(err, ErrNewerSchema) {
		if s2 != nil {
			s2.Close()
		}
		t.Fatalf("Open err = %v, want ErrNewerSchema", err)
	}
	// The refusal must release the lock and leave the database as it was.
	s3 := mustRaw(t, dir)
	defer s3.Close()
	var v int
	if err := s3.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 999 {
		t.Fatalf("user_version = %d (%v), want 999 untouched", v, err)
	}
}

func mustRaw(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "tern.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestUnwritableDirFailsCleanly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	s, err := Open(context.Background(), dir)
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded on a read-only directory")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error %q does not name the directory %s", err, dir)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("directory has %d new entries, want none", len(entries))
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	boom := errors.New("boom")
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if err := SetMetaTx(ctx, tx, "x", "1"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Tx err = %v, want boom", err)
	}
	if _, ok, _ := s.GetMeta(ctx, "x"); ok {
		t.Error("write inside a failed Tx is visible")
	}
	// And a successful Tx commits, including a delete.
	if err := s.Tx(ctx, func(tx *sql.Tx) error { return SetMetaTx(ctx, tx, "x", "2") }); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.GetMeta(ctx, "x"); v != "2" {
		t.Errorf("committed value = %q, want 2", v)
	}
	if err := s.Tx(ctx, func(tx *sql.Tx) error { return DeleteMetaTx(ctx, tx, "x") }); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetMeta(ctx, "x"); ok {
		t.Error("DeleteMetaTx left the key behind")
	}
}
