package scheduler

import (
	"context"
	"strconv"
	"time"

	"tern/internal/runlog"
)

const (
	MetaLogKeepDays = "settings.log_keep_days"
	MetaLogKeepRuns = "settings.log_keep_runs"

	DefaultMaxConcurrent = 1
	DefaultLogKeepDays   = 90
	DefaultLogKeepRuns   = 200

	housekeepEvery = 24 * time.Hour
)

func (s *Scheduler) metaInt(ctx context.Context, key string, def int) int {
	raw, ok, err := s.d.Store.GetMeta(ctx, key)
	if n, perr := strconv.Atoi(raw); err == nil && ok && perr == nil && n >= 1 {
		return n
	}
	return def
}

// Housekeep deletes run records, and their log files, that are older than
// the configured number of days or beyond the configured number of runs
// per task. Runs in progress are never touched. The main loop calls it at
// start-up and then once a day.
func (s *Scheduler) Housekeep(ctx context.Context) error {
	days := s.metaInt(ctx, MetaLogKeepDays, DefaultLogKeepDays)
	keep := s.metaInt(ctx, MetaLogKeepRuns, DefaultLogKeepRuns)
	cutoff := s.d.Clock.Now().Add(-time.Duration(days) * 24 * time.Hour)
	removed, err := s.d.Store.PruneRuns(ctx, cutoff, keep)
	if err != nil {
		return err
	}
	if len(removed) > 0 {
		s.d.Log.Info("old run records removed", "count", len(removed))
	}
	// Records first: a log without a record is only wasted space, a record
	// without its log would be a broken link in the UI.
	return runlog.Remove(s.d.Runner.DataDir, removed)
}

// housekeepIfDue is called from every pass, with passMu held.
func (s *Scheduler) housekeepIfDue(ctx context.Context, now time.Time) {
	if !s.lastHousekeep.IsZero() && !now.Before(s.lastHousekeep) && now.Sub(s.lastHousekeep) < housekeepEvery {
		return
	}
	s.lastHousekeep = now
	if err := s.Housekeep(ctx); err != nil {
		s.d.Log.Error("housekeeping failed", "err", err)
	}
}
