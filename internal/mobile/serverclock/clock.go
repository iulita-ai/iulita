// Package serverclock identifies a runtime clock and fences detected wall-clock
// discontinuities. A sample describes this runtime, not universal UTC accuracy.
package serverclock

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Clock is shared by the OAuth listener, gRPC and the device coordinator.
type Clock struct {
	mu         sync.Mutex
	id         string
	anchor     time.Time
	now        func() time.Time
	monotonic  func() time.Duration
	monoAnchor time.Duration
}

// New creates a new process-local clock epoch, never restored from backup.
func New() *Clock {
	start := time.Now()
	return newClock(time.Now, func() time.Duration { return time.Since(start) })
}

func newClock(now func() time.Time, monotonic func() time.Duration) *Clock {
	return &Clock{id: uuid.NewString(), anchor: now(), now: now, monotonic: monotonic, monoAnchor: monotonic()}
}

// Read returns the current epoch and wall time. An observed >50ms discontinuity
// rotates the epoch, invalidating native sessions bound to the previous sample.
func (c *Clock) Read() (string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now, mono := c.now(), c.monotonic()
	wall := now.UnixNano() - c.anchor.UnixNano()
	delta := time.Duration(wall) - (mono - c.monoAnchor)
	if delta > 50*time.Millisecond || delta < -50*time.Millisecond {
		c.id, c.anchor, c.monoAnchor = uuid.NewString(), now, mono
	}
	return c.id, now
}

// ID samples the clock rather than caching a potentially stale epoch.
func (c *Clock) ID() string { id, _ := c.Read(); return id }
