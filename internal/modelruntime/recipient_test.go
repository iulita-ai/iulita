package modelruntime

import (
	"testing"

	"github.com/iulita-ai/iulita/internal/models"
)

func TestBindingsDiscloseEffectiveDefaultAndCustomRecipients(t *testing.T) {
	manager := newTestManager(t, &memoryRepo{}, echoFactory)
	for _, test := range []struct {
		provider, endpoint, expected string
	}{
		{"claude", "", "https://api.anthropic.com"},
		{"openai", "", "https://api.openai.com/v1"},
		{"claude", "https://gateway.test/custom", "https://gateway.test/custom"},
		{"ollama", "http://127.0.0.1:11434", "http://127.0.0.1:11434"},
	} {
		t.Run(test.provider+test.endpoint, func(t *testing.T) {
			profile := models.Profile{ID: "fixture", Connection: test.provider, Model: "fixture-model", Thinking: "disabled", MaxOutputTokens: 128}
			bindings, err := manager.buildBindingsLocked(models.Settings{Profiles: []models.Profile{profile}}, map[string]secretConnection{test.provider: {Connection: Connection{Provider: test.provider, Endpoint: test.endpoint, Enabled: true, Generation: "fixture-generation"}}}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if binding := bindings[profile.ID]; binding.Endpoint != test.expected || binding.ConnectionGeneration != "fixture-generation" {
				t.Fatalf("recipient metadata changed or disappeared: %+v", binding)
			}
		})
	}
}
