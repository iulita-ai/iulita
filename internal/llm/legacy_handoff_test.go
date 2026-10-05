package llm

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/models"
)

func legacyHandoffSnapshot(t *testing.T, eligibility string) *ProfileSnapshot {
	t.Helper()
	settings := models.Settings{SchemaVersion: 1, Profiles: []models.Profile{
		{ID: "sonnet", Name: "Sonnet", Connection: "claude", Model: "claude-sonnet-4-6", MaxOutputTokens: 4096, Thinking: "disabled"},
		{ID: "haiku", Name: "Haiku", Connection: "claude", Model: "claude-haiku-4-5", MaxOutputTokens: 4096, Thinking: "disabled"},
	}, Policy: models.Policy{Everyday: "sonnet", Background: "haiku"}}
	bindings := map[string]ProfileBinding{}
	for _, p := range settings.Profiles {
		bindings[p.ID] = ProfileBinding{Profile: p, Provider: stubProvider{name: p.ID}, Eligibility: eligibility}
	}
	s, err := NewProfileSnapshot(1, settings, bindings)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLegacyVisibleSynthesisHandoffIsNarrowAndOwnsOrigins(t *testing.T) {
	for _, tc := range []struct {
		name, eligibility string
		edit              func(*Request)
		want              string
		rejected          bool
	}{
		{"legacy visible", "legacy_preserved", func(*Request) {}, "haiku", false},
		{"explicit override", "legacy_preserved", func(r *Request) { r.PinnedProfile = false }, "sonnet", false},
		{"opaque reasoning", "legacy_preserved", func(r *Request) { r.ToolExchanges[0].ReasoningContent = "opaque" }, "sonnet", false},
		{"thinking budget", "legacy_preserved", func(r *Request) { r.ThinkingBudget = 1024 }, "sonnet", false},
		{"new profile", "production_eligible", func(*Request) {}, "sonnet", false},
		{"mixed origin", "legacy_preserved", func(r *Request) { r.ToolExchanges[0].ProfileID = "other" }, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRoutingProvider(nil, nil)
			if err := router.SetProfileSnapshot(legacyHandoffSnapshot(t, tc.eligibility)); err != nil {
				t.Fatal(err)
			}
			req := Request{ProfileID: "sonnet", PinnedProfile: true, RouteHint: RouteHintCheap, ToolExchanges: []ToolExchange{{ProfileID: "sonnet"}}}
			tc.edit(&req)
			original := req.ToolExchanges[0].ProfileID
			resp, err := router.Complete(context.Background(), req)
			if tc.rejected {
				if err == nil {
					t.Fatal("mixed origin accepted")
				}
				return
			}
			if err != nil || resp.ProfileID != tc.want || resp.LegacyVisibleHandoff != (tc.want == "haiku") {
				t.Fatalf("wrong handoff: %+v %v", resp, err)
			}
			if req.ToolExchanges[0].ProfileID != original {
				t.Fatal("router mutated caller history")
			}
		})
	}
}

func TestLegacyVisibleHandoffCannotBypassCurrentProviderBan(t *testing.T) {
	router := NewRoutingProvider(nil, nil)
	old := legacyHandoffSnapshot(t, "legacy_preserved")
	if err := router.SetProfileSnapshot(old); err != nil {
		t.Fatal(err)
	}
	current := testProfileSnapshot(t, 2, func(s *models.Settings) { s.Policy.ForbiddenProviders = []string{"claude"} })
	if err := router.SetProfileSnapshot(current); err != nil {
		t.Fatal(err)
	}
	_, err := router.Complete(context.Background(), Request{RoutingSnapshot: old, ProfileID: "sonnet", PinnedProfile: true, RouteHint: RouteHintCheap, ToolExchanges: []ToolExchange{{ProfileID: "sonnet"}}})
	if err == nil {
		t.Fatal("legacy handoff bypassed current ban")
	}
}
