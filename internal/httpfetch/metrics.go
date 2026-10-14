package httpfetch

import "time"

// MetricsHooks receives bounded engine observations. Implementations must
// treat every string as a label VALUE from a fixed vocabulary: method
// buckets, status classes, outcome enums — never URLs, queries, usernames,
// headers, or operation IDs (plan §11).
type MetricsHooks interface {
	StepDone(methodBucket, statusClass, outcome string, seconds float64, entityBytes int64, download bool)
	TransferAcquired()
	TransferReleased()
	PendingHeld()
	PendingReleased()
	ConsentAsked(reason, outcome string)
	ArtifactBytes(op string, bytes int64)
}

// noopMetrics is the default when the host wires none.
type noopMetrics struct{}

// StepDone observes one finished step (no-op default).
func (noopMetrics) StepDone(string, string, string, float64, int64, bool) {}

// TransferAcquired observes a permit acquisition (no-op default).
func (noopMetrics) TransferAcquired() {}

// TransferReleased observes a permit release (no-op default).
func (noopMetrics) TransferReleased() {}

// PendingHeld observes a paused-stream hold (no-op default).
func (noopMetrics) PendingHeld() {}

// PendingReleased observes a paused-stream release (no-op default).
func (noopMetrics) PendingReleased() {}

// ConsentAsked observes a confirmation ask (no-op default).
func (noopMetrics) ConsentAsked(string, string) {}

// ArtifactBytes observes stored/deleted bytes (no-op default).
func (noopMetrics) ArtifactBytes(string, int64) {}

// methodBucket collapses custom methods into a bounded set.
func methodBucket(method string) string {
	switch normalizeMethod(method) {
	case "GET", "HEAD":
		return "get"
	case "POST":
		return "post"
	case "PUT":
		return "put"
	case "PATCH":
		return "patch"
	case "DELETE":
		return "delete"
	case "OPTIONS":
		return "options"
	}
	return "custom"
}

// statusClass returns "1xx".."5xx" or "none".
func statusClass(status int) string {
	if status == 0 {
		return "none"
	}
	switch {
	case status < 200:
		return "1xx"
	case status < 300:
		return "2xx"
	case status < 400:
		return "3xx"
	case status < 500:
		return "4xx"
	default:
		return "5xx"
	}
}

// observeStep reports one finished step.
func (e *Engine) observeStep(sr StepResult, method string, dur time.Duration, download bool) {
	if e.metrics == nil {
		return
	}
	e.metrics.StepDone(methodBucket(method), statusClass(sr.Status), string(sr.Outcome), dur.Seconds(), sr.BytesRead, download)
}
