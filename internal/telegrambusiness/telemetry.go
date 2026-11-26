package telegrambusiness

import (
	"context"
	"errors"
	"time"
)

// Observation contains aggregate diagnostics only. Never put account identifiers,
// message content, operation IDs, model names or provider errors in these fields.
type Observation struct {
	Operation    string
	Kind         string
	Outcome      string
	Duration     time.Duration
	Age          time.Duration
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
}

// Observer receives content-free diagnostics; metric adapters must bound labels.
type Observer interface {
	ObserveBusiness(Observation)
}

// ObserveTransport records content-free polling and queue diagnostics. The
// production observer bounds every label and discards unknown categories.
func (s *Service) ObserveTransport(observation Observation) {
	s.observe(observation)
}

func (s *Service) observe(observation Observation) {
	if s.options.Observer != nil {
		s.options.Observer.ObserveBusiness(observation)
	}
}

func (s *Service) observeResult(operation, kind string, started time.Time, err error) {
	outcome := "ok"
	switch {
	case errors.Is(err, ErrDenied):
		outcome = "denied"
	case errors.Is(err, ErrStale):
		outcome = "stale"
	case errors.Is(err, ErrQuota):
		outcome = "quota"
	case errors.Is(err, ErrUnknownOutcome):
		outcome = "unknown"
	case errors.Is(err, ErrLiveWritesDisabled):
		outcome = "disabled"
	case errors.Is(err, ErrUnsupported):
		outcome = "unsupported"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		outcome = "cancelled"
	case err != nil:
		outcome = "unavailable"
	}
	s.observe(Observation{Operation: operation, Kind: kind, Outcome: outcome, Duration: time.Since(started)})
}
