package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"tern/internal/model"
)

// ErrFinalized is returned when a run that already reached a terminal status
// is written to again. Run records are append-only.
var ErrFinalized = errors.New("store: run is already final")

const (
	metaRunSeq = "runs.seq"
	runColumns = `id, COALESCE(seq, 0), task_id, task_name, kind, triggered_by, status, started_at, finished_at,
		attempts, bytes, files, error_kind, error_summary, units`
)

func scanRun(row interface{ Scan(...any) error }) (model.Run, error) {
	var (
		r                 model.Run
		kind, trig, st    string
		started, finished int64
		errKind, units    string
	)
	err := row.Scan(&r.ID, &r.Seq, &r.TaskID, &r.TaskName, &kind, &trig, &st, &started, &finished,
		&r.Attempts, &r.Bytes, &r.Files, &errKind, &r.ErrorSummary, &units)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Run{}, ErrNotFound
	}
	if err != nil {
		return model.Run{}, err
	}
	r.Kind, r.Trigger, r.Status, r.ErrorKind = model.RunKind(kind), model.Trigger(trig), model.Status(st), model.ErrKind(errKind)
	r.StartedAt, r.FinishedAt = fromUnix(started), fromUnix(finished)
	if err := json.Unmarshal([]byte(units), &r.Units); err != nil {
		return model.Run{}, fmt.Errorf("store: run %s has unreadable units: %w", r.ID, err)
	}
	if len(r.Units) == 0 {
		r.Units = nil
	}
	return r, nil
}

// CreateRun inserts a run that is not final yet.
func (s *Store) CreateRun(ctx context.Context, r model.Run) error {
	if r.Status.Terminal() {
		return fmt.Errorf("store: a run is created in progress, not as %q", r.Status)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runs (id, task_id, task_name, kind, triggered_by, status, started_at, attempts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.TaskID, r.TaskName, string(r.Kind), string(r.Trigger), string(r.Status), r.StartedAt.Unix(), r.Attempts)
	return err
}

// finalMiss explains why a write guarded by "seq IS NULL" touched nothing.
func finalMiss(ctx context.Context, q queryer, id string) error {
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM runs WHERE id = ?", id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return ErrFinalized
}

// UpdateRun moves a run between in-progress statuses and records the number
// of attempts. A final run is ErrFinalized.
func (s *Store) UpdateRun(ctx context.Context, id string, st model.Status, attempts int) error {
	if st.Terminal() {
		return fmt.Errorf("store: UpdateRun cannot set the terminal status %q; use FinalizeRun", st)
	}
	res, err := s.db.ExecContext(ctx, "UPDATE runs SET status = ?, attempts = ? WHERE id = ? AND seq IS NULL",
		string(st), attempts, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return finalMiss(ctx, s.db, id)
	}
	return nil
}

func nextSeq(ctx context.Context, tx *sql.Tx) (int64, error) {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", metaRunSeq).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("store: meta %s is %q: %w", metaRunSeq, raw, err)
	}
	return n + 1, nil
}

// FinalizeRun writes the terminal fields of r and gives it the next sequence
// number. The counter lives in meta, so numbers are never reused, whatever
// is pruned. A successful backup or sync also moves the task's
// last_success_at. A run that is already final is ErrFinalized.
func (s *Store) FinalizeRun(ctx context.Context, r model.Run) (seq int64, err error) {
	if !r.Status.Terminal() {
		return 0, fmt.Errorf("store: FinalizeRun needs a terminal status, got %q", r.Status)
	}
	units := r.Units
	if units == nil {
		units = []model.UnitResult{}
	}
	unitsJSON, err := json.Marshal(units)
	if err != nil {
		return 0, err
	}
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		if seq, err = nextSeq(ctx, tx); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE runs SET seq = ?, status = ?, finished_at = ?, attempts = ?, bytes = ?, files = ?,
			error_kind = ?, error_summary = ?, units = ? WHERE id = ? AND seq IS NULL`,
			seq, string(r.Status), unixOrZero(r.FinishedAt), r.Attempts, r.Bytes, r.Files,
			string(r.ErrorKind), r.ErrorSummary, string(unitsJSON), r.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return finalMiss(ctx, tx, r.ID)
		}
		if err := SetMetaTx(ctx, tx, metaRunSeq, strconv.FormatInt(seq, 10)); err != nil {
			return err
		}
		if r.Kind.RunsHooks() && (r.Status == model.StatusSuccess || r.Status == model.StatusWarning) {
			// No row when the task has been deleted meanwhile; that is fine.
			_, err = tx.ExecContext(ctx, "UPDATE task_state SET last_success_at = ? WHERE task_id = ?",
				unixOrZero(r.FinishedAt), r.TaskID)
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// InterruptLeftovers finalizes every run still in progress as interrupted.
// It is called at start-up, when no run can be alive.
func (s *Store) InterruptLeftovers(ctx context.Context, now time.Time) (int, error) {
	n := 0
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM runs WHERE seq IS NULL ORDER BY started_at, rowid")
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(ids) == 0 {
			return err
		}
		seq, err := nextSeq(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE runs SET seq = ?, status = ?, finished_at = ?, error_summary = ?
				WHERE id = ? AND seq IS NULL`, seq, string(model.StatusInterrupted), now.Unix(),
				"tern stopped while this run was in progress", id); err != nil {
				return err
			}
			seq++
		}
		n = len(ids)
		return SetMetaTx(ctx, tx, metaRunSeq, strconv.FormatInt(seq-1, 10))
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) GetRun(ctx context.Context, id string) (model.Run, error) {
	return scanRun(s.db.QueryRowContext(ctx, "SELECT "+runColumns+" FROM runs WHERE id = ?", id))
}

// RunFilter narrows ListRuns. Zero fields do not filter; Limit 0 means no limit.
type RunFilter struct {
	TaskID string
	Before time.Time // only runs started strictly before this
	Limit  int
}

// ListRuns returns runs, most recently started first.
func (s *Store) ListRuns(ctx context.Context, f RunFilter) ([]model.Run, error) {
	q := "SELECT " + runColumns + " FROM runs WHERE 1"
	var args []any
	if f.TaskID != "" {
		q += " AND task_id = ?"
		args = append(args, f.TaskID)
	}
	if !f.Before.IsZero() {
		q += " AND started_at < ?"
		args = append(args, f.Before.Unix())
	}
	q += " ORDER BY started_at DESC, rowid DESC LIMIT ?"
	limit := f.Limit
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.QueryContext(ctx, q, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []model.Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// LastRun returns the run of taskID that finished most recently.
func (s *Store) LastRun(ctx context.Context, taskID string) (model.Run, bool, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, "SELECT "+runColumns+
		" FROM runs WHERE task_id = ? AND seq IS NOT NULL ORDER BY seq DESC LIMIT 1", taskID))
	if errors.Is(err, ErrNotFound) {
		return model.Run{}, false, nil
	}
	return r, err == nil, err
}

// PruneRuns deletes final runs that finished before olderThan, and for each
// task the final runs beyond its keepPerTask most recent ones. Runs in
// progress are never touched.
func (s *Store) PruneRuns(ctx context.Context, olderThan time.Time, keepPerTask int) (removedIDs []string, err error) {
	removedIDs = []string{}
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `DELETE FROM runs WHERE seq IS NOT NULL AND (finished_at < ? OR id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY task_id ORDER BY started_at DESC, seq DESC) AS rn
				FROM runs WHERE seq IS NOT NULL
			) WHERE rn > ?
		)) RETURNING id`, olderThan.Unix(), keepPerTask)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			removedIDs = append(removedIDs, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return removedIDs, nil
}
