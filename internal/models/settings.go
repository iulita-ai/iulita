package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxSettingsBytes = 256 << 10
const MaxProfiles = 32
const MaxDeploymentOutputTokens = 32_768

type Profile struct {
	ID              string `json:"id" koanf:"id"`
	Name            string `json:"name" koanf:"name"`
	Connection      string `json:"connection" koanf:"connection"`
	Model           string `json:"model" koanf:"model"`
	MaxOutputTokens int    `json:"max_output_tokens" koanf:"max_output_tokens"`
	Thinking        string `json:"thinking" koanf:"thinking"`
	ReasoningEffort string `json:"reasoning_effort,omitempty" koanf:"reasoning_effort"`
	ClearThinking   bool   `json:"clear_thinking" koanf:"clear_thinking"`
}
type Classifier struct {
	Enabled bool   `json:"enabled" koanf:"enabled"`
	Profile string `json:"profile,omitempty" koanf:"profile"`
}
type Policy struct {
	Everyday    string            `json:"everyday" koanf:"everyday"`
	Complex     string            `json:"complex" koanf:"complex"`
	Vision      string            `json:"vision" koanf:"vision"`
	Background  string            `json:"background" koanf:"background"`
	Classifier  Classifier        `json:"classifier" koanf:"classifier"`
	LegacyHints map[string]string `json:"legacy_hints,omitempty" koanf:"legacy_hints"`
	// LegacyProfileHints preserves custom routes whose target is distinct from
	// all four task roles. It is not inferred from a raw ProfileID-like hint.
	LegacyProfileHints map[string]string   `json:"legacy_profile_hints,omitempty" koanf:"legacy_profile_hints"`
	Fallbacks          map[string][]string `json:"fallbacks,omitempty" koanf:"fallbacks"`
	ForbiddenProviders []string            `json:"forbidden_providers,omitempty" koanf:"forbidden_providers"`
}
type Settings struct {
	SchemaVersion int       `json:"schema_version" koanf:"schema_version"`
	Profiles      []Profile `json:"profiles" koanf:"profiles"`
	Policy        Policy    `json:"policy" koanf:"policy"`
}
type FieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Path + ": " + e.Message }

var validHint = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var validID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func Parse(data []byte) (Settings, error) {
	if len(data) > MaxSettingsBytes {
		return Settings{}, fmt.Errorf("model settings exceed size limit")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Settings
	if err := dec.Decode(&s); err != nil {
		return Settings{}, fmt.Errorf("invalid model settings JSON")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Settings{}, fmt.Errorf("model settings must contain one document")
	}
	if errs := s.Validate(); len(errs) > 0 {
		return Settings{}, errs[0]
	}
	return s, nil
}
func ValidProvider(p string) bool {
	switch p {
	case "claude", "openai", "deepseek", "ollama", "zai":
		return true
	}
	return false
}
func (p Policy) Roles() map[string]string {
	return map[string]string{"everyday": p.Everyday, "complex": p.Complex, "vision": p.Vision, "background": p.Background}
}

// Validate checks the draft locally. Runtime admission additionally requires a
// configured connection and server-owned evidence; a draft cannot grant either.
func (s Settings) Validate() []FieldError {
	var errs []FieldError
	add := func(path, code, msg string) { errs = append(errs, FieldError{path, code, msg}) }
	if s.SchemaVersion != 1 {
		add("schema_version", "unsupported_version", "use schema version 1")
	}
	if len(s.Profiles) > MaxProfiles {
		add("profiles", "too_many_profiles", "at most 32 profiles are allowed")
	}
	byID := make(map[string]Profile)
	for i, p := range s.Profiles {
		path := fmt.Sprintf("profiles[%d]", i)
		if !validID.MatchString(p.ID) {
			add(path+".id", "invalid_id", "use a lowercase stable profile ID")
		}
		if _, ok := byID[p.ID]; ok {
			add(path+".id", "duplicate_id", "profile IDs must be unique")
		}
		byID[p.ID] = p
		if !utf8.ValidString(p.Name) || strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 80 || strings.ContainsAny(p.Name, "\x00\r\n") {
			add(path+".name", "invalid_name", "use a readable name up to 80 characters")
		}
		if !ValidProvider(p.Connection) {
			add(path+".connection", "invalid_connection", "unknown provider")
		}
		if len(p.Model) == 0 || len(p.Model) > 128 || strings.TrimSpace(p.Model) != p.Model || strings.ContainsAny(p.Model, "\x00\r\n") {
			add(path+".model", "invalid_model", "model must be a bounded nonempty identifier")
		}
		if p.MaxOutputTokens < 1 || p.MaxOutputTokens > MaxDeploymentOutputTokens {
			add(path+".max_output_tokens", "invalid_output_limit", "output limit must be between 1 and 32768")
		}
		if p.Thinking != "enabled" && p.Thinking != "disabled" {
			add(path+".thinking", "unsupported_parameter", "thinking must be enabled or disabled")
		}
		d, known := Lookup(p.Connection, p.Model)
		if known && p.MaxOutputTokens > d.MaxOutputTokens {
			add(path+".max_output_tokens", "invalid_output_limit", "output exceeds model capacity")
		}
		if p.Connection == "zai" {
			if !known {
				add(path+".model", "unsupported_model", "model is not in the supported Z.ai catalog")
			}
			if p.Thinking != "enabled" || !p.ClearThinking {
				add(path+".thinking", "unsupported_parameter", "GLM requires enabled thinking and clear_thinking for this adapter")
			}
			switch p.ReasoningEffort {
			case "low", "high", "max":
			default:
				add(path+".reasoning_effort", "unsupported_parameter", "GLM effort must be low, high or max")
			}
		} else if p.Connection == "deepseek" {
			if !known {
				add(path+".model", "unsupported_model", "new DeepSeek profiles require a supported catalog model")
			}
			if p.Thinking == "disabled" && p.ReasoningEffort != "" {
				add(path+".reasoning_effort", "unsupported_parameter", "non-thinking profiles cannot set effort")
			}
			if p.Thinking == "enabled" && p.ReasoningEffort != "high" && p.ReasoningEffort != "max" {
				add(path+".reasoning_effort", "unsupported_parameter", "DeepSeek thinking effort must be high or max")
			}
		} else if p.ReasoningEffort != "" || p.ClearThinking {
			add(path+".reasoning_effort", "unsupported_parameter", "reasoning options are supported only on DeepSeek and Z.ai profiles")
		}
	}
	forbidden := map[string]bool{}
	for _, p := range s.Policy.ForbiddenProviders {
		if !ValidProvider(p) {
			add("policy.forbidden_providers", "invalid_connection", "unknown forbidden provider")
		}
		forbidden[p] = true
	}
	check := func(path, id string, vision bool) {
		if id == "" {
			return
		}
		p, ok := byID[id]
		if !ok {
			add(path, "invalid_reference", "profile does not exist")
			return
		}
		if forbidden[p.Connection] {
			add(path, "forbidden_provider", "profile provider is forbidden")
		}
		if vision {
			d, known := Lookup(p.Connection, p.Model)
			if p.Connection != "claude" && (!known || !d.Images) {
				add(path, "incompatible_capability", "profile cannot accept images")
			}
		}
	}
	for role, id := range s.Policy.Roles() {
		check("policy."+role, id, role == "vision")
	}
	if s.Policy.Classifier.Enabled {
		if s.Policy.Classifier.Profile == "" {
			add("policy.classifier.profile", "invalid_reference", "select a classifier profile")
		}
		check("policy.classifier.profile", s.Policy.Classifier.Profile, false)
		if p, ok := byID[s.Policy.Classifier.Profile]; ok && p.MaxOutputTokens > 1024 {
			add("policy.classifier.profile", "invalid_output_limit", "classifier output limit must be at most 1024")
		}
	}
	for hint, role := range s.Policy.LegacyHints {
		if !validHint.MatchString(hint) {
			add("policy.legacy_hints", "invalid_hint", "invalid legacy hint")
		}
		if _, ok := s.Policy.Roles()[role]; !ok {
			add("policy.legacy_hints", "invalid_reference", "legacy hints must refer to a task role")
		}
	}
	for hint, id := range s.Policy.LegacyProfileHints {
		if !validHint.MatchString(hint) {
			add("policy.legacy_profile_hints", "invalid_hint", "invalid legacy hint")
		}
		if _, exists := s.Policy.LegacyHints[hint]; exists {
			add("policy.legacy_profile_hints", "duplicate_hint", "hint already assigned to a role")
		}
		check("policy.legacy_profile_hints", id, false)
	}
	for role, ids := range s.Policy.Fallbacks {
		if _, ok := s.Policy.Roles()[role]; !ok {
			add("policy.fallbacks", "invalid_reference", "unknown fallback role")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] || id == s.Policy.Roles()[role] {
				add("policy.fallbacks."+role, "fallback_cycle", "duplicate or self-referencing fallback")
			}
			seen[id] = true
			check("policy.fallbacks."+role, id, role == "vision")
		}
	}
	return errs
}
