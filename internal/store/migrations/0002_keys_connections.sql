CREATE TABLE ssh_keys (
  id TEXT PRIMARY KEY, version INTEGER NOT NULL, name TEXT NOT NULL UNIQUE,
  public_key TEXT NOT NULL, fingerprint TEXT NOT NULL,
  private_key TEXT NOT NULL, passphrase TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE TABLE connections (
  id TEXT PRIMARY KEY, version INTEGER NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('ssh','webdav')), name TEXT NOT NULL UNIQUE,
  host TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 0, username TEXT NOT NULL DEFAULT '',
  ssh_key_id TEXT REFERENCES ssh_keys(id) ON DELETE RESTRICT,
  host_key TEXT NOT NULL DEFAULT '', url TEXT NOT NULL DEFAULT '', password TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
