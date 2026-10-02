package auth

import (
	"sync"
	"time"
)

// Admin credential brute-force policy: loginMaxFailures wrong secrets per
// client IP inside a fixed LoginFailureWindow lock that IP out of every admin
// credential check (login form and X-Admin-* headers alike, since both are the
// same oracle) until the window ends. Ten is generous for typos and tight for
// guessing; the cap bounds memory however many source IPs an attacker has.
const (
	loginMaxFailures   = 10
	LoginFailureWindow = 15 * time.Minute
	loginLimiterCap    = 4096
)

type failureWindow struct {
	count int
	start time.Time
}

// failureLimiter counts credential failures per key in fixed windows.
type failureLimiter struct {
	mu       sync.Mutex
	byKey    map[string]*failureWindow
	max      int
	window   time.Duration
	capacity int
	now      func() time.Time
}

func newFailureLimiter(max int, window time.Duration, capacity int, now func() time.Time) *failureLimiter {
	return &failureLimiter{
		byKey:    make(map[string]*failureWindow, capacity),
		max:      max,
		window:   window,
		capacity: capacity,
		now:      now,
	}
}

// failures is the process-wide admin credential failure limiter.
var failures = newFailureLimiter(loginMaxFailures, LoginFailureWindow, loginLimiterCap, time.Now)

// blocked reports whether key is locked out and, if so, for how long.
func (l *failureLimiter) blocked(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.byKey[key]
	if !ok {
		return 0, false
	}
	left := l.window - l.now().Sub(w.start)
	if left <= 0 || w.count < l.max {
		return 0, false
	}
	return left, true
}

// fail records one failure for key, opening a new window when none is active.
func (l *failureLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if w, ok := l.byKey[key]; ok && now.Sub(w.start) < l.window {
		w.count++
		return
	}
	delete(l.byKey, key)
	if len(l.byKey) >= l.capacity {
		l.pruneLocked(now)
	}
	if len(l.byKey) >= l.capacity {
		l.evictOldestLocked()
	}
	l.byKey[key] = &failureWindow{count: 1, start: now}
}

// reset forgets key's failures after a successful authentication.
func (l *failureLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byKey, key)
}

// pruneLocked drops every elapsed window. Bounded by capacity.
func (l *failureLimiter) pruneLocked(now time.Time) {
	for k, w := range l.byKey {
		if now.Sub(w.start) >= l.window {
			delete(l.byKey, k)
		}
	}
}

// evictOldestLocked drops the window that started first. Bounded by capacity.
func (l *failureLimiter) evictOldestLocked() {
	oldestKey := ""
	var oldest *failureWindow
	for k, w := range l.byKey {
		if oldest == nil || w.start.Before(oldest.start) {
			oldestKey, oldest = k, w
		}
	}
	if oldest != nil {
		delete(l.byKey, oldestKey)
	}
}
