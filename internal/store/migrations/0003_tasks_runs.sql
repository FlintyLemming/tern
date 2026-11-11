CREATE TABLE tasks (
  id TEXT PRIMARY KEY, version INTEGER NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('backup','sync')),
  name TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL,
  spec TEXT NOT NULL,                      -- JSON of model.Task; never holds credentials
  repo_password TEXT NOT NULL DEFAULT '',  -- sealed; backup tasks only
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE TABLE task_state (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  last_fire_at INTEGER NOT NULL DEFAULT 0, last_success_at INTEGER NOT NULL DEFAULT 0,
  last_prune_at INTEGER NOT NULL DEFAULT 0, last_check_at INTEGER NOT NULL DEFAULT 0
) STRICT;
-- runs.task_id has no foreign key on purpose: run records outlive their task.
CREATE TABLE runs (
  id TEXT PRIMARY KEY, seq INTEGER UNIQUE,
  task_id TEXT NOT NULL, task_name TEXT NOT NULL,
  kind TEXT NOT NULL, triggered_by TEXT NOT NULL, status TEXT NOT NULL,
  started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0, bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0,
  error_kind TEXT NOT NULL DEFAULT '', error_summary TEXT NOT NULL DEFAULT '',
  units TEXT NOT NULL DEFAULT '[]'
) STRICT;
CREATE INDEX runs_by_task ON runs(task_id, started_at DESC);
