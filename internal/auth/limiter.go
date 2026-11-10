package auth

import (
	"sync"
	"time"
)

const (
	limiterFreeFailures = 5
	limiterFirstLock    = time.Minute
	limiterMaxLock      = 15 * time.Minute
)

// Limiter throttles login attempts per source IP: five consecutive failures
// lock the IP for a minute; every failure after a lock expires doubles the
// lock, capped at fifteen minutes. A successful login resets it.
type Limiter struct {
	mu sync.Mutex
	m  map[string]*limiterEntry
}

type limiterEntry struct {
	fails       int
	lock        time.Duration
	lockedUntil time.Time
}

func NewLimiter() *Limiter {
	return &Limiter{m: make(map[string]*limiterEntry)}
}

// Allow reports whether ip may attempt a login now, and if not, how long to wait.
func (l *Limiter) Allow(ip string, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[ip]; e != nil && now.Before(e.lockedUntil) {
		return false, e.lockedUntil.Sub(now)
	}
	return true, 0
}

// Fail records a failed attempt.
func (l *Limiter) Fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[ip]
	if e == nil {
		e = &limiterEntry{}
		l.m[ip] = e
	}
	if now.Before(e.lockedUntil) {
		return // attempts that raced past Allow do not escalate the lock
	}
	e.fails++
	if e.fails < limiterFreeFailures {
		return
	}
	if e.lock == 0 {
		e.lock = limiterFirstLock
	} else {
		e.lock = min(e.lock*2, limiterMaxLock)
	}
	e.lockedUntil = now.Add(e.lock)
}

// Reset forgets ip's failures.
func (l *Limiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, ip)
}
