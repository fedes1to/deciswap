package auth

import (
	"sync"
	"time"
)

const (
	throttleIPFailures     = 5
	throttleIPBaseLock     = time.Minute
	throttleIPMaxLock      = 15 * time.Minute
	throttleGlobalFailures = 100
	throttleGlobalWindow   = 15 * time.Minute
	throttleGlobalLock     = time.Minute
	throttleMaxTracked     = 1024
)

type ipThrottle struct {
	failures    int
	lockedUntil time.Time
}

// throttle limits failed authentication attempts. Per IP: after 5 consecutive
// failures the address is locked for a minute, doubling on each further
// failure up to 15 minutes. Globally: 100 failures in 15 minutes lock all
// attempts for a minute. A success clears the per-IP counter.
type throttle struct {
	mu          sync.Mutex
	perIP       map[string]*ipThrottle
	globalFails []time.Time
	globalLock  time.Time
}

// check returns a ThrottledError if ip or the global bucket is locked at now.
func (t *throttle) check(ip string, now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var wait time.Duration
	if now.Before(t.globalLock) {
		wait = t.globalLock.Sub(now)
	}
	if st := t.perIP[ip]; st != nil && now.Before(st.lockedUntil) {
		if d := st.lockedUntil.Sub(now); d > wait {
			wait = d
		}
	}
	if wait > 0 {
		return ThrottledError{RetryAfter: wait}
	}
	return nil
}

// failure records one failed attempt from ip at now.
func (t *throttle) failure(ip string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.perIP == nil {
		t.perIP = make(map[string]*ipThrottle)
	}
	if len(t.perIP) >= throttleMaxTracked {
		for k, st := range t.perIP {
			if !now.Before(st.lockedUntil) {
				delete(t.perIP, k)
			}
		}
	}
	st := t.perIP[ip]
	if st == nil {
		st = &ipThrottle{}
		t.perIP[ip] = st
	}
	st.failures++
	if st.failures >= throttleIPFailures {
		lock := throttleIPBaseLock << (st.failures - throttleIPFailures)
		if lock > throttleIPMaxLock || lock <= 0 {
			lock = throttleIPMaxLock
		}
		st.lockedUntil = now.Add(lock)
	}

	kept := t.globalFails[:0]
	for _, at := range t.globalFails {
		if now.Sub(at) < throttleGlobalWindow {
			kept = append(kept, at)
		}
	}
	t.globalFails = append(kept, now)
	if len(t.globalFails) >= throttleGlobalFailures {
		t.globalLock = now.Add(throttleGlobalLock)
		t.globalFails = nil
	}
}

// success clears the per-IP counter for ip.
func (t *throttle) success(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.perIP, ip)
}
