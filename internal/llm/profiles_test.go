package llm

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/models"
)

func testProfileSnapshot(t *testing.T, revision uint64, edit func(*models.Settings)) *ProfileSnapshot {
	t.Helper()
	settings := models.Settings{SchemaVersion: 1, Profiles: models.CandidateProfiles(), Policy: models.Policy{Everyday: "ds-flash", Complex: "ds-pro", Vision: "glm-flash", Background: "glm-flash", LegacyHints: map[string]string{"light": "background", "complex": "complex"}}}
	if edit != nil {
		edit(&settings)
	}
	bindings := map[string]ProfileBinding{}
	for _, p := range settings.Profiles {
		d, _ := models.Lookup(p.Connection, p.Model)
		bindings[p.ID] = ProfileBinding{Profile: p, Provider: stubProvider{name: p.ID}, Eligibility: "production_eligible", Images: d.Images}
	}
	snapshot, err := NewProfileSnapshot(revision, settings, bindings)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func TestRoutingProfilesExplicitReferenceDistinctFromLegacyHint(t *testing.T) {
	r := NewRoutingProvider(stubProvider{name: "legacy"}, nil)
	if err := r.SetProfileSnapshot(testProfileSnapshot(t, 1, nil)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		req  Request
		want string
	}{
		{Request{}, "ds-flash"},
		{Request{ProfileID: "ds-pro", RouteHint: "light", Message: "hint:complex hi"}, "ds-pro"},
		{Request{RouteHint: "ds-pro"}, "ds-flash"},
		{Request{RouteHint: "light"}, "glm-flash"},
		{Request{Message: "hint:complex hi"}, "ds-pro"},
		{Request{ProfileID: "ds-pro", Images: []ImageAttachment{{Data: []byte("fixture")}}}, "glm-flash"},
	} {
		got, err := r.Complete(context.Background(), tc.req)
		if err != nil || got.Provider != tc.want {
			t.Fatalf("got %s err %v want %s", got.Provider, err, tc.want)
		}
	}
	if _, err := r.Complete(context.Background(), Request{ProfileID: "missing"}); err == nil {
		t.Fatal("unknown explicit profile silently fell through")
	}
}
func TestRoutingSnapshotPinsTurnAndCurrentDenyWins(t *testing.T) {
	r := NewRoutingProvider(nil, nil)
	first := testProfileSnapshot(t, 1, nil)
	if err := r.SetProfileSnapshot(first); err != nil {
		t.Fatal(err)
	}
	second := testProfileSnapshot(t, 2, func(s *models.Settings) { s.Policy.Everyday = "glm-main" })
	if err := r.SetProfileSnapshot(second); err != nil {
		t.Fatal(err)
	}
	got, err := r.Complete(context.Background(), Request{RoutingSnapshot: first})
	if err != nil || got.Provider != "ds-flash" || got.PolicyRevision != 1 {
		t.Fatalf("turn changed: %+v %v", got, err)
	}
	third := testProfileSnapshot(t, 3, func(s *models.Settings) {
		s.Policy.Everyday = "glm-main"
		s.Policy.Complex = "glm-main"
		s.Policy.ForbiddenProviders = []string{"deepseek"}
	})
	if err := r.SetProfileSnapshot(third); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Complete(context.Background(), Request{RoutingSnapshot: first}); err == nil {
		t.Fatal("old snapshot bypassed current deny")
	}
	if err := r.SetProfileSnapshot(second); err == nil {
		t.Fatal("old revision overwrote current")
	}
}
func TestRoutingRejectsExperimentalAndMixedToolContinuation(t *testing.T) {
	settings := models.Settings{SchemaVersion: 1, Profiles: models.CandidateProfiles(), Policy: models.Policy{Everyday: "ds-flash"}}
	bindings := map[string]ProfileBinding{}
	for _, p := range settings.Profiles {
		bindings[p.ID] = ProfileBinding{Profile: p, Provider: stubProvider{}, Eligibility: "experimental"}
	}
	if _, err := NewProfileSnapshot(1, settings, bindings); err == nil {
		t.Fatal("experimental role activated")
	}
	r := NewRoutingProvider(nil, nil)
	_ = r.SetProfileSnapshot(testProfileSnapshot(t, 1, nil))
	if _, err := r.Complete(context.Background(), Request{ProfileID: "glm-flash", ToolExchanges: []ToolExchange{{ProfileID: "ds-flash"}}}); err == nil {
		t.Fatal("mixed tool continuation accepted")
	}
}
func TestRoutingOwnsLegacyMap(t *testing.T) {
	routes := map[string]Provider{"light": stubProvider{name: "old"}}
	r := NewRoutingProvider(stubProvider{name: "default"}, routes)
	routes["light"] = stubProvider{name: "mutated"}
	got, _ := r.Complete(context.Background(), Request{RouteHint: "light"})
	if got.Provider != "old" {
		t.Fatal("caller mutated owned map")
	}
}
