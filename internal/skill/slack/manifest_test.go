package slack

import "testing"

func TestLoadManifest(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Name != "slack_search" {
		t.Errorf("name = %q, want slack_search", m.Name)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0] != "slack_user" {
		t.Errorf("capabilities = %v", m.Capabilities)
	}
	if len(m.SecretKeys) == 0 {
		t.Error("expected secret keys for client id/secret")
	}
	if m.SystemPrompt == "" {
		t.Error("expected a system prompt body")
	}
}
