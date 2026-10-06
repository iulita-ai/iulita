package llm

import (
	"context"
	"crypto/rand"
	"errors"
	"time"
)

// AttemptMetadata binds an adapter to immutable profile identity. No prompt,
// image, tool arguments, upstream error or credential is retained.
type AttemptMetadata struct {
	Provider, RequestedModel, ProfileID, Role string
	PolicyRevision                            uint64
}

// Attempt records bounded usage and routing provenance for one adapter invocation.
type Attempt struct {
	AttemptMetadata
	AttemptID, ChatID, UserID, Operation  string
	Model                                 string
	ModelVerified                         bool
	StartedAt, CompletedAt                time.Time
	Status                                string
	Usage                                 Usage
	UsageAvailable, ChargeUnknown, Cached bool
}

// AttemptObserver consumes an attempt without prompts, reasoning or credentials.
type AttemptObserver func(context.Context, Attempt)

// ObservingProvider wraps one adapter attempt, inside retry and credential
// admission. The observer sees failed attempts too, including known partial
// usage. It runs once even if another observing wrapper is composed outside it.
type ObservingProvider struct {
	inner    Provider
	metadata AttemptMetadata
	observe  AttemptObserver
}

// NewObservingProvider wraps one adapter with synchronous attempt observation.
func NewObservingProvider(inner Provider, metadata AttemptMetadata, observe AttemptObserver) *ObservingProvider {
	return &ObservingProvider{inner: inner, metadata: metadata, observe: observe}
}

// Complete records usage and outcome after a single nonstreaming adapter attempt.
func (p *ObservingProvider) Complete(ctx context.Context, req Request) (Response, error) {
	start := time.Now().UTC()
	resp, err := p.inner.Complete(ctx, req)
	p.record(ctx, req, &resp, err, start)
	return resp, err
}

// CompleteStream records streaming usage, including partial and failed attempts.
func (p *ObservingProvider) CompleteStream(ctx context.Context, req Request, cb StreamCallback) (Response, error) {
	start := time.Now().UTC()
	var resp Response
	var err error
	if sp, ok := p.inner.(StreamingProvider); ok {
		resp, err = sp.CompleteStream(ctx, req, cb)
	} else {
		resp, err = p.inner.Complete(ctx, req)
		if err == nil && cb != nil && resp.Content != "" {
			cb(resp.Content)
		}
	}
	p.record(ctx, req, &resp, err, start)
	return resp, err
}

func (p *ObservingProvider) record(ctx context.Context, req Request, resp *Response, err error, start time.Time) {
	if resp.UsageObserved || p.observe == nil {
		return
	}
	a := Attempt{AttemptMetadata: p.metadata, AttemptID: rand.Text(), ChatID: req.ChatID,
		UserID: req.UserID, Operation: req.Operation, Model: resp.Model,
		ModelVerified: resp.ModelVerified, StartedAt: start, CompletedAt: time.Now().UTC(),
		Usage: resp.Usage, Cached: resp.Cached, Status: "success"}
	if resp.Provider != "" {
		a.Provider = resp.Provider
	}
	if resp.RequestedModel != "" {
		a.RequestedModel = resp.RequestedModel
	}
	if req.ProfileID != "" {
		a.ProfileID = req.ProfileID
	}
	if req.Role != "" {
		a.Role = req.Role
	}
	if req.RoutingSnapshot != nil {
		a.PolicyRevision = req.RoutingSnapshot.Revision()
	}
	if req.PolicyRevision != 0 {
		a.PolicyRevision = req.PolicyRevision
	}
	if a.Model == "" {
		a.Model = a.RequestedModel
	}
	if a.Operation == "" {
		a.Operation = "background"
	}
	a.UsageAvailable = resp.UsageReported || resp.Usage != (Usage{}) || resp.Cached
	if resp.Cached {
		a.Status, a.Usage = "cache_hit", Usage{}
		resp.Usage = Usage{}
	} else if err != nil {
		a.Status = "error"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			a.Status = "cancelled" //nolint:misspell // Preserve the existing persisted event and metric status contract.
		}
		if errors.Is(err, ErrIncompleteResponse) {
			a.Status = "incomplete"
		}
	}
	// A failed or successful network call without provider usage has an unknown
	// bill. Empty usage is never silently treated as a free model invocation.
	a.ChargeUnknown = !a.UsageAvailable && !a.Cached
	var unsent interface{ NoCharge() bool }
	if errors.As(err, &unsent) && unsent.NoCharge() {
		a.Status, a.UsageAvailable, a.ChargeUnknown, a.Usage = "rejected", true, false, Usage{}
		resp.Usage = Usage{}
	}
	resp.UsageObserved = true
	p.observe(context.WithoutCancel(ctx), a)
}
