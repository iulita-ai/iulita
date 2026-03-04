package google

import (
	"testing"

	"google.golang.org/api/gmail/v1"
)

func TestParseScopesConfig_Empty(t *testing.T) {
	scopes := ParseScopesConfig("")
	if len(scopes) == 0 {
		t.Fatal("empty string should return default scopes")
	}
	if scopes[0] != gmail.GmailReadonlyScope {
		t.Errorf("expected readonly gmail scope, got %s", scopes[0])
	}
}

func TestParseScopesConfig_Preset(t *testing.T) {
	tests := []struct {
		input    string
		contains string
	}{
		{"readonly", "gmail.readonly"},
		{"readwrite", "gmail.modify"},
		{"READWRITE", "gmail.modify"},
		{"full", "drive"},
	}
	for _, tt := range tests {
		scopes := ParseScopesConfig(tt.input)
		found := false
		for _, s := range scopes {
			if strContains(s, tt.contains) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("preset %q should contain scope with %q, got %v", tt.input, tt.contains, scopes)
		}
	}
}

func TestParseScopesConfig_JSONArray(t *testing.T) {
	input := `["https://mail.google.com/", "https://www.googleapis.com/auth/drive"]`
	scopes := ParseScopesConfig(input)
	if len(scopes) != 2 {
		t.Fatalf("expected 2 scopes, got %d", len(scopes))
	}
	if scopes[0] != "https://mail.google.com/" {
		t.Errorf("unexpected scope: %s", scopes[0])
	}
}

func TestParseScopesConfig_UnknownPreset(t *testing.T) {
	scopes := ParseScopesConfig("nonexistent")
	defaults := DefaultScopes()
	if len(scopes) != len(defaults) {
		t.Fatalf("unknown preset should return defaults, got %d scopes", len(scopes))
	}
}

func TestParseScopesConfig_InvalidJSON(t *testing.T) {
	scopes := ParseScopesConfig("[invalid")
	defaults := DefaultScopes()
	if len(scopes) != len(defaults) {
		t.Fatal("invalid JSON should return defaults")
	}
}

func TestFormatScopesForDisplay(t *testing.T) {
	display := FormatScopesForDisplay(DefaultScopes())
	if display == "" {
		t.Fatal("display should not be empty")
	}
	if !strContains(display, "readonly") {
		t.Errorf("default scopes should format as 'readonly', got %q", display)
	}
}

func TestFormatScopesForDisplay_Custom(t *testing.T) {
	display := FormatScopesForDisplay([]string{"https://mail.google.com/"})
	if !strContains(display, "custom") {
		t.Errorf("non-preset scopes should format as 'custom', got %q", display)
	}
}

func TestScopesEqual(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"c", "a", "b"}
	if !scopesEqual(a, b) {
		t.Error("same scopes in different order should be equal")
	}
	if scopesEqual(a, []string{"a", "b"}) {
		t.Error("different length should not be equal")
	}
}

func strContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
