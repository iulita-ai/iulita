package main

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
)

type stubLLM struct{ name string }

func (s stubLLM) Complete(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{Provider: s.name}, nil
}

func TestResolveLightRoute(t *testing.T) {
	haiku := stubLLM{name: "claude-haiku"}
	deepseek := stubLLM{name: "deepseek"}
	pm := map[string]llm.Provider{"claude-haiku": haiku, "deepseek": deepseek}

	tests := []struct {
		name     string
		enabled  bool
		provider string
		wantName string
		wantSet  bool
		wantProv string // expected resolved provider name, "" if none
	}{
		{"enabled+registered", true, "deepseek", "deepseek", true, "deepseek"},
		{"enabled+empty defaults to haiku", true, "", "claude-haiku", true, "claude-haiku"},
		{"enabled+unregistered falls through", true, "openai", "openai", false, ""},
		{"disabled removes route", false, "deepseek", "deepseek", false, ""},
		{"disabled+empty still defaults name", false, "", "claude-haiku", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, prov, set := resolveLightRoute(tt.enabled, tt.provider, pm)
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if set != tt.wantSet {
				t.Errorf("set = %v, want %v", set, tt.wantSet)
			}
			if tt.wantProv == "" {
				if prov != nil {
					t.Errorf("provider = %v, want nil", prov)
				}
			} else {
				got, _ := prov.Complete(context.Background(), llm.Request{})
				if got.Provider != tt.wantProv {
					t.Errorf("provider = %q, want %q", got.Provider, tt.wantProv)
				}
			}
		})
	}
}

// Disable -> enable round trip must re-resolve to the configured provider.
func TestResolveLightRoute_RoundTrip(t *testing.T) {
	pm := map[string]llm.Provider{"deepseek": stubLLM{name: "deepseek"}}
	if _, _, set := resolveLightRoute(true, "deepseek", pm); !set {
		t.Fatal("enable: expected set=true")
	}
	if _, _, set := resolveLightRoute(false, "deepseek", pm); set {
		t.Fatal("disable: expected set=false")
	}
	if _, p, set := resolveLightRoute(true, "deepseek", pm); !set || p == nil {
		t.Fatal("re-enable: expected set=true with provider")
	}
}
