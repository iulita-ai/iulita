package ratelimit

import (
	"sync"
	"time"
)

// Limiter enforces per-chat message rate limits using a sliding window.
type Limiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	rate    int
	window  time.Duration
}

// New creates a rate limiter allowing `rate` messages per `window` duration.
func New(rate int, window time.Duration) *Limiter {
	return &Limiter{
		windows: make(map[string][]time.Time),
		rate:    rate,
		window:  window,
	}
}

// Allow returns true if the chat has not exceeded its rate limit.
func (l *Limiter) Allow(chatID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	// Prune expired entries.
	timestamps := l.windows[chatID]
	start := 0
	for start < len(timestamps) && timestamps[start].Before(cutoff) {
		start++
	}
	timestamps = timestamps[start:]

	if len(timestamps) >= l.rate {
		l.windows[chatID] = timestamps
		return false
	}

	l.windows[chatID] = append(timestamps, now)
	return true
}

// ActionLimiter limits total LLM/tool actions per time window (global, not per-chat).
type ActionLimiter struct {
	mu      sync.Mutex
	count   int
	limit   int
	window  time.Duration
	resetAt time.Time
}

// NewActionLimiter creates a global action limiter allowing `limit` actions per `window`.
func NewActionLimiter(limit int, window time.Duration) *ActionLimiter {
	return &ActionLimiter{
		limit:   limit,
		window:  window,
		resetAt: time.Now().Add(window),
	}
}

// Allow returns false if the action limit has been exceeded for the current window.
func (l *ActionLimiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if now.After(l.resetAt) {
		l.count = 0
		l.resetAt = now.Add(l.window)
	}

	if l.count >= l.limit {
		return false
	}

	l.count++
	return true
}

// Count returns the current action count in the active window.
func (l *ActionLimiter) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if now.After(l.resetAt) {
		l.count = 0
		l.resetAt = now.Add(l.window)
	}

	return l.count
}
