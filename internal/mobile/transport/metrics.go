package transport

import (
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
)

// Counters have fixed cardinality: no labels for owner/device/chat IDs, titles,
// tokens, URLs, selectors, content or arbitrary client-supplied error strings.
type Counters struct {
	completed      [17]atomic.Uint64
	active         atomic.Int64
	successLatency [7]atomic.Uint64
}

// Diagnostics contains bounded subsystem counters without private payloads.
type Diagnostics struct {
	Codes                 [17]uint64
	Active                int64
	SuccessLatencyBuckets [7]uint64
}

func (c *Counters) record(code codes.Code, elapsed time.Duration) {
	i := int(code)
	if i < 0 || i >= len(c.completed) {
		i = int(codes.Unknown)
	}
	c.completed[i].Add(1)
	if code == codes.OK {
		bounds := []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, time.Second, 5 * time.Second, 30 * time.Second, 120 * time.Second}
		bucket := len(bounds)
		for i, b := range bounds {
			if elapsed <= b {
				bucket = i
				break
			}
		}
		c.successLatency[bucket].Add(1)
	}
}

// Diagnostics returns bounded subsystem counters.
func (s *Server) Diagnostics() Diagnostics {
	var d Diagnostics
	for i := range d.Codes {
		d.Codes[i] = s.metrics.completed[i].Load()
	}
	for i := range d.SuccessLatencyBuckets {
		d.SuccessLatencyBuckets[i] = s.metrics.successLatency[i].Load()
	}
	d.Active = s.metrics.active.Load()
	return d
}
