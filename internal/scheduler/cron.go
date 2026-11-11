package scheduler

import (
	"errors"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"tern/internal/model"
)

// parse accepts exactly what model.CheckSchedule accepts for a non-empty
// schedule: five fields, no descriptors, no time zone prefix.
func parse(expr string) (cron.Schedule, error) {
	if expr == "" {
		return nil, errors.New("no schedule")
	}
	if code := model.CheckSchedule(expr); code != "" && code != "never_fires" {
		return nil, fmt.Errorf("schedule %q is %s", expr, code)
	}
	return cron.ParseStandard(expr)
}

// next is the first time after t at which sched fires, in UTC; the zero
// time when there is none within five years. Expressions are read in the
// container's time zone.
func next(sched cron.Schedule, t time.Time) time.Time {
	n := sched.Next(t.In(time.Local))
	if n.IsZero() {
		return n
	}
	return n.UTC()
}

// NextRuns returns the next n times expr fires after from. An expression
// that does not fire n times within five years is an error.
func NextRuns(expr string, from time.Time, n int) ([]time.Time, error) {
	sched, err := parse(expr)
	if err != nil {
		return nil, err
	}
	out := make([]time.Time, 0, n)
	for t := from; len(out) < n; {
		if t = next(sched, t); t.IsZero() {
			return nil, fmt.Errorf("schedule %q does not fire %d times in the next five years", expr, n)
		}
		out = append(out, t)
	}
	return out, nil
}

// Period is the longest gap between consecutive firings among the next ten.
func Period(expr string, from time.Time) (time.Duration, error) {
	runs, err := NextRuns(expr, from, 10)
	if err != nil {
		return 0, err
	}
	var longest time.Duration
	for i := 1; i < len(runs); i++ {
		longest = max(longest, runs[i].Sub(runs[i-1]))
	}
	return longest, nil
}
