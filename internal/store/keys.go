package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"modernc.org/sqlite"

	"tern/internal/model"
)

// SQLite extended result codes for the constraint failures the store maps.
const (
	sqliteConstraintForeignKey = 787
	sqliteConstraintTrigger    = 1811 // what an ON DELETE RESTRICT action reports
	sqliteConstraintUnique     = 2067
)

func isConstraint(err error, code int) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == code
}

// versionMiss tells apart "no such row" from "row exists, but not at that
// version" after a guarded UPDATE touched nothing.
func (s *Store) versionMiss(ctx context.Context, table, id string) error {
	var one int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM "+table+" WHERE id = ?", id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return ErrVersion
}

const keyColumns = "id, version, name, public_key, fingerprint, passphrase <> '', created_at, updated_at"

func scanKey(row interface{ Scan(...any) error }) (model.SSHKey, error) {
	var (
		k                model.SSHKey
		created, updated int64
	)
	err := row.Scan(&k.ID, &k.Version, &k.Name, &k.PublicKey, &k.Fingerprint, &k.HasPassphrase, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SSHKey{}, ErrNotFound
	}
	k.CreatedAt, k.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return k, err
}

// CreateKey stores k with its sealed private key and sealed passphrase
// ("" when the key has none). A taken name is ErrDuplicate.
func (s *Store) CreateKey(ctx context.Context, k model.SSHKey, privSealed, passSealed string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys
		(id, version, name, public_key, fingerprint, private_key, passphrase, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.Version, k.Name, k.PublicKey, k.Fingerprint, privSealed, passSealed, k.CreatedAt.Unix(), k.UpdatedAt.Unix())
	if isConstraint(err, sqliteConstraintUnique) {
		return ErrDuplicate
	}
	return err
}

// ListKeys returns every key, oldest first.
func (s *Store) ListKeys(ctx context.Context) ([]model.SSHKey, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+keyColumns+" FROM ssh_keys ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []model.SSHKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) GetKey(ctx context.Context, id string) (model.SSHKey, error) {
	return scanKey(s.db.QueryRowContext(ctx, "SELECT "+keyColumns+" FROM ssh_keys WHERE id = ?", id))
}

// KeySecrets returns the sealed private key and sealed passphrase of a key.
func (s *Store) KeySecrets(ctx context.Context, id string) (privSealed, passSealed string, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT private_key, passphrase FROM ssh_keys WHERE id = ?", id).Scan(&privSealed, &passSealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return privSealed, passSealed, err
}

// RenameKey renames the key if it is still at version, bumping the version.
func (s *Store) RenameKey(ctx context.Context, id string, version int, name string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE ssh_keys SET name = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ?",
		name, time.Now().Unix(), id, version)
	if isConstraint(err, sqliteConstraintUnique) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return s.versionMiss(ctx, "ssh_keys", id)
	}
	return nil
}

// DeleteKey removes a key; one that a connection still uses is ErrInUse.
func (s *Store) DeleteKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM ssh_keys WHERE id = ?", id)
	if isConstraint(err, sqliteConstraintForeignKey) || isConstraint(err, sqliteConstraintTrigger) {
		return ErrInUse
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
