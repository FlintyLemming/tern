package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"golang.org/x/crypto/ssh"

	"tern/internal/model"
)

const connColumns = "id, version, type, name, host, port, username, ssh_key_id, host_key, url, password <> '', created_at, updated_at"

func scanConnection(row interface{ Scan(...any) error }) (model.Connection, error) {
	var (
		c                model.Connection
		keyID            sql.NullString
		created, updated int64
	)
	err := row.Scan(&c.ID, &c.Version, &c.Type, &c.Name, &c.Host, &c.Port, &c.Username, &keyID,
		&c.HostKey, &c.URL, &c.HasPassword, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Connection{}, ErrNotFound
	}
	if err != nil {
		return model.Connection{}, err
	}
	c.SSHKeyID = keyID.String
	c.CreatedAt, c.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	if c.HostKey != "" {
		if key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.HostKey)); err == nil {
			c.HostKeyFingerprint = ssh.FingerprintSHA256(key)
		}
	}
	return c, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateConnection stores c; passwordSealed is "" when there is no password.
// A taken name is ErrDuplicate.
func (s *Store) CreateConnection(ctx context.Context, c model.Connection, passwordSealed string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO connections
		(id, version, type, name, host, port, username, ssh_key_id, host_key, url, password, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Version, c.Type, c.Name, c.Host, c.Port, c.Username, nullable(c.SSHKeyID), c.HostKey, c.URL,
		passwordSealed, c.CreatedAt.Unix(), c.UpdatedAt.Unix())
	if isConstraint(err, sqliteConstraintUnique) {
		return ErrDuplicate
	}
	return err
}

// UpdateConnection rewrites the editable fields of c.ID if it is still at
// c.Version, bumping the version. The type and creation time never change.
// passwordSealed nil leaves the stored password alone.
func (s *Store) UpdateConnection(ctx context.Context, c model.Connection, passwordSealed *string) error {
	q := `UPDATE connections SET version = version + 1, name = ?, host = ?, port = ?, username = ?,
		ssh_key_id = ?, host_key = ?, url = ?, updated_at = ?`
	args := []any{c.Name, c.Host, c.Port, c.Username, nullable(c.SSHKeyID), c.HostKey, c.URL, c.UpdatedAt.Unix()}
	if passwordSealed != nil {
		q += ", password = ?"
		args = append(args, *passwordSealed)
	}
	res, err := s.db.ExecContext(ctx, q+" WHERE id = ? AND version = ?", append(args, c.ID, c.Version)...)
	if isConstraint(err, sqliteConstraintUnique) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return s.versionMiss(ctx, "connections", c.ID)
	}
	return nil
}

func (s *Store) GetConnection(ctx context.Context, id string) (model.Connection, error) {
	return scanConnection(s.db.QueryRowContext(ctx, "SELECT "+connColumns+" FROM connections WHERE id = ?", id))
}

// ListConnections returns every connection, oldest first.
func (s *Store) ListConnections(ctx context.Context) ([]model.Connection, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+connColumns+" FROM connections ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	conns := []model.Connection{}
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, rows.Err()
}

// ConnectionPassword returns the sealed password, "" when none is set.
func (s *Store) ConnectionPassword(ctx context.Context, id string) (sealed string, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT password FROM connections WHERE id = ?", id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return sealed, err
}

// DeleteConnection removes a connection; one still referenced is ErrInUse.
func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM connections WHERE id = ?", id)
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
