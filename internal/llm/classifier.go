package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ClassifyingProvider auto-classifies queries and routes to appropriate providers.
type ClassifyingProvider struct {
	classifier Provider // cheap provider for classification (e.g. Ollama)
	router     *RoutingProvider
}

// NewClassifyingProvider creates a provider that classifies queries before routing.
func NewClassifyingProvider(classifier Provider, router *RoutingProvider) *ClassifyingProvider {
	return &ClassifyingProvider{
		classifier: classifier,
		router:     router,
	}
}

// Complete classifies the message and routes to the appropriate provider.
func (p *ClassifyingProvider) Complete(ctx context.Context, req Request) (Response, error) {
	// Only classify if no RouteHint is already set.
	if p.shouldClassify(req) {
		hint := p.classifyRequest(ctx, req)
		if hint != "" {
			req.RouteHint = hint
		}
	}
	return p.router.Complete(ctx, req)
}

// CompleteStream classifies the message and routes streaming to the appropriate provider.
func (p *ClassifyingProvider) CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error) {
	if p.shouldClassify(req) {
		hint := p.classifyRequest(ctx, req)
		if hint != "" {
			req.RouteHint = hint
		}
	}
	return p.router.CompleteStream(ctx, req, callback)
}

// classify asks the classifier to categorize the message.
func (p *ClassifyingProvider) classify(ctx context.Context, message string) string {
	return classifyWith(ctx, message, p.classifier)
}

func (p *ClassifyingProvider) classifyRequest(ctx context.Context, req Request) string {
	s := req.RoutingSnapshot
	if s == nil {
		s = p.router.AcquireProfileSnapshot()
	}
	classifier := p.classifier
	if s != nil {
		if !s.policy.Classifier.Enabled {
			return ""
		}
		id := s.policy.Classifier.Profile
		if err := s.eligible(id); err != nil {
			return ""
		}
		// Use the same registry, current invocation guard and observer as all
		// other calls. The selector can return categories, never credentials.
		classifier = &snapshotClassifier{router: p.router, snapshot: s, profileID: id, origin: req}
	}
	return classifyWith(ctx, req.Message, &classifierMetadata{inner: classifier, origin: req})
}

type snapshotClassifier struct {
	router    *RoutingProvider
	snapshot  *ProfileSnapshot
	profileID string
	origin    Request
}

func (p *snapshotClassifier) Complete(ctx context.Context, req Request) (Response, error) {
	req.ChatID = p.origin.ChatID
	req.UserID = p.origin.UserID
	req.Operation = "classifier"
	req.ProfileID = p.profileID
	req.RoutingSnapshot = p.snapshot
	return p.router.Complete(ctx, req)
}

func classifyWith(ctx context.Context, message string, classifier Provider) string {
	if classifier == nil {
		return ""
	}
	// Bound complete Unicode code points; do not send attachments or history.
	runes := []rune(message)
	if len(runes) > 500 {
		runes = runes[:500]
	}
	msg := string(runes)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	classReq := Request{
		SystemPrompt: "You are a query classifier. Respond with exactly one word.",
		Message: fmt.Sprintf(
			"Classify this user message into exactly one category: simple, complex, creative. Reply with just the category word.\n\nMessage: %s",
			msg,
		),
	}

	resp, err := classifier.Complete(ctx, classReq)
	if err != nil || resp.FinishReason == "length" || len(resp.Content) > 32 {
		return "" // fall through to default on error
	}

	category := strings.TrimSpace(strings.ToLower(resp.Content))

	// Validate the classification.
	switch category {
	case "simple", "complex", "creative":
		return category
	default:
		return "" // unrecognized category, fall through to default
	}
}

// Structured profile/hint/role and a user prefix precede classification. The
// selector runs only at an ambiguous top-level text request, never a tool loop.
func (p *ClassifyingProvider) shouldClassify(req Request) bool {
	if req.ProfileID != "" || req.Role != "" || req.RouteHint != "" || strings.HasPrefix(req.Message, "hint:") || len(req.ToolExchanges) > 0 || len(req.Images) > 0 || len(req.Documents) > 0 || strings.TrimSpace(req.Message) == "" {
		return false
	}
	s := req.RoutingSnapshot
	if s == nil {
		s = p.router.AcquireProfileSnapshot()
	}
	if s != nil {
		return s.policy.Classifier.Enabled
	}
	return true
}
func (p *ClassifyingProvider) AcquireProfileSnapshot() *ProfileSnapshot {
	return p.router.AcquireProfileSnapshot()
}

type classifierMetadata struct {
	inner  Provider
	origin Request
}

func (p *classifierMetadata) Complete(ctx context.Context, req Request) (Response, error) {
	if p.inner == nil {
		return Response{}, fmt.Errorf("classifier unavailable")
	}
	req.ChatID = p.origin.ChatID
	req.UserID = p.origin.UserID
	req.Operation = "classifier"
	return p.inner.Complete(ctx, req)
}
