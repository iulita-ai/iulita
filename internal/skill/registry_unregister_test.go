package skill

import (
	"testing"
)

func TestUnregisterSkill(t *testing.T) {
	r := NewRegistry()

	s := &dummySkill{name: "test-skill"}
	m := &Manifest{Name: "test-skill", Description: "Test"}

	r.RegisterExternalWithManifest(s, m)

	// Should be accessible.
	if _, ok := r.Get("test-skill"); !ok {
		t.Fatal("skill should be registered")
	}
	if _, ok := r.GetManifest("test-skill"); !ok {
		t.Fatal("manifest should be registered")
	}

	// Unregister.
	r.UnregisterSkill("test-skill")

	if _, ok := r.Get("test-skill"); ok {
		t.Error("skill should be unregistered")
	}
	if _, ok := r.GetManifest("test-skill"); ok {
		t.Error("manifest should be removed (no more references)")
	}
}

func TestUnregisterSkillKeepsSharedManifest(t *testing.T) {
	r := NewRegistry()

	m := &Manifest{Name: "group", Description: "Group manifest"}
	s1 := &dummySkill{name: "skill-a"}
	s2 := &dummySkill{name: "skill-b"}

	r.RegisterExternalWithManifest(s1, m)
	r.RegisterInGroup(s2, "group")

	// Unregister one — manifest should remain.
	r.UnregisterSkill("skill-a")

	if _, ok := r.Get("skill-a"); ok {
		t.Error("skill-a should be unregistered")
	}
	if _, ok := r.Get("skill-b"); !ok {
		t.Error("skill-b should still be registered")
	}
	// Manifest should still exist (skill-b references it).
	if _, ok := r.GetManifest("group"); !ok {
		t.Error("shared manifest should not be removed while references exist")
	}
}

func TestUnregisterNonexistent(t *testing.T) {
	r := NewRegistry()
	// Should not panic.
	r.UnregisterSkill("nonexistent")
}

func TestUnregisterBuiltinProtected(t *testing.T) {
	r := NewRegistry()

	// Register as built-in.
	s := &dummySkill{name: "weather"}
	m := &Manifest{Name: "weather", Description: "Built-in weather"}
	r.RegisterWithManifest(s, m)

	// Try to unregister — should be protected.
	r.UnregisterSkill("weather")

	if _, ok := r.Get("weather"); !ok {
		t.Error("built-in skill should NOT be unregistered")
	}
}

func TestExternalCannotOverwriteBuiltin(t *testing.T) {
	r := NewRegistry()

	// Register built-in.
	builtin := &dummySkill{name: "weather"}
	m := &Manifest{Name: "weather", Description: "Built-in"}
	r.RegisterWithManifest(builtin, m)

	// Try to register external with same name.
	external := &dummySkill{name: "weather"}
	extM := &Manifest{Name: "weather", Description: "External"}
	r.RegisterExternalWithManifest(external, extM)

	// Built-in should still be there (external silently skipped).
	if _, ok := r.Get("weather"); !ok {
		t.Error("built-in skill should still be registered")
	}
}
