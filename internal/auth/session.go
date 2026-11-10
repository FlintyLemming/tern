package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

const (
	sessionIdleTimeout = 24 * time.Hour
	sessionMaxAge      = 7 * 24 * time.Hour
)

// SessionMaxAge is the longest a session can live; cookies use it as Max-Age.
const SessionMaxAge = sessionMaxAge

// Sessions stores login sessions in the sessions table. Only the SHA-256 of a
// token is stored, so a leaked database cannot be replayed as a cookie.
type Sessions struct {
	db  *sql.DB
	now func() time.Time
}

func NewSessions(db *sql.DB, now func() time.Time) *Sessions {
	return &Sessions{db: db, now: now}
}

func randomToken() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Create starts a session and returns its cookie token and CSRF token.
func (s *Sessions) Create(ctx context.Context) (token, csrf string, err error) {
	token, csrf = randomToken(), randomToken()
	now := s.now().Unix()
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO sessions (token_hash, csrf, created_at, last_seen_at) VALUES (?, ?, ?, ?)",
		hashToken(token), csrf, now, now)
	if err != nil {
		return "", "", err
	}
	return token, csrf, nil
}

// Lookup returns the session's CSRF token and refreshes its idle timer.
// An expired session is deleted and reported as absent.
func (s *Sessions) Lookup(ctx context.Context, token string) (csrf string, ok bool, err error) {
	if token == "" {
		return "", false, nil
	}
	h := hashToken(token)
	var created, seen int64
	err = s.db.QueryRowContext(ctx,
		"SELECT csrf, created_at, last_seen_at FROM sessions WHERE token_hash = ?", h).
		Scan(&csrf, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	now := s.now()
	if now.Sub(time.Unix(seen, 0)) > sessionIdleTimeout || now.Sub(time.Unix(created, 0)) > sessionMaxAge {
		_, err = s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", h)
		return "", false, err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", now.Unix(), h); err != nil {
		return "", false, err
	}
	return csrf, true, nil
}

// Delete ends one session.
func (s *Sessions) Delete(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", hashToken(token))
	return err
}

// DeleteAll ends every session.
func (s *Sessions) DeleteAll(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions")
	return err
}
