package scheduler

import (
	"time"

	"tern/internal/model"
)

// StaleAfter is how long a task may go without a successful run before it
// counts as stale: its own setting, or twice its schedule period. Zero
// means the task cannot be stale (no schedule and no setting).
func StaleAfter(t model.Task, now time.Time) time.Duration {
	if t.StaleAfterSec > 0 {
		return time.Duration(t.StaleAfterSec) * time.Second
	}
	if t.Schedule == "" {
		return 0
	}
	period, err := Period(t.Schedule, now)
	if err != nil {
		return 0
	}
	return 2 * period
}

// IsStale reports whether t has gone too long without a success. A task
// that never succeeded is measured from its creation.
func IsStale(t model.Task, lastSuccess, now time.Time) bool {
	limit := StaleAfter(t, now)
	if limit == 0 {
		return false
	}
	since := lastSuccess
	if since.IsZero() {
		since = t.CreatedAt
	}
	return now.Sub(since) > limit
}
