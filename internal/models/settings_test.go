package models

import (
	"encoding/json"
	"testing"
)

func candidateSettings() Settings { return Settings{SchemaVersion: 1, Profiles: CandidateProfiles()} }
func TestCatalogPresetsAreDraftsAndCapabilitySpecific(t *testing.T) {
	s := candidateSettings()
	if errs := s.Validate(); len(errs) > 0 {
		t.Fatal(errs)
	}
	if s.Policy.Everyday != "" || s.Policy.Vision != "" || s.Policy.Classifier.Enabled {
		t.Fatal("preset silently activates models")
	}
	for _, tc := range []struct {
		provider, model string
		images          bool
	}{{"deepseek", "deepseek-flash", true}, {"deepseek", "deepseek-v4-pro", false}, {"zai", "glm-5.3-flash", true}, {"zai", "glm-5.3", false}} {
		d, ok := Lookup(tc.provider, tc.model)
		if !ok || d.Images != tc.images || d.ContextTokens != 1_000_000 {
			t.Fatalf("invalid catalog for %s", tc.model)
		}
	}
}
func TestInvalidSettingsCannotGrantCapabilitiesOrReferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Settings)
	}{
		{"duplicate", func(s *Settings) { s.Profiles[1].ID = s.Profiles[0].ID }},
		{"unknown role", func(s *Settings) { s.Policy.Everyday = "missing" }},
		{"text-only vision", func(s *Settings) { s.Policy.Vision = "ds-pro" }},
		{"GLM cannot disable thinking", func(s *Settings) { s.Profiles[2].Thinking = "disabled" }},
		{"GLM clear thinking", func(s *Settings) { s.Profiles[2].ClearThinking = false }},
		{"forbidden provider", func(s *Settings) { s.Policy.Everyday = "ds-flash"; s.Policy.ForbiddenProviders = []string{"deepseek"} }},
		{"output limit", func(s *Settings) { s.Profiles[0].MaxOutputTokens = 384_000 }},
		{"fallback self", func(s *Settings) {
			s.Policy.Everyday = "ds-flash"
			s.Policy.Fallbacks = map[string][]string{"everyday": {"ds-flash"}}
		}},
		{"unknown fallback role", func(s *Settings) { s.Policy.Fallbacks = map[string][]string{"planner": {"ds-pro"}} }},
		{"large classifier", func(s *Settings) { s.Policy.Classifier = Classifier{Enabled: true, Profile: "glm-flash"} }},
		{"unsupported model", func(s *Settings) { s.Profiles[0].Model = "made-up-vision-model" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := candidateSettings()
			tc.edit(&s)
			if len(s.Validate()) == 0 {
				t.Fatal("invalid settings accepted")
			}
		})
	}
}
func TestParseSettingsRejectsUnknownFieldsAndTrailingDocument(t *testing.T) {
	data, _ := json.Marshal(candidateSettings())
	if _, err := Parse(data); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{append(append([]byte{}, data...), []byte(` {}`)...), []byte(`{"schema_version":1,"eligible":true}`), make([]byte, MaxSettingsBytes+1)} {
		if _, err := Parse(bad); err == nil {
			t.Fatal("untrusted document accepted")
		}
	}
}
