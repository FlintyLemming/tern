package store

import (
	"context"
	"database/sql"
	"errors"
)

const upsertMeta = `INSERT INTO meta (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value`

// GetMeta returns the value stored under key; ok is false when there is none.
func (s *Store) GetMeta(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetMeta stores value under key, replacing any previous value.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, upsertMeta, key, value)
	return err
}

// SetMetaTx is SetMeta inside a transaction started by Store.Tx.
func SetMetaTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, upsertMeta, key, value)
	return err
}

// DeleteMetaTx removes key inside a transaction; a missing key is not an error.
func DeleteMetaTx(ctx context.Context, tx *sql.Tx, key string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM meta WHERE key = ?", key)
	return err
}
