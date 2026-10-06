package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/modelruntime"
	"github.com/iulita-ai/iulita/internal/models"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func TestJobProfileNullableUpdatePreservesOrClearsOverride(t *testing.T) {
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	job := &domain.AgentJob{Name: "job", Prompt: "prompt", ProfileID: "old-profile", Model: "old-hint", Interval: "24h", Enabled: true}
	if err := store.CreateAgentJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, logger: zap.NewNop()}
	app := fiber.New()
	app.Put("/jobs/:id", s.handleUpdateAgentJob)
	for _, test := range []struct {
		body           string
		code           int
		profile, model string
	}{
		{`{"name":"renamed"}`, 200, "old-profile", "old-hint"},
		{`{"profile_id":null}`, 200, "", ""},
		{`{"profile_id":12}`, 400, "", ""},
		{`{"profile_id":{}}`, 400, "", ""},
		{`{"profile_id":"unavailable-profile"}`, 422, "", ""},
		{`{"model":"raw-model-name"}`, 422, "", ""},
	} {
		req := httptest.NewRequest(http.MethodPut, "/jobs/1", strings.NewReader(test.body))
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != test.code {
			t.Fatalf("%s: status %d, want %d", test.body, res.StatusCode, test.code)
		}
		stored, err := store.GetAgentJob(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.ProfileID != test.profile || stored.Model != test.model {
			t.Fatalf("%s: override changed incorrectly: %+v", test.body, stored)
		}
	}
}

func TestJobProfileEligibilityUsesCurrentActiveGraph(t *testing.T) {
	base := modelruntime.View{Revision: 3, ActiveRevision: 3, ActivationStatus: "applied", Settings: models.Settings{Policy: models.Policy{Everyday: "selected"}, Profiles: []models.Profile{{ID: "selected", Connection: "zai"}}}, Profiles: []modelruntime.ProfileView{{ID: "selected", Eligibility: "production_eligible"}}, Connections: []modelruntime.ConnectionView{{Provider: "zai", CredentialSet: true, Availability: "untested"}}}
	if !agentJobProfileEligible(base, "selected") {
		t.Fatal("eligible active profile rejected")
	}
	for _, change := range []func(*modelruntime.View){
		func(v *modelruntime.View) { v.Settings.Policy.ForbiddenProviders = []string{"zai"} },
		func(v *modelruntime.View) {
			v.Connections = []modelruntime.ConnectionView{{Provider: "zai", CredentialSet: true, Availability: "suspended"}}
		},
		func(v *modelruntime.View) {
			v.Profiles = []modelruntime.ProfileView{{ID: "selected", Eligibility: "experimental"}}
		},
		func(v *modelruntime.View) { v.ActiveRevision = 2 },
		func(v *modelruntime.View) { v.ActivationStatus = "failed" },
	} {
		v := base
		change(&v)
		if agentJobProfileEligible(v, "selected") {
			t.Fatal("ineligible/stale profile accepted")
		}
	}
	if agentJobProfileEligible(base, "raw-model-name") {
		t.Fatal("raw model mistaken for profile ID")
	}
}
