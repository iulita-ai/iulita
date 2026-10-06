package deepseek_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/llm/deepseek"
	"github.com/iulita-ai/iulita/internal/llm/zai"
	"github.com/iulita-ai/iulita/internal/models"
)

// Inject failure at the real HTTP adapter boundary, without changing production
// credentials. Both real attempts must keep separate identities and billing.
func TestProfileFailoverHTTPAndAttemptAccounting(t *testing.T) {
	for _, status := range []int{503, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var primaryCalls, backupCalls atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"synthetic outage"}}`))
			}))
			defer primary.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "reply", "model": "deepseek-flash", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "backup success"}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 3}})
			}))
			defer backup.Close()
			settings := models.Settings{SchemaVersion: 1, Profiles: models.CandidateProfiles(), Policy: models.Policy{Everyday: "glm-flash", Fallbacks: map[string][]string{"everyday": {"ds-flash"}}}}
			var attempts []llm.Attempt
			observe := func(_ context.Context, a llm.Attempt) { attempts = append(attempts, a) }
			bindings := map[string]llm.ProfileBinding{}
			for _, profile := range settings.Profiles {
				var client llm.Provider
				switch profile.ID {
				case "glm-flash":
					client = zai.New("synthetic-key", profile.Model, 512, primary.URL, primary.Client(), nil)
				case "ds-flash":
					client = deepseek.NewWithOptions("synthetic-key", profile.Model, 512, backup.URL, backup.Client(), nil, deepseek.Options{Thinking: "disabled"})
				default:
					continue
				}
				bindings[profile.ID] = llm.ProfileBinding{Profile: profile, Eligibility: "production_eligible", Provider: llm.NewObservingProvider(client, llm.AttemptMetadata{Provider: profile.Connection, RequestedModel: profile.Model, ProfileID: profile.ID}, observe)}
			}
			snapshot, err := llm.NewProfileSnapshot(7, settings, bindings)
			if err != nil {
				t.Fatal(err)
			}
			router := llm.NewRoutingProvider(nil, nil)
			if err = router.SetProfileSnapshot(snapshot); err != nil {
				t.Fatal(err)
			}
			response, err := router.Complete(context.Background(), llm.Request{UserID: "synthetic-user", Operation: "chat", Message: "synthetic check"})
			if primaryCalls.Load() != 1 {
				t.Fatal("primary retried unexpectedly")
			}
			if status == 401 {
				if err == nil || backupCalls.Load() != 0 || len(attempts) != 1 {
					t.Fatal("authentication triggered cross-provider fallback")
				}
				return
			}
			if err != nil || response.ProfileID != "ds-flash" || response.Content != "backup success" || backupCalls.Load() != 1 || len(attempts) != 2 {
				t.Fatalf("HTTP fallback failed: %+v %v attempts=%d", response, err, len(attempts))
			}
			if attempts[0].ProfileID != "glm-flash" || attempts[0].Status != "error" || !attempts[0].ChargeUnknown || attempts[1].ProfileID != "ds-flash" || attempts[1].Status != "success" || !attempts[1].UsageAvailable || attempts[1].Usage.InputTokens != 12 || attempts[1].PolicyRevision != 7 || attempts[1].Operation != "chat" {
				t.Fatalf("attempt provenance/accounting lost: %+v", attempts)
			}
		})
	}
}
