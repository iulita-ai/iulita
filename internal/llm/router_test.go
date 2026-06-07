package llm

import (
	"context"
	"sync"
	"testing"
)

type stubProvider struct{ name string }

func (s stubProvider) Complete(_ context.Context, _ Request) (Response, error) {
	return Response{Provider: s.name}, nil
}

func TestRoutingProvider_DefaultAndHints(t *testing.T) {
	def := stubProvider{name: "claude"}
	ds := stubProvider{name: "deepseek"}
	r := NewRoutingProvider(def, map[string]Provider{"deepseek": ds})

	// Unhinted request -> default.
	got, _ := r.Complete(context.Background(), Request{Message: "hi"})
	if got.Provider != "claude" {
		t.Errorf("default route = %q, want claude", got.Provider)
	}
	// RouteHint -> deepseek.
	got, _ = r.Complete(context.Background(), Request{RouteHint: "deepseek"})
	if got.Provider != "deepseek" {
		t.Errorf("hinted route = %q, want deepseek", got.Provider)
	}
}

func TestRoutingProvider_SetDefault(t *testing.T) {
	def := stubProvider{name: "claude"}
	ds := stubProvider{name: "deepseek"}
	r := NewRoutingProvider(def, nil)

	if got, _ := r.Complete(context.Background(), Request{}); got.Provider != "claude" {
		t.Fatalf("initial default = %q, want claude", got.Provider)
	}
	// Live-switch the default (e.g. dashboard claude -> deepseek).
	r.SetDefault(ds)
	if got, _ := r.Complete(context.Background(), Request{}); got.Provider != "deepseek" {
		t.Errorf("after SetDefault = %q, want deepseek", got.Provider)
	}
	// nil is ignored (keeps current default).
	r.SetDefault(nil)
	if got, _ := r.Complete(context.Background(), Request{}); got.Provider != "deepseek" {
		t.Errorf("SetDefault(nil) must not change default, got %q", got.Provider)
	}
}

func TestRoutingProvider_SetDefaultRace(t *testing.T) {
	r := NewRoutingProvider(stubProvider{name: "a"}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.SetDefault(stubProvider{name: "b"}) }()
		go func() { defer wg.Done(); _, _ = r.Complete(context.Background(), Request{}) }()
	}
	wg.Wait()
}

func TestRoutingProvider_SetRoute(t *testing.T) {
	def := stubProvider{name: "default"}
	light := stubProvider{name: "light-provider"}
	r := NewRoutingProvider(def, nil)

	// Before SetRoute: the hint falls through to the default.
	if got, _ := r.Complete(context.Background(), Request{RouteHint: "light"}); got.Provider != "default" {
		t.Errorf("before SetRoute: routed to %q, want default", got.Provider)
	}
	// After SetRoute: routes to the registered provider.
	r.SetRoute("light", light)
	if got, _ := r.Complete(context.Background(), Request{RouteHint: "light"}); got.Provider != "light-provider" {
		t.Errorf("after SetRoute: routed to %q, want light-provider", got.Provider)
	}
	// SetRoute(hint, nil) removes the route → back to default.
	r.SetRoute("light", nil)
	if got, _ := r.Complete(context.Background(), Request{RouteHint: "light"}); got.Provider != "default" {
		t.Errorf("after SetRoute(nil): routed to %q, want default", got.Provider)
	}
}

func TestRoutingProvider_SetRouteRace(t *testing.T) {
	r := NewRoutingProvider(stubProvider{name: "default"}, nil)
	p := stubProvider{name: "light-provider"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); r.SetRoute("light", p) }()
		go func() { defer wg.Done(); r.SetRoute("light", nil) }()
		go func() { defer wg.Done(); _, _ = r.Complete(context.Background(), Request{RouteHint: "light"}) }()
	}
	wg.Wait()
}
