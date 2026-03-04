package shellexec

import (
	"testing"

	"github.com/iulita-ai/iulita/internal/skill"
)

func TestLoadManifest(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected manifest, got nil")
	}
	if m.Name != "shell_exec" {
		t.Errorf("name = %q, want %q", m.Name, "shell_exec")
	}
	if m.Description == "" {
		t.Error("description should not be empty")
	}
	if m.Type != skill.Internal {
		t.Errorf("type = %v, want Internal", m.Type)
	}
	if m.SystemPrompt == "" {
		t.Error("system prompt should not be empty")
	}
}
