package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// RoutingProvider routes requests to different providers based on hints.
type RoutingProvider struct {
	providers map[string]Provider // hint -> provider

	mu              sync.RWMutex
	defaultProvider Provider
	snapshot        *ProfileSnapshot
}

// NewRoutingProvider creates a routing provider with named routes and a default.
func NewRoutingProvider(defaultProvider Provider, routes map[string]Provider) *RoutingProvider {
	ownedRoutes := make(map[string]Provider, len(routes))
	for name, provider := range routes {
		ownedRoutes[name] = provider
	}
	return &RoutingProvider{
		providers:       ownedRoutes,
		defaultProvider: defaultProvider,
	}
}

// SetDefault swaps the default provider at runtime (thread-safe). Used to switch
// the active LLM (e.g. claude -> deepseek) from the dashboard without a restart.
func (p *RoutingProvider) SetDefault(provider Provider) {
	if provider == nil {
		return
	}
	p.mu.Lock()
	p.defaultProvider = provider
	p.mu.Unlock()
}

// SetRoute adds, updates, or removes a named route at runtime (thread-safe).
// A nil provider removes the route so the hint falls through to the default.
func (p *RoutingProvider) SetRoute(hint string, provider Provider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if provider == nil {
		delete(p.providers, hint)
		return
	}
	p.providers[hint] = provider
}

// Complete routes the request based on RouteHint or message prefix.
func (p *RoutingProvider) Complete(ctx context.Context, req Request) (Response, error) {
	if resp, handled, err := p.completeProfile(ctx, req, nil, false); handled {
		return resp, err
	}
	provider, modReq := p.resolveProvider(req)
	if provider == nil {
		return Response{}, fmt.Errorf("no model provider is available")
	}
	return provider.Complete(ctx, modReq)
}

// CompleteStream routes the request and delegates streaming.
func (p *RoutingProvider) CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error) {
	if resp, handled, err := p.completeProfile(ctx, req, callback, true); handled {
		return resp, err
	}
	provider, modReq := p.resolveProvider(req)
	if provider == nil {
		return Response{}, fmt.Errorf("no model provider is available")
	}
	if sp, ok := provider.(StreamingProvider); ok {
		return sp.CompleteStream(ctx, modReq, callback)
	}
	return provider.Complete(ctx, modReq)
}

// resolveProvider determines which provider to use and returns a potentially
// modified request. Holds the read lock for the whole lookup so a concurrent
// SetRoute/SetDefault cannot race the providers map or default.
func (p *RoutingProvider) resolveProvider(req Request) (Provider, Request) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Check RouteHint first.
	if req.RouteHint != "" {
		if provider, ok := p.providers[req.RouteHint]; ok {
			return provider, req
		}
	}

	// Check for "hint:" prefix in message.
	if strings.HasPrefix(req.Message, "hint:") {
		parts := strings.SplitN(req.Message[5:], " ", 2)
		if len(parts) >= 1 {
			hint := strings.TrimSpace(parts[0])
			if provider, ok := p.providers[hint]; ok {
				modReq := req
				if len(parts) == 2 {
					modReq.Message = parts[1]
				} else {
					modReq.Message = ""
				}
				return provider, modReq
			}
		}
	}

	return p.defaultProvider, req
}
