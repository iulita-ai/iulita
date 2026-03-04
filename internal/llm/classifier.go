package llm

import (
	"context"
	"fmt"
	"strings"
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
	if req.RouteHint == "" {
		hint := p.classify(ctx, req.Message)
		if hint != "" {
			req.RouteHint = hint
		}
	}
	return p.router.Complete(ctx, req)
}

// CompleteStream classifies the message and routes streaming to the appropriate provider.
func (p *ClassifyingProvider) CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error) {
	if req.RouteHint == "" {
		hint := p.classify(ctx, req.Message)
		if hint != "" {
			req.RouteHint = hint
		}
	}
	return p.router.CompleteStream(ctx, req, callback)
}

// classify asks the classifier to categorize the message.
func (p *ClassifyingProvider) classify(ctx context.Context, message string) string {
	// Truncate message for classification.
	msg := message
	if len(msg) > 500 {
		msg = msg[:500]
	}

	classReq := Request{
		SystemPrompt: "You are a query classifier. Respond with exactly one word.",
		Message: fmt.Sprintf(
			"Classify this user message into exactly one category: simple, complex, creative. Reply with just the category word.\n\nMessage: %s",
			msg,
		),
	}

	resp, err := p.classifier.Complete(ctx, classReq)
	if err != nil {
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
