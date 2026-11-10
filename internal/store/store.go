// Package store owns the SQLite database: opening, migrating and per-table access.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
)

// Store is the open database plus the single-instance lock on its directory.
type Store struct {
	db   *sql.DB
	lock *os.File
}

// Open locks dataDir, opens <dataDir>/tern.db and applies pending migrations.
// It fails with ErrLockHeld when another process holds the directory and with
// ErrNewerSchema when the database was written by a newer binary.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	lock, err := acquireLock(filepath.Join(dataDir, "tern.lock"))
	if err != nil {
		return nil, fmt.Errorf("open data directory %s: %w", dataDir, err)
	}
	db, err := openDB(ctx, filepath.Join(dataDir, "tern.db"))
	if err != nil {
		releaseLock(lock)
		return nil, fmt.Errorf("open data directory %s: %w", dataDir, err)
	}
	return &Store{db: db, lock: lock}, nil
}

func openDB(ctx context.Context, path string) (*sql.DB, error) {
	// Create the file ourselves so it is 0600 from the first byte; SQLite gives
	// the -wal and -shm files the same mode as the main file.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}

	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}

func releaseLock(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()
}

// Close closes the database and releases the directory lock.
func (s *Store) Close() error {
	err := s.db.Close()
	releaseLock(s.lock)
	return err
}

// DB exposes the handle for packages that own their own tables.
func (s *Store) DB() *sql.DB { return s.db }

// Tx runs fn in a transaction, committing when fn returns nil and rolling back
// otherwise. The store has a single connection: fn must use tx, never s.
func (s *Store) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit; covers errors and panics
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
