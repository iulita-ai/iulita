package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/models"
)

type routingCapture struct {
	reqs []Request
	resp Response
	err  error
}

func (p *routingCapture) Complete(_ context.Context, r Request) (Response, error) {
	p.reqs = append(p.reqs, r)
	return p.resp, p.err
}
func TestClassifierUsesPinnedGuardedProfileAndSkipsToolLoop(t *testing.T) {
	first := testProfileSnapshot(t, 1, nil)
	selector := first.profiles["ds-flash"].Profile
	selector.ID = "selector"
	selector.MaxOutputTokens = 512
	first.profiles[selector.ID] = ProfileBinding{Profile: selector, Eligibility: "production_eligible", Provider: &routingCapture{resp: Response{Content: "complex"}}}
	first.policy.Classifier = models.Classifier{Enabled: true, Profile: selector.ID}
	captured := first.profiles[selector.ID].Provider.(*routingCapture)
	r := NewRoutingProvider(nil, nil)
	_ = r.SetProfileSnapshot(first)
	later := testProfileSnapshot(t, 2, nil)
	_ = r.SetProfileSnapshot(later)
	old := &routingCapture{resp: Response{Content: "simple"}}
	p := NewClassifyingProvider(old, r)
	res, err := p.Complete(context.Background(), Request{RoutingSnapshot: first, Message: strings.Repeat("я", 600)})
	if err != nil || res.ProfileID != "ds-pro" || len(old.reqs) != 0 || len(captured.reqs) != 1 || !utf8.ValidString(captured.reqs[0].Message) || strings.Count(captured.reqs[0].Message, "я") != 500 {
		t.Fatalf("classifier drift or invalid Unicode: %+v %v", res, err)
	}
	_, _ = p.Complete(context.Background(), Request{RoutingSnapshot: first, ProfileID: "ds-pro", ToolExchanges: []ToolExchange{{ProfileID: "ds-pro"}}})
	if len(captured.reqs) != 1 {
		t.Fatal("paid classifier repeated in tool loop")
	}
}
func TestFallbackStopsPermanentFailureOrEffects(t *testing.T) {
	for _, tc := range []struct {
		req      Request
		resp     Response
		err      error
		fallback bool
	}{
		{err: errors.New("invalid input")}, {err: context.Canceled}, {err: observedRetryError{}, fallback: true},
		{req: Request{ToolExchanges: []ToolExchange{{}}}, err: observedRetryError{}},
		{resp: Response{Content: "partial"}, err: observedRetryError{}},
		{req: Request{ThinkingBudget: 1024}, err: observedRetryError{}},
	} {
		first := &routingCapture{resp: tc.resp, err: tc.err}
		second := &routingCapture{resp: Response{Content: "fallback"}}
		_, _ = NewFallbackProvider(first, second).Complete(context.Background(), tc.req)
		if (len(second.reqs) > 0) != tc.fallback {
			t.Fatalf("unsafe fallback: %+v", tc)
		}
	}
}
func TestTaskProviderUsesSharedBackgroundPolicy(t *testing.T) {
	r := NewRoutingProvider(nil, nil)
	_ = r.SetProfileSnapshot(testProfileSnapshot(t, 1, nil))
	p := NewTaskProvider(r, "", "heartbeat")
	res, err := p.Complete(context.Background(), Request{})
	if err != nil || res.ProfileID != "glm-flash" || res.Role != "background" {
		t.Fatalf("background bypass: %+v %v", res, err)
	}
}
