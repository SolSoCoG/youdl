package controller

import (
	"sync"
	"time"
)

// ipRateLimiter is a fixed-window rate limiter keyed by IP address.
type ipRateLimiter struct {
	mu     sync.Mutex
	slots  map[string]*rlSlot
	limit  int
	window time.Duration
}

type rlSlot struct {
	count int
	reset time.Time
}

func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	r := &ipRateLimiter{
		slots:  make(map[string]*rlSlot),
		limit:  limit,
		window: window,
	}
	go r.cleanup()
	return r
}

// Allow returns true if the IP is within the rate limit, false if it should be rejected.
// A limit of 0 always allows.
func (r *ipRateLimiter) Allow(ip string) bool {
	if r.limit <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	s, ok := r.slots[ip]
	if !ok || now.After(s.reset) {
		r.slots[ip] = &rlSlot{count: 1, reset: now.Add(r.window)}
		return true
	}
	if s.count >= r.limit {
		return false
	}
	s.count++
	return true
}

// cleanup removes expired slots periodically to prevent unbounded memory growth.
func (r *ipRateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		r.mu.Lock()
		now := time.Now()
		for ip, s := range r.slots {
			if now.After(s.reset) {
				delete(r.slots, ip)
			}
		}
		r.mu.Unlock()
	}
}
