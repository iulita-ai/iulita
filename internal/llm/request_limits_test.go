package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestOutputLimitNeverEnlargesAndSeparatesCache(t *testing.T) {
	for _, tc := range []struct{ configured, request, want int64 }{{4096, 1000, 1000}, {512, 1000, 512}, {4096, 0, 4096}, {0, 1000, 1000}} {
		got, err := (Request{MaxOutputTokens: tc.request}).OutputTokenLimit(tc.configured)
		if err != nil || got != tc.want {
			t.Fatalf("limit %+v: %d %v", tc, got, err)
		}
	}
	if _, err := (Request{MaxOutputTokens: -1}).OutputTokenLimit(4096); err == nil {
		t.Fatal("negative cap accepted")
	}
	one := Request{Message: "same", MaxOutputTokens: 1000}
	two := one
	two.MaxOutputTokens = 100
	if responseCacheKey(one) == responseCacheKey(two) {
		t.Fatal("different output ceilings share a cached response")
	}
}

func TestRequestAdmissionGuardNeverSerializes(t *testing.T) {
	encoded, err := json.Marshal(Request{Message: "fixture", BeforeRequest: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["BeforeRequest"]; exists {
		t.Fatal("local authority callback entered serialized request")
	}
}

func TestSingleAttemptHTTPClientPreservesSharedConfiguration(t *testing.T) {
	redirects := 0
	client := &http.Client{Timeout: 37 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { redirects++; return nil }}
	clone := RequestHTTPClient(client, Request{SingleAttempt: true})
	if clone == client || clone.Timeout != client.Timeout || clone.Transport != client.Transport || !errors.Is(clone.CheckRedirect(nil, nil), http.ErrUseLastResponse) || redirects != 0 {
		t.Fatal("single-attempt HTTP configuration was not isolated")
	}
	if err := client.CheckRedirect(nil, nil); err != nil || redirects != 1 {
		t.Fatal("shared redirect policy mutated")
	}
	if RequestHTTPClient(client, Request{}) != client {
		t.Fatal("ordinary request client changed")
	}
}

func TestSingleAttemptRevokeBeforeHTTPAdmissionMakesZeroRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var revoked atomic.Bool
	var requests atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	base := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, context.Canceled
	})}
	client := RequestHTTPClient(base, Request{SingleAttempt: true, BeforeRequest: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if revoked.Load() {
			return context.Canceled
		}
		return nil
	}})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/completion", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		response, callErr := client.Do(request)
		if response != nil {
			response.Body.Close()
		}
		done <- callErr
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	revoked.Store(true)
	close(release)
	if err := <-done; err == nil || requests.Load() != 0 {
		t.Fatalf("revoked request entered transport: %d %v", requests.Load(), err)
	}
}

func TestSingleAttemptStopsRetryFallbackAndClassification(t *testing.T) {
	ctx := context.Background()
	primary := &routingCapture{err: statusFailure(503)}
	backup := &routingCapture{resp: Response{Content: "backup"}}
	retry := NewRetryProvider(primary, RetryConfig{MaxAttempts: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
	if _, err := NewFallbackProvider(retry, backup).Complete(ctx, Request{Message: "private", SingleAttempt: true}); err == nil {
		t.Fatal("expected upstream failure")
	}
	if len(primary.reqs) != 1 || len(backup.reqs) != 0 {
		t.Fatalf("unexpected attempts %d/%d", len(primary.reqs), len(backup.reqs))
	}
	classifier := &routingCapture{resp: Response{Content: "complex"}}
	final := &routingCapture{resp: Response{Content: "answer"}}
	p := NewClassifyingProvider(classifier, NewRoutingProvider(final, nil))
	if _, err := p.Complete(ctx, Request{Message: "private", SingleAttempt: true}); err != nil {
		t.Fatal(err)
	}
	if len(classifier.reqs) != 0 || len(final.reqs) != 1 {
		t.Fatal("single attempt invoked a classifier")
	}
}

func TestSingleAttemptProfileFallbackAndStreaming(t *testing.T) {
	for _, stream := range []bool{false, true} {
		primary := &scenarioProvider{run: func(context.Context, Request, StreamCallback) (Response, error) {
			return Response{}, statusFailure(503)
		}}
		backup := &scenarioProvider{run: func(context.Context, Request, StreamCallback) (Response, error) {
			return Response{Content: "backup"}, nil
		}}
		router := fallbackRouter(t, primary, backup)
		req := Request{Message: "private", SingleAttempt: true}
		var err error
		if stream {
			_, err = router.CompleteStream(context.Background(), req, nil)
		} else {
			_, err = router.Complete(context.Background(), req)
		}
		if err == nil || primary.calls != 1 || backup.calls != 0 {
			t.Fatalf("stream=%v attempts=%d/%d err=%v", stream, primary.calls, backup.calls, err)
		}
	}
}

func TestResolveOperationIsLocalEligibleRoleResolution(t *testing.T) {
	snapshot := testProfileSnapshot(t, 1, nil)
	binding, err := snapshot.ResolveOperation("background")
	if err != nil || binding.Profile.ID != snapshot.policy.Background || binding.Provider == nil {
		t.Fatalf("local role resolution: %+v %v", binding.Profile, err)
	}
	if _, err := snapshot.ResolveOperation("unknown-role"); err == nil {
		t.Fatal("unknown role resolved")
	}
	var absent *ProfileSnapshot
	if _, err := absent.ResolveOperation("everyday"); err == nil {
		t.Fatal("nil snapshot resolved")
	}
}
