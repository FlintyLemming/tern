package model

import "time"

type TaskType string

const (
	TaskBackup TaskType = "backup"
	TaskSync   TaskType = "sync"
)

// Task is one configured backup or sync job. It holds no credentials: the
// restic repository password stays sealed in the store.
type Task struct {
	ID            string      `json:"id"`
	Version       int         `json:"version"`
	Type          TaskType    `json:"type"`
	Name          string      `json:"name"`
	Enabled       bool        `json:"enabled"`
	Schedule      string      `json:"schedule"`        // 5-field cron; empty = manual runs only
	CatchUp       bool        `json:"catch_up"`        // run once after start-up if a scheduled time was missed
	StaleAfterSec int         `json:"stale_after_sec"` // 0 = automatic (2 × schedule period)
	MaxRuntimeSec int         `json:"max_runtime_sec"` // 0 = unlimited
	Retry         RetryPolicy `json:"retry"`
	Pre           Hook        `json:"pre"`
	Post          Hook        `json:"post"`
	Backup        *BackupSpec `json:"backup,omitempty"`
	Sync          *SyncSpec   `json:"sync,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// RetryPolicy: before the n-th retry tern waits DelaysSec[min(n, len)-1] seconds.
type RetryPolicy struct {
	Retries   int   `json:"retries"`
	DelaysSec []int `json:"delays_sec"`
}

// Delay is the wait before the n-th retry (n starts at 1).
func (p RetryPolicy) Delay(n int) time.Duration {
	if len(p.DelaysSec) == 0 || n < 1 {
		return 0
	}
	return time.Duration(p.DelaysSec[min(n, len(p.DelaysSec))-1]) * time.Second
}

const (
	HookLocal = "local"
	HookSSH   = "ssh"
)

// Hook is a pre- or post-task command.
type Hook struct {
	Script        string `json:"script"`
	Where         string `json:"where"`         // HookLocal | HookSSH
	ConnectionID  string `json:"connection_id"` // required when Where is HookSSH
	TimeoutSec    int    `json:"timeout_sec"`
	IgnoreFailure bool   `json:"ignore_failure"` // pre-task command only
}

type BackupSpec struct {
	ConnectionID   string     `json:"connection_id"`
	RepoPath       string     `json:"repo_path"`
	Host           string     `json:"host"` // restic --host; fixed at creation
	Sources        []string   `json:"sources"`
	Excludes       []string   `json:"excludes"`
	LimitUploadKiB int        `json:"limit_upload_kib"` // 0 = unlimited
	Connections    int        `json:"connections"`      // restic sftp.connections
	Keep           KeepPolicy `json:"keep"`
	PruneEveryDays int        `json:"prune_every_days"` // 0 = never automatically
	CheckEveryDays int        `json:"check_every_days"` // 0 = never automatically
}

type KeepPolicy struct {
	Last    int `json:"last"`
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

type SyncSpec struct {
	Protocol     string       `json:"protocol"` // "sftp" | "rsync" | "webdav"
	ConnectionID string       `json:"connection_id"`
	Direction    string       `json:"direction"` // "push" | "pull"
	Pairs        []FolderPair `json:"pairs"`
	DeleteMode   string       `json:"delete_mode"` // "keep" | "mirror"
	MaxDeletes   int          `json:"max_deletes"`
	Excludes     []string     `json:"excludes"`
	Compare      string       `json:"compare"`   // "size_mtime" | "checksum"
	LimitKiB     int          `json:"limit_kib"` // 0 = unlimited
	Transfers    int          `json:"transfers"` // files in parallel; must be 0 for rsync
}

type FolderPair struct {
	Local  string `json:"local"`
	Remote string `json:"remote"`
}

// ApplyDefaults fills in what the user may leave out.
func (t *Task) ApplyDefaults() {
	if len(t.Retry.DelaysSec) == 0 {
		t.Retry = RetryPolicy{Retries: 3, DelaysSec: []int{60, 300, 900}}
	}
	for _, h := range []*Hook{&t.Pre, &t.Post} {
		if h.Where == "" {
			h.Where = HookLocal
		}
		if h.TimeoutSec == 0 {
			h.TimeoutSec = 600
		}
	}
	if b := t.Backup; b != nil {
		if b.Connections == 0 {
			b.Connections = 5
		}
	}
	if s := t.Sync; s != nil {
		if s.DeleteMode == "" {
			s.DeleteMode = "keep"
		}
		if s.MaxDeletes == 0 {
			s.MaxDeletes = 100
		}
		if s.Compare == "" {
			s.Compare = "size_mtime"
		}
		if s.Transfers == 0 && s.Protocol != "rsync" {
			s.Transfers = 4
		}
	}
}

// RunKind is the kind of run a schedule or a plain "run now" starts.
func (t Task) RunKind() RunKind {
	if t.Type == TaskBackup {
		return RunBackup
	}
	return RunSync
}

// ConnectionIDs lists the connections the task refers to: the main one
// first, then those of its hooks, without duplicates.
func (t Task) ConnectionIDs() []string {
	var ids []string
	add := func(id string) {
		if id == "" {
			return
		}
		for _, have := range ids {
			if have == id {
				return
			}
		}
		ids = append(ids, id)
	}
	if t.Backup != nil {
		add(t.Backup.ConnectionID)
	}
	if t.Sync != nil {
		add(t.Sync.ConnectionID)
	}
	for _, h := range []Hook{t.Pre, t.Post} {
		if h.Where == HookSSH {
			add(h.ConnectionID)
		}
	}
	return ids
}
