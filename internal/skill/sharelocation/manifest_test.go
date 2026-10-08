package sharelocation

import "testing"

func TestManifestFrontmatter(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m == nil {
		t.Fatal("manifest is nil")
	}
	if m.Name != "share_location" {
		t.Fatalf("manifest name = %q", m.Name)
	}
	// Guards D9/D18/D28: no force triggers, no capability gate, no config keys.
	if len(m.ForceTriggers) != 0 {
		t.Fatalf("share_location must declare no force triggers: %v", m.ForceTriggers)
	}
	if len(m.ConfigKeys) != 0 {
		t.Fatalf("share_location must declare no config keys: %v", m.ConfigKeys)
	}
	if len(m.SecretKeys) != 0 {
		t.Fatalf("share_location must declare no secret keys: %v", m.SecretKeys)
	}
	if len(m.Capabilities) != 0 {
		t.Fatalf("share_location must declare no capabilities: %v", m.Capabilities)
	}
}
