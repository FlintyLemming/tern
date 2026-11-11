// Package clock is the one source of time for everything that schedules,
// waits or measures, so tests can drive it by hand.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock is what tern needs from time.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// Real is the system clock.
var Real Clock = realClock{}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Fake is a clock that only moves when told to.
type Fake struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	waiters []waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func NewFake(start time.Time) *Fake {
	f := &Fake{now: start}
	f.cond = sync.NewCond(&f.mu)
	return f
}

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After returns a channel that receives once the clock reaches now+d.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.waiters = append(f.waiters, waiter{at: f.now.Add(d), ch: ch})
	f.cond.Broadcast()
	return ch
}

// Advance moves the clock forward; After channels that come due fire in
// order of their deadlines.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set(f.now.Add(d))
}

// Set jumps to t, which may lie in the past. Going back fires nothing.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set(t)
}

func (f *Fake) set(t time.Time) {
	f.now = t
	sort.SliceStable(f.waiters, func(i, j int) bool { return f.waiters[i].at.Before(f.waiters[j].at) })
	n := 0
	for _, w := range f.waiters {
		if w.at.After(t) {
			break
		}
		w.ch <- w.at
		n++
	}
	f.waiters = append(f.waiters[:0], f.waiters[n:]...)
}

// BlockUntil returns once at least n After channels are pending. Tests use
// it to know that the code under test has reached its wait.
func (f *Fake) BlockUntil(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.waiters) < n {
		f.cond.Wait()
	}
}
