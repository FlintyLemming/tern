package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"tern/internal/model"
)

// unixOrZero and fromUnix keep "never" as 0 in the database and as the zero
// time in Go.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

const taskColumns = "id, version, type, name, enabled, spec, created_at, updated_at"

// scanTask decodes the spec and lets the real columns win over whatever the
// JSON says about them.
func scanTask(row interface{ Scan(...any) error }) (model.Task, error) {
	var (
		t                model.Task
		id, typ, name    string
		version          int
		enabled          bool
		spec             string
		created, updated int64
	)
	err := row.Scan(&id, &version, &typ, &name, &enabled, &spec, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, ErrNotFound
	}
	if err != nil {
		return model.Task{}, err
	}
	if err := json.Unmarshal([]byte(spec), &t); err != nil {
		return model.Task{}, fmt.Errorf("store: task %s has an unreadable spec: %w", id, err)
	}
	t.ID, t.Version, t.Type, t.Name, t.Enabled = id, version, model.TaskType(typ), name, enabled
	t.CreatedAt, t.UpdatedAt = fromUnix(created), fromUnix(updated)
	return t, nil
}

// CreateTask stores t; repoPasswordSealed is "" for anything but a backup
// task. A taken name is ErrDuplicate.
func (s *Store) CreateTask(ctx context.Context, t model.Task, repoPasswordSealed string) error {
	spec, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO tasks (id, version, type, name, enabled, spec, repo_password, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Version, string(t.Type), t.Name, t.Enabled, string(spec), repoPasswordSealed, t.CreatedAt.Unix(), t.UpdatedAt.Unix())
		if isConstraint(err, sqliteConstraintUnique) {
			return ErrDuplicate
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO task_state (task_id) VALUES (?)", t.ID)
		return err
	})
}

// UpdateTask rewrites t.ID if it is still at t.Version, bumping the version.
// The type, the creation time and the repository password never change here.
func (s *Store) UpdateTask(ctx context.Context, t model.Task) error {
	spec, err := json.Marshal(t)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET version = version + 1, name = ?, enabled = ?, spec = ?, updated_at = ?
		WHERE id = ? AND version = ?`, t.Name, t.Enabled, string(spec), t.UpdatedAt.Unix(), t.ID, t.Version)
	if isConstraint(err, sqliteConstraintUnique) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return s.versionMiss(ctx, "tasks", t.ID)
	}
	return nil
}

func (s *Store) GetTask(ctx context.Context, id string) (model.Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM tasks WHERE id = ?", id))
}

// ListTasks returns every task, oldest first.
func (s *Store) ListTasks(ctx context.Context) ([]model.Task, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+taskColumns+" FROM tasks ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []model.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// DeleteTask removes a task and its state. Its run records stay.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TaskRepoPassword returns the sealed restic repository password, "" when
// none is set.
func (s *Store) TaskRepoPassword(ctx context.Context, id string) (sealed string, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT repo_password FROM tasks WHERE id = ?", id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return sealed, err
}

// TasksUsingConnection names the tasks that refer to connID, as their main
// connection or from a hook.
func (s *Store) TasksUsingConnection(ctx context.Context, connID string) (taskNames []string, err error) {
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	taskNames = []string{}
	for _, t := range tasks {
		for _, id := range t.ConnectionIDs() {
			if id == connID {
				taskNames = append(taskNames, t.Name)
				break
			}
		}
	}
	return taskNames, nil
}

// TaskState is what the scheduler remembers about a task between runs.
type TaskState struct {
	LastFireAt    time.Time // the latest scheduled time that has been dealt with
	LastSuccessAt time.Time
	LastPruneAt   time.Time
	LastCheckAt   time.Time
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getTaskState(ctx context.Context, q queryer, id string) (TaskState, error) {
	var fire, success, prune, check int64
	err := q.QueryRowContext(ctx, `SELECT last_fire_at, last_success_at, last_prune_at, last_check_at
		FROM task_state WHERE task_id = ?`, id).Scan(&fire, &success, &prune, &check)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskState{}, ErrNotFound
	}
	if err != nil {
		return TaskState{}, err
	}
	return TaskState{fromUnix(fire), fromUnix(success), fromUnix(prune), fromUnix(check)}, nil
}

func (s *Store) GetTaskState(ctx context.Context, id string) (TaskState, error) {
	return getTaskState(ctx, s.db, id)
}

// SetTaskState reads the state of task id, lets fn change it and writes it
// back, all in one transaction.
func (s *Store) SetTaskState(ctx context.Context, id string, fn func(*TaskState)) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		st, err := getTaskState(ctx, tx, id)
		if err != nil {
			return err
		}
		fn(&st)
		_, err = tx.ExecContext(ctx, `UPDATE task_state SET last_fire_at = ?, last_success_at = ?, last_prune_at = ?, last_check_at = ?
			WHERE task_id = ?`, unixOrZero(st.LastFireAt), unixOrZero(st.LastSuccessAt), unixOrZero(st.LastPruneAt), unixOrZero(st.LastCheckAt), id)
		return err
	})
}
