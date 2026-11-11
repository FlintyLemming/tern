package scheduler

import (
	"reflect"
	"testing"
	"time"

	"tern/internal/model"
)

// Cron expressions are read in time.Local; pin it so the tests mean the
// same thing on every machine.
func init() { time.Local = time.UTC }

func at(day, hour, min int) time.Time { return time.Date(2026, 11, day, hour, min, 0, 0, time.UTC) }

func TestNextRuns(t *testing.T) {
	got, err := NextRuns("0 3 * * *", at(10, 2, 0), 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []time.Time{at(10, 3, 0), at(11, 3, 0), at(12, 3, 0)}; !reflect.DeepEqual(got, want) {
		t.Errorf("NextRuns = %v", got)
	}
	// Strictly after from.
	if got, _ := NextRuns("0 3 * * *", at(10, 3, 0), 1); !got[0].Equal(at(11, 3, 0)) {
		t.Errorf("from 03:00 itself: %v", got)
	}
	for _, bad := range []string{"", "nope", "@daily"} {
		if _, err := NextRuns(bad, at(10, 2, 0), 1); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestNextRunsUsesLocalTime(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()
	time.Local = time.FixedZone("UTC+8", 8*3600)
	got, err := NextRuns("0 3 * * *", at(10, 2, 0), 1) // 10:00 local
	if err != nil {
		t.Fatal(err)
	}
	if want := at(10, 19, 0); !got[0].Equal(want) || got[0].Location() != time.UTC { // 03:00 local the next day
		t.Errorf("NextRuns = %v, want %v in UTC", got[0], want)
	}
}

func TestNeverFiringCron(t *testing.T) {
	if _, err := NextRuns("0 0 31 2 *", at(10, 2, 0), 1); err == nil {
		t.Error("want an error for a schedule that never fires")
	}
	if _, err := Period("0 0 31 2 *", at(10, 2, 0)); err == nil {
		t.Error("Period: want an error for a schedule that never fires")
	}
}

func TestPeriod(t *testing.T) {
	for expr, want := range map[string]time.Duration{
		"0 3 * * *":    24 * time.Hour,
		"0 3 * * 1-5":  72 * time.Hour,
		"*/15 * * * *": 15 * time.Minute,
	} {
		if got, err := Period(expr, at(10, 2, 0)); err != nil || got != want {
			t.Errorf("Period(%q) = %v, %v; want %v", expr, got, err, want)
		}
	}
}

func daily() model.Task {
	return model.Task{Schedule: "0 3 * * *", CreatedAt: at(1, 0, 0)}
}

func TestStaleAfter(t *testing.T) {
	now := at(10, 2, 0)
	if got := StaleAfter(daily(), now); got != 48*time.Hour {
		t.Errorf("daily: %v", got)
	}
	custom := daily()
	custom.StaleAfterSec = 3600
	if got := StaleAfter(custom, now); got != time.Hour {
		t.Errorf("explicit: %v", got)
	}
	if got := StaleAfter(model.Task{}, now); got != 0 {
		t.Errorf("no schedule: %v", got)
	}
}

func TestIsStale(t *testing.T) {
	now := at(10, 12, 0)
	if !IsStale(daily(), now.Add(-49*time.Hour), now) {
		t.Error("49h since the last success should be stale")
	}
	if IsStale(daily(), now.Add(-47*time.Hour), now) {
		t.Error("47h since the last success should not be stale")
	}
	never := daily()
	never.CreatedAt = now.Add(-49 * time.Hour)
	if !IsStale(never, time.Time{}, now) {
		t.Error("never succeeded and created 49h ago should be stale")
	}
	never.CreatedAt = now.Add(-time.Hour)
	if IsStale(never, time.Time{}, now) {
		t.Error("a task created an hour ago is not stale yet")
	}
	if IsStale(model.Task{CreatedAt: at(1, 0, 0)}, time.Time{}, now) {
		t.Error("a task without a schedule is never stale")
	}
}
