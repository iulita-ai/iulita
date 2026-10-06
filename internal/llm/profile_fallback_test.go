package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/models"
)

type scenarioProvider struct {
	calls int
	run   func(context.Context, Request, StreamCallback) (Response, error)
}

func (p *scenarioProvider) Complete(ctx context.Context, r Request) (Response, error) {
	p.calls++
	return p.run(ctx, r, nil)
}
func (p *scenarioProvider) CompleteStream(ctx context.Context, r Request, cb StreamCallback) (Response, error) {
	p.calls++
	return p.run(ctx, r, cb)
}

type statusFailure int

func (e statusFailure) Error() string   { return "synthetic failure" }
func (e statusFailure) StatusCode() int { return int(e) }
func fallbackRouter(t *testing.T, primary, backup Provider) *RoutingProvider {
	t.Helper()
	s := testProfileSnapshot(t, 1, func(settings *models.Settings) {
		settings.Policy.Everyday = "glm-flash"
		settings.Policy.Fallbacks = map[string][]string{"everyday": {"ds-flash"}}
	})
	b := s.profiles["glm-flash"]
	b.Provider = primary
	s.profiles["glm-flash"] = b
	b = s.profiles["ds-flash"]
	b.Provider = backup
	b.Images = false
	s.profiles["ds-flash"] = b
	r := NewRoutingProvider(nil, nil)
	if err := r.SetProfileSnapshot(s); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestProfileFallbackAdmissionAndContinuation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		req      Request
		resp     Response
		err      error
		chunk    string
		fallback bool
	}{
		{name: "unavailable", err: statusFailure(503), fallback: true},
		{name: "rate", err: statusFailure(429), fallback: true},
		{name: "gateway timeout", err: statusFailure(504), fallback: true},
		{name: "network", err: &AvailabilityError{Provider: "zai"}, fallback: true},
		{name: "auth", err: statusFailure(401)},
		{name: "credit", err: statusFailure(402)},
		{name: "forbidden", err: statusFailure(403)},
		{name: "input", err: statusFailure(400)},
		{name: "context", err: errors.New("context window exceeded")},
		{name: "canceled", err: context.Canceled},
		{name: "unowned deadline", err: context.DeadlineExceeded},
		{name: "partial response", err: statusFailure(503), resp: Response{Content: "partial"}},
		{name: "visible stream empty response", err: statusFailure(503), chunk: "visible"},
		{name: "tool calls", err: statusFailure(503), resp: Response{ToolCalls: []ToolCall{{ID: "action", Name: "write"}}}},
		{name: "tool continuation", err: statusFailure(503), req: Request{ToolExchanges: []ToolExchange{{ProfileID: "glm-flash", ReasoningContent: "private reasoning"}}}},
		{name: "explicit profile", err: statusFailure(503), req: Request{ProfileID: "glm-flash"}},
		{name: "classifier", err: statusFailure(503), req: Request{Operation: "classifier"}},
		{name: "image without verified backup", err: statusFailure(503), req: Request{Images: []ImageAttachment{{Data: []byte("synthetic")}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := &scenarioProvider{run: func(_ context.Context, _ Request, cb StreamCallback) (Response, error) {
				if cb != nil && tc.chunk != "" {
					cb(tc.chunk)
				}
				return tc.resp, tc.err
			}}
			backup := &scenarioProvider{run: func(_ context.Context, r Request, _ StreamCallback) (Response, error) {
				if len(r.ToolExchanges) > 0 || r.ProfileID != "ds-flash" || r.Role != "everyday" {
					t.Fatal("backup provenance lost")
				}
				return Response{Content: "backup", ToolCalls: []ToolCall{{ID: "next", Name: "read"}}}, nil
			}}
			r := fallbackRouter(t, primary, backup)
			response, err := r.CompleteStream(context.Background(), tc.req, func(string) {})
			if (backup.calls == 1) != tc.fallback {
				t.Fatalf("backup calls=%d error=%v", backup.calls, err)
			}
			if tc.fallback {
				if err != nil || response.ProfileID != "ds-flash" {
					t.Fatalf("fallback result=%+v err=%v", response, err)
				}
				// The returned profile pins the tool round; no second classification or
				// reverse transition is permitted even when the backup subsequently fails.
				backup.run = func(_ context.Context, r Request, _ StreamCallback) (Response, error) {
					if r.ProfileID != "ds-flash" || len(r.ToolExchanges) != 1 {
						t.Fatal("tool profile changed")
					}
					return Response{}, statusFailure(503)
				}
				_, err = r.Complete(context.Background(), Request{ProfileID: response.ProfileID, PinnedProfile: true, Role: response.Role, ToolExchanges: []ToolExchange{{ProfileID: response.ProfileID}}})
				if err == nil || primary.calls != 1 || backup.calls != 2 {
					t.Fatal("tool round retried across providers")
				}
			}
		})
	}
}
func TestProfileFallbackOwnDeadlineAndParentCancellation(t *testing.T) {
	primary := &scenarioProvider{run: func(ctx context.Context, _ Request, _ StreamCallback) (Response, error) {
		<-ctx.Done()
		return Response{}, ctx.Err()
	}}
	backup := &scenarioProvider{run: func(context.Context, Request, StreamCallback) (Response, error) { return Response{Content: "ok"}, nil }}
	r := fallbackRouter(t, primary, backup)
	r.snapshot.policy.FallbackTimeoutMS = 1 // Short test-only deadline.
	response, err := r.Complete(context.Background(), Request{})
	if err != nil || response.ProfileID != "ds-flash" || backup.calls != 1 {
		t.Fatalf("own deadline did not switch: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.Complete(ctx, Request{})
	if !errors.Is(err, context.Canceled) || primary.calls != 1 || backup.calls != 1 {
		t.Fatal("parent cancellation billed another call")
	}
}
func TestProfileFallbackContextCapacityAndExplicitHint(t *testing.T) {
	primary := &scenarioProvider{run: func(context.Context, Request, StreamCallback) (Response, error) {
		return Response{}, statusFailure(503)
	}}
	backup := &scenarioProvider{run: func(context.Context, Request, StreamCallback) (Response, error) { return Response{}, nil }}
	r := fallbackRouter(t, primary, backup)
	b := r.snapshot.profiles["ds-flash"]
	b.ContextTokens = 100
	r.snapshot.profiles["ds-flash"] = b
	response, err := r.Complete(context.Background(), Request{})
	if err == nil || backup.calls != 0 || response.ProfileID != "glm-flash" {
		t.Fatal("smaller context admitted or failed profile misreported")
	}
	b.ContextTokens = 1_000_000
	r.snapshot.profiles["ds-flash"] = b
	r.snapshot.policy.LegacyProfileHints["chosen"] = "glm-flash"
	for _, req := range []Request{{RouteHint: "chosen"}, {Message: "hint:chosen hello"}} {
		_, _ = r.Complete(context.Background(), req)
	}
	if backup.calls != 0 {
		t.Fatal("explicit profile hint ignored")
	}
}
func TestClassifierMapsManagedCategoriesAndBoundsPayload(t *testing.T) {
	for _, label := range []string{"simple", "complex", "creative", "malformed explanation"} {
		t.Run(label, func(t *testing.T) {
			s := testProfileSnapshot(t, 1, nil)
			profile := s.profiles["ds-flash"].Profile
			profile.ID = "selector"
			profile.MaxOutputTokens = 512
			selector := &scenarioProvider{run: func(_ context.Context, req Request, _ StreamCallback) (Response, error) {
				if len(req.History) > 0 || len(req.Tools) > 0 || len(req.ToolExchanges) > 0 || len(req.Images) > 0 || req.UserID != "actor" || req.Operation != "classifier" || strings.Count(req.Message, "я") != 500 {
					t.Fatal("classifier data or identity boundary violated")
				}
				return Response{Content: label, FinishReason: "stop"}, nil
			}}
			s.profiles[profile.ID] = ProfileBinding{Profile: profile, Provider: selector, Eligibility: "production_eligible", ClassifierVerified: true}
			s.policy.Classifier = models.Classifier{Enabled: true, Profile: profile.ID}
			s.policy.LegacyHints["simple"] = "complex"
			s.policy.LegacyHints["creative"] = "background"
			r := NewRoutingProvider(nil, nil)
			_ = r.SetProfileSnapshot(s)
			p := NewClassifyingProvider(nil, r)
			res, err := p.Complete(context.Background(), Request{UserID: "actor", Message: strings.Repeat("я", 600)})
			expected := "ds-pro"
			if label == "simple" || label == "malformed explanation" {
				expected = "ds-flash"
			}
			if err != nil || res.ProfileID != expected || selector.calls != 1 {
				t.Fatalf("category mapped incorrectly: %+v %v", res, err)
			}
			for _, req := range []Request{{Role: "complex", Message: "x"}, {RouteHint: "light", Message: "x"}, {Message: "hint:complex x"}, {Images: []ImageAttachment{{Data: []byte("x")}}}, {Documents: []DocumentAttachment{{}}}, {ProfileID: "ds-pro"}, {ToolExchanges: []ToolExchange{{}}}} {
				_, _ = p.Complete(context.Background(), req)
			}
			if selector.calls != 1 {
				t.Fatal("classifier charged explicit or attachment/tool requests")
			}
		})
	}
}
func TestClassificationRejectsTruncatedOrToolResponses(t *testing.T) {
	for _, r := range []Response{{Content: "complex", FinishReason: "length"}, {Content: "simple", ToolCalls: []ToolCall{{ID: "call"}}}, {Content: "simple", FinishReason: "content_filter"}, {Content: "creative\nmore"}} {
		if ParseClassification(r) != "" {
			t.Fatal("unsafe label accepted")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	p := &scenarioProvider{run: func(ctx context.Context, _ Request, _ StreamCallback) (Response, error) {
		<-ctx.Done()
		return Response{Content: "complex"}, nil
	}}
	if classifyBounded(ctx, "question", p, time.Second) != "" {
		t.Fatal("late classifier output accepted")
	}
}
