package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrate applies every embedded migration the database has not seen yet.
// PRAGMA user_version counts the files applied so far.
func migrate(ctx context.Context, db *sql.DB) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var have int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&have); err != nil {
		return err
	}
	if have > len(names) {
		return fmt.Errorf("%w: schema version %d, this binary knows up to %d; run the newer version or restore a backup",
			ErrNewerSchema, have, len(names))
	}
	for i := have; i < len(names); i++ {
		body, err := migrationFS.ReadFile("migrations/" + names[i])
		if err != nil {
			return err
		}
		if err := applyMigration(ctx, db, string(body), i+1); err != nil {
			return fmt.Errorf("migration %s: %w", names[i], err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, body string, version int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	// user_version lives in the database header and is transactional.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}
