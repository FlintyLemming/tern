package model

import (
	"encoding/json"
	"time"
)

type RunKind string

const (
	RunBackup  RunKind = "backup"
	RunSync    RunKind = "sync"
	RunDryRun  RunKind = "dry_run"
	RunRestore RunKind = "restore"
	RunPrune   RunKind = "prune"
	RunCheck   RunKind = "check"
)

// RunsHooks reports whether runs of this kind execute the pre- and
// post-task commands.
func (k RunKind) RunsHooks() bool { return k == RunBackup || k == RunSync }

type Trigger string

const (
	TriggerSchedule Trigger = "schedule"
	TriggerManual   Trigger = "manual"
	TriggerCatchUp  Trigger = "catch_up"
)

type Status string

const (
	// In progress.
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusRetryWait Status = "retry_wait"
	// Terminal: exactly these seven.
	StatusSuccess     Status = "success"
	StatusWarning     Status = "warning" // finished, with warnings
	StatusPartial     Status = "partial" // some units failed
	StatusFailed      Status = "failed"
	StatusCanceled    Status = "canceled"
	StatusSkipped     Status = "skipped"
	StatusInterrupted Status = "interrupted"
)

func (s Status) Terminal() bool {
	switch s {
	case StatusSuccess, StatusWarning, StatusPartial, StatusFailed, StatusCanceled, StatusSkipped, StatusInterrupted:
		return true
	}
	return false
}

// Run is one execution of a task. Once it reaches a terminal status it gets
// a sequence number and never changes again.
type Run struct {
	ID           string       `json:"id"`
	Seq          int64        `json:"seq"` // assigned on reaching a terminal status; 0 before
	TaskID       string       `json:"task_id"`
	TaskName     string       `json:"task_name"`
	Kind         RunKind      `json:"kind"`
	Trigger      Trigger      `json:"trigger"`
	Status       Status       `json:"status"`
	StartedAt    time.Time    `json:"started_at"`
	FinishedAt   time.Time    `json:"finished_at"`
	Attempts     int          `json:"attempts"`
	Bytes        int64        `json:"bytes"`
	Files        int64        `json:"files"`
	ErrorKind    ErrKind      `json:"error_kind"`
	ErrorSummary string       `json:"error_summary"`
	Units        []UnitResult `json:"units"`
}

// MarshalJSON renders an unfinished run's finished_at as null and a run
// without units as [].
func (r Run) MarshalJSON() ([]byte, error) {
	type plain Run
	out := struct {
		plain
		FinishedAt *time.Time   `json:"finished_at"`
		Units      []UnitResult `json:"units"`
	}{plain: plain(r), Units: r.Units}
	if !r.FinishedAt.IsZero() {
		out.FinishedAt = &r.FinishedAt
	}
	if out.Units == nil {
		out.Units = []UnitResult{}
	}
	return json.Marshal(out)
}

// UnitResult is the outcome of one unit of work: for a sync, one folder
// pair; for everything else, the single unit.
type UnitResult struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Bytes  int64  `json:"bytes"`
	Files  int64  `json:"files"`
	Error  string `json:"error"`
}

// Progress is the common shape of what the tools report while running.
type Progress struct {
	Unit           string  `json:"unit"`
	BytesDone      int64   `json:"bytes_done"`
	BytesTotal     int64   `json:"bytes_total"`
	TotalEstimated bool    `json:"total_estimated,omitempty"`
	FilesDone      int64   `json:"files_done"`
	FilesTotal     int64   `json:"files_total"`
	SpeedBps       float64 `json:"speed_bps"`
	ETASec         int64   `json:"eta_sec"`
}
