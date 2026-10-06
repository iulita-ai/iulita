package llm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type observedFixture struct {
	response Response
	err      error
}

func (p observedFixture) Complete(context.Context, Request) (Response, error) {
	return p.response, p.err
}
func (p observedFixture) CompleteStream(_ context.Context, _ Request, cb StreamCallback) (Response, error) {
	if cb != nil {
		cb("visible")
	}
	return p.response, p.err
}

func TestObserverKnownFailedUsageCacheAndUnknown(t *testing.T) {
	for _, test := range []struct {
		name               string
		resp               Response
		err                error
		status             string
		available, unknown bool
	}{
		{"failed known", Response{Usage: Usage{InputTokens: 10, OutputTokens: 3}}, ErrIncompleteResponse, "incomplete", true, false},
		{"failed unknown", Response{}, errors.New("private upstream error"), "error", false, true},
		{"local rejected", Response{}, &UnsentRequestError{Cause: errors.New("image count exceeds limit")}, "rejected", true, false},
		{"success missing usage", Response{Content: "ok"}, nil, "success", false, true},
		{"explicit zero", Response{Content: "ok", UsageReported: true}, nil, "success", true, false},
		{"cancel known", Response{Usage: Usage{InputTokens: 4}}, context.Canceled, "cancelled", true, false}, //nolint:misspell // Preserve the existing persisted event and metric status contract.
		{"cache", Response{Cached: true, Usage: Usage{InputTokens: 20}}, nil, "cache_hit", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempts []Attempt
			p := NewObservingProvider(observedFixture{test.resp, test.err}, AttemptMetadata{Provider: "deepseek", RequestedModel: "requested", ProfileID: "fallback"},
				func(ctx context.Context, a Attempt) {
					if ctx.Err() != nil {
						t.Error("observer inherited cancellation")
					}
					attempts = append(attempts, a)
				})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			resp, _ := p.Complete(ctx, Request{ChatID: "chat", UserID: "actor", ProfileID: "resolved", Role: "vision", PolicyRevision: 7, Operation: "probe"})
			if !resp.UsageObserved || len(attempts) != 1 {
				t.Fatalf("observation missing: %+v %d", resp, len(attempts))
			}
			a := attempts[0]
			if a.Status != test.status || a.UsageAvailable != test.available || a.ChargeUnknown != test.unknown || a.ProfileID != "resolved" || a.PolicyRevision != 7 || a.Role != "vision" || a.Operation != "probe" || a.AttemptID == "" || a.CompletedAt.Before(a.StartedAt) {
				t.Fatalf("attempt metadata: %+v", a)
			}
			if test.resp.Cached && (a.Usage != (Usage{}) || resp.Usage != (Usage{})) {
				t.Fatal("cached tokens billed twice")
			}
		})
	}
}

func TestNestedObserversDoNotDoubleCount(t *testing.T) {
	count := 0
	observe := func(context.Context, Attempt) { count++ }
	inner := NewObservingProvider(observedFixture{response: Response{UsageReported: true}}, AttemptMetadata{}, observe)
	outer := NewObservingProvider(inner, AttemptMetadata{}, observe)
	if _, err := outer.Complete(context.Background(), Request{}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("got %d observations, want 1", count)
	}
}

type retryFixture struct{ calls int }
type observedRetryError struct{}

func (observedRetryError) Error() string   { return "rate limited" }
func (observedRetryError) StatusCode() int { return 429 }
func (p *retryFixture) Complete(context.Context, Request) (Response, error) {
	p.calls++
	if p.calls == 1 {
		return Response{Usage: Usage{InputTokens: 2}}, observedRetryError{}
	}
	return Response{Content: "ok", Usage: Usage{InputTokens: 3}}, nil
}

func TestObserverRecordsEachRetryAttempt(t *testing.T) {
	var attempts []Attempt
	p := NewRetryProvider(NewObservingProvider(&retryFixture{}, AttemptMetadata{Provider: "deepseek"}, func(_ context.Context, a Attempt) { attempts = append(attempts, a) }), RetryConfig{MaxAttempts: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
	if _, err := p.Complete(context.Background(), Request{}); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Status != "error" || attempts[1].Status != "success" || attempts[0].AttemptID == attempts[1].AttemptID || attempts[0].Usage.InputTokens+attempts[1].Usage.InputTokens != 5 {
		t.Fatalf("retry attempts: %+v", attempts)
	}
}

func TestObserverStreamingPartialFailureAndConcurrentCalls(t *testing.T) {
	var mu sync.Mutex
	ids := map[string]bool{}
	p := NewObservingProvider(observedFixture{Response{Content: "visible", Usage: Usage{OutputTokens: 1}}, ErrIncompleteResponse}, AttemptMetadata{}, func(_ context.Context, a Attempt) {
		mu.Lock()
		defer mu.Unlock()
		if ids[a.AttemptID] {
			t.Error("duplicate attempt ID")
		}
		ids[a.AttemptID] = true
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			visible := ""
			resp, err := p.CompleteStream(context.Background(), Request{}, func(s string) { visible += s })
			if !errors.Is(err, ErrIncompleteResponse) || visible != "visible" || resp.Content != "visible" || !resp.UsageObserved {
				t.Error("lost partial streaming result")
			}
		}()
	}
	wg.Wait()
	if len(ids) != 20 {
		t.Fatalf("got %d attempts", len(ids))
	}
}
