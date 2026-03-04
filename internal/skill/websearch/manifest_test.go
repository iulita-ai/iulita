package websearch

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
	if m.Name != "websearch" {
		t.Errorf("name = %q, want %q", m.Name, "websearch")
	}
	if m.Type != skill.Internal {
		t.Errorf("type = %v, want Internal", m.Type)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0] != "web" {
		t.Errorf("capabilities = %v, want [web]", m.Capabilities)
	}
}
