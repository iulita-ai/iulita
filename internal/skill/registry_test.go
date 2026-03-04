package skill

import (
	"context"
	"encoding/json"
	"testing"
)

// mockSkill implements Skill for testing.
type mockSkill struct {
	name string
}

func (m *mockSkill) Name() string                                                 { return m.name }
func (m *mockSkill) Description() string                                          { return "mock " + m.name }
func (m *mockSkill) InputSchema() json.RawMessage                                 { return json.RawMessage(`{}`) }
func (m *mockSkill) Execute(_ context.Context, _ json.RawMessage) (string, error) { return "", nil }

// mockCapSkill implements Skill + CapabilityAware.
type mockCapSkill struct {
	mockSkill
	caps []string
}

func (m *mockCapSkill) RequiredCapabilities() []string { return m.caps }

// mockReloadableSkill implements Skill + ConfigReloadable.
type mockReloadableSkill struct {
	mockSkill
	lastKey   string
	lastValue string
	callCount int
}

func (m *mockReloadableSkill) OnConfigChanged(key, value string) {
	m.lastKey = key
	m.lastValue = value
	m.callCount++
}

func TestRegistry_AllEnabledByDefault(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockSkill{name: "a"})
	r.Register(&mockSkill{name: "b"})

	enabled := r.EnabledSkills()
	if len(enabled) != 2 {
		t.Fatalf("expected 2 enabled skills, got %d", len(enabled))
	}

	if _, ok := r.Get("a"); !ok {
		t.Error("expected skill 'a' to be available")
	}
	if _, ok := r.Get("b"); !ok {
		t.Error("expected skill 'b' to be available")
	}
}

func TestRegistry_DisableSkill(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockSkill{name: "a"})
	r.Register(&mockSkill{name: "b"})

	r.DisableSkill("a")

	if _, ok := r.Get("a"); ok {
		t.Error("expected skill 'a' to be disabled")
	}
	if _, ok := r.Get("b"); !ok {
		t.Error("expected skill 'b' to be still available")
	}

	enabled := r.EnabledSkills()
	if len(enabled) != 1 {
		t.Fatalf("expected 1 enabled skill, got %d", len(enabled))
	}
}

func TestRegistry_EnableSkill(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockSkill{name: "a"})
	r.DisableSkill("a")

	if _, ok := r.Get("a"); ok {
		t.Error("expected skill 'a' to be disabled")
	}

	r.EnableSkill("a")

	if _, ok := r.Get("a"); !ok {
		t.Error("expected skill 'a' to be re-enabled")
	}
}

func TestRegistry_IsDisabled(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockSkill{name: "a"})

	if r.IsDisabled("a") {
		t.Error("expected 'a' not disabled initially")
	}

	r.DisableSkill("a")
	if !r.IsDisabled("a") {
		t.Error("expected 'a' disabled after DisableSkill")
	}
}

func TestRegistry_RegisterWithManifest(t *testing.T) {
	r := NewRegistry()
	s := &mockSkill{name: "test"}
	m := &Manifest{
		Name:         "test",
		SystemPrompt: "Test system prompt",
		Type:         Internal,
	}

	r.RegisterWithManifest(s, m)

	got, ok := r.Get("test")
	if !ok {
		t.Fatal("expected to get skill")
	}
	if got.Name() != "test" {
		t.Errorf("name = %q, want %q", got.Name(), "test")
	}

	manifest, ok := r.GetManifest("test")
	if !ok {
		t.Fatal("expected to get manifest")
	}
	if manifest.SystemPrompt != "Test system prompt" {
		t.Errorf("prompt = %q", manifest.SystemPrompt)
	}
}

func TestRegistry_RegisterWithManifest_StoredByManifestName(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{Name: "craft", Type: Internal, SystemPrompt: "craft prompt"}
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, m)

	// Manifest should be found by manifest Name, not skill name.
	if _, ok := r.GetManifest("craft"); !ok {
		t.Error("expected manifest 'craft' to exist")
	}

	// Skill-to-manifest mapping should be set.
	if got := r.SkillManifest("craft_search"); got != "craft" {
		t.Errorf("SkillManifest('craft_search') = %q, want 'craft'", got)
	}
}

func TestRegistry_RegisterInGroup(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{Name: "craft", Type: Internal}
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, m)
	r.RegisterInGroup(&mockSkill{name: "craft_read"}, "craft")
	r.RegisterInGroup(&mockSkill{name: "craft_write"}, "craft")

	// All skills should be registered.
	if _, ok := r.Get("craft_search"); !ok {
		t.Error("expected craft_search")
	}
	if _, ok := r.Get("craft_read"); !ok {
		t.Error("expected craft_read")
	}
	if _, ok := r.Get("craft_write"); !ok {
		t.Error("expected craft_write")
	}

	// All should map to same manifest.
	for _, name := range []string{"craft_search", "craft_read", "craft_write"} {
		if got := r.SkillManifest(name); got != "craft" {
			t.Errorf("SkillManifest(%q) = %q, want 'craft'", name, got)
		}
	}

	// GroupSkills should return all three.
	group := r.GroupSkills("craft")
	if len(group) != 3 {
		t.Fatalf("GroupSkills('craft') len = %d, want 3", len(group))
	}
}

func TestRegistry_DisableGroup(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{Name: "craft", Type: Internal}
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, m)
	r.RegisterInGroup(&mockSkill{name: "craft_read"}, "craft")
	r.RegisterInGroup(&mockSkill{name: "craft_write"}, "craft")

	r.DisableGroup("craft")

	for _, name := range []string{"craft_search", "craft_read", "craft_write"} {
		if !r.IsDisabled(name) {
			t.Errorf("expected %q to be disabled after DisableGroup", name)
		}
	}
}

func TestRegistry_EnableGroup(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{Name: "craft", Type: Internal}
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, m)
	r.RegisterInGroup(&mockSkill{name: "craft_read"}, "craft")

	r.DisableGroup("craft")
	r.EnableGroup("craft")

	for _, name := range []string{"craft_search", "craft_read"} {
		if r.IsDisabled(name) {
			t.Errorf("expected %q to be enabled after EnableGroup", name)
		}
	}
}

func TestRegistry_DispatchConfigChanged_GroupToggle(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{Name: "craft", Type: Internal, ConfigKeys: []string{"skills.craft.api_key"}}
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, m)
	r.RegisterInGroup(&mockSkill{name: "craft_read"}, "craft")
	r.RegisterInGroup(&mockSkill{name: "craft_tasks"}, "craft")

	// Disable group via config key.
	r.DispatchConfigChanged("skills.craft.enabled", "false", false)

	for _, name := range []string{"craft_search", "craft_read", "craft_tasks"} {
		if !r.IsDisabled(name) {
			t.Errorf("expected %q disabled after group toggle", name)
		}
	}

	// Re-enable via delete.
	r.DispatchConfigChanged("skills.craft.enabled", "", true)

	for _, name := range []string{"craft_search", "craft_read", "craft_tasks"} {
		if r.IsDisabled(name) {
			t.Errorf("expected %q enabled after group toggle delete", name)
		}
	}
}

func TestRegistry_SystemPrompts_GroupDisabled(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{Name: "craft", Type: Internal, SystemPrompt: "craft rules"}
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, m)
	r.RegisterInGroup(&mockSkill{name: "craft_read"}, "craft")

	// All enabled → prompt included.
	prompts := r.SystemPrompts()
	if len(prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompts))
	}

	// Disable all group members → prompt excluded.
	r.DisableGroup("craft")
	prompts = r.SystemPrompts()
	if len(prompts) != 0 {
		t.Errorf("expected 0 prompts when group disabled, got %d", len(prompts))
	}

	// Enable one member → prompt included again.
	r.EnableSkill("craft_read")
	prompts = r.SystemPrompts()
	if len(prompts) != 1 {
		t.Errorf("expected 1 prompt when one member enabled, got %d", len(prompts))
	}
}

func TestRegistry_AllSkills_ManifestGroup(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{Name: "craft", Type: Internal}
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, m)
	r.RegisterInGroup(&mockSkill{name: "craft_read"}, "craft")
	r.Register(&mockSkill{name: "datetime"})

	all := r.AllSkills()
	if len(all) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(all))
	}

	for _, ss := range all {
		switch ss.Skill.Name() {
		case "craft_search", "craft_read":
			if ss.ManifestGroup != "craft" {
				t.Errorf("%q ManifestGroup = %q, want 'craft'", ss.Skill.Name(), ss.ManifestGroup)
			}
		case "datetime":
			if ss.ManifestGroup != "" {
				t.Errorf("datetime ManifestGroup = %q, want empty", ss.ManifestGroup)
			}
		}
	}
}

func TestRegistry_AddManifest_TextOnly(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{
		Name:         "tone",
		Description:  "Tone of voice",
		SystemPrompt: "Be friendly.",
		Type:         TextOnly,
	}

	r.AddManifest(m)

	prompts := r.SystemPrompts()
	if len(prompts) != 1 {
		t.Fatalf("prompts len = %d, want 1", len(prompts))
	}
	if prompts[0] != "Be friendly." {
		t.Errorf("prompt = %q", prompts[0])
	}
}

func TestRegistry_SystemPrompts_FiltersDisabledInternal(t *testing.T) {
	r := NewRegistry()

	r.RegisterWithManifest(&mockSkill{name: "enabled_skill"}, &Manifest{
		Name:         "enabled_skill",
		SystemPrompt: "enabled prompt",
		Type:         Internal,
	})

	r.RegisterWithManifest(&mockSkill{name: "disabled_skill"}, &Manifest{
		Name:         "disabled_skill",
		SystemPrompt: "disabled prompt",
		Type:         Internal,
	})
	r.DisableSkill("disabled_skill")

	r.AddManifest(&Manifest{
		Name:         "text_skill",
		SystemPrompt: "text prompt",
		Type:         TextOnly,
	})

	prompts := r.SystemPrompts()
	if len(prompts) != 2 {
		t.Fatalf("prompts len = %d, want 2", len(prompts))
	}

	for _, p := range prompts {
		if p == "disabled prompt" {
			t.Error("disabled skill prompt should not be included")
		}
	}
}

func TestRegistry_SystemPrompts_DisabledTextOnly(t *testing.T) {
	r := NewRegistry()

	// TextOnly registered via RegisterWithManifest (marketplace path).
	r.RegisterWithManifest(&mockSkill{name: "weather"}, &Manifest{
		Name:         "weather",
		SystemPrompt: "Use webfetch for weather",
		Type:         TextOnly,
	})
	r.DisableSkill("weather")

	// TextOnly registered via AddManifest (flat-file path).
	r.AddManifest(&Manifest{
		Name:         "disabled-tone",
		SystemPrompt: "Be formal.",
		Type:         TextOnly,
	})
	r.DisableSkill("disabled-tone")

	// Enabled TextOnly (should still appear).
	r.AddManifest(&Manifest{
		Name:         "enabled-tone",
		SystemPrompt: "Be friendly.",
		Type:         TextOnly,
	})

	prompts := r.SystemPrompts()
	if len(prompts) != 1 {
		t.Fatalf("prompts len = %d, want 1 (only enabled-tone)", len(prompts))
	}
	if prompts[0] != "Be friendly." {
		t.Errorf("prompt = %q, want 'Be friendly.'", prompts[0])
	}
}

func TestRegistry_Manifests(t *testing.T) {
	r := NewRegistry()
	r.RegisterWithManifest(&mockSkill{name: "a"}, &Manifest{Name: "a", Type: Internal})
	r.AddManifest(&Manifest{Name: "b", Type: TextOnly})

	manifests := r.Manifests()
	if len(manifests) != 2 {
		t.Fatalf("manifests len = %d, want 2", len(manifests))
	}
}

func TestRegistry_SystemPrompts_EmptyPromptSkipped(t *testing.T) {
	r := NewRegistry()
	r.RegisterWithManifest(&mockSkill{name: "s"}, &Manifest{
		Name:         "s",
		SystemPrompt: "",
		Type:         Internal,
	})

	prompts := r.SystemPrompts()
	if len(prompts) != 0 {
		t.Errorf("prompts len = %d, want 0 (empty prompts should be skipped)", len(prompts))
	}
}

func TestRegistry_ConfigKeys(t *testing.T) {
	r := NewRegistry()
	r.RegisterWithManifest(&mockSkill{name: "a"}, &Manifest{
		Name:       "a",
		Type:       Internal,
		ConfigKeys: []string{"skills.a.key1", "skills.a.key2"},
	})
	r.RegisterWithManifest(&mockSkill{name: "b"}, &Manifest{
		Name:       "b",
		Type:       Internal,
		ConfigKeys: []string{"skills.b.key1"},
	})

	keys := r.ConfigKeys()

	keySet := make(map[string]bool)
	for _, k := range keys {
		keySet[k] = true
	}
	for _, want := range []string{"skills.a.key1", "skills.a.key2", "skills.b.key1"} {
		if !keySet[want] {
			t.Errorf("missing key %q in ConfigKeys()", want)
		}
	}
	for _, want := range []string{"skills.a.enabled", "skills.b.enabled"} {
		if !keySet[want] {
			t.Errorf("missing auto-generated key %q in ConfigKeys()", want)
		}
	}
}

func TestRegistry_ConfigKeys_GroupEnabled(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{Name: "craft", Type: Internal, ConfigKeys: []string{"skills.craft.api_key"}}
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, m)
	r.RegisterInGroup(&mockSkill{name: "craft_read"}, "craft")

	keys := r.ConfigKeys()
	keySet := make(map[string]bool)
	for _, k := range keys {
		keySet[k] = true
	}
	// Should have manifest-level enabled key.
	if !keySet["skills.craft.enabled"] {
		t.Error("missing skills.craft.enabled (manifest group key)")
	}
	// Should also have individual skill enabled keys.
	if !keySet["skills.craft_search.enabled"] {
		t.Error("missing skills.craft_search.enabled")
	}
	if !keySet["skills.craft_read.enabled"] {
		t.Error("missing skills.craft_read.enabled")
	}
}

func TestRegistry_ConfigKeys_Empty(t *testing.T) {
	r := NewRegistry()
	r.RegisterWithManifest(&mockSkill{name: "a"}, &Manifest{
		Name: "a",
		Type: Internal,
	})

	keys := r.ConfigKeys()
	// skills.a.enabled for skill + manifest (same name, deduplicated) = 1
	if len(keys) != 1 {
		t.Errorf("ConfigKeys() len = %d, want 1 (deduplicated skills.a.enabled)", len(keys))
	}
}

func TestRegistry_DispatchConfigChanged_SystemPrompt(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{
		Name:         "craft",
		Type:         Internal,
		SystemPrompt: "original",
		ConfigKeys:   []string{"skills.craft.system_prompt", "skills.craft.secret_link_id"},
	}
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, m)

	r.DispatchConfigChanged("skills.craft.system_prompt", "updated prompt", false)

	if m.SystemPrompt != "updated prompt" {
		t.Errorf("SystemPrompt = %q, want %q", m.SystemPrompt, "updated prompt")
	}
}

func TestRegistry_DispatchConfigChanged_SkillReceivesNotification(t *testing.T) {
	r := NewRegistry()
	s := &mockReloadableSkill{mockSkill: mockSkill{name: "craft_search"}}
	m := &Manifest{
		Name:       "craft",
		Type:       Internal,
		ConfigKeys: []string{"skills.craft.threshold"},
	}
	r.RegisterWithManifest(s, m)

	r.DispatchConfigChanged("skills.craft.threshold", "42", false)

	if s.callCount != 1 {
		t.Errorf("callCount = %d, want 1", s.callCount)
	}
	if s.lastKey != "skills.craft.threshold" {
		t.Errorf("lastKey = %q", s.lastKey)
	}
	if s.lastValue != "42" {
		t.Errorf("lastValue = %q", s.lastValue)
	}
}

func TestRegistry_DispatchConfigChanged_UnknownKey(t *testing.T) {
	r := NewRegistry()
	s := &mockReloadableSkill{mockSkill: mockSkill{name: "a"}}
	r.RegisterWithManifest(s, &Manifest{
		Name:       "a",
		Type:       Internal,
		ConfigKeys: []string{"skills.a.key1"},
	})

	r.DispatchConfigChanged("skills.unknown.key", "val", false)

	if s.callCount != 0 {
		t.Errorf("callCount = %d, want 0 (key doesn't match)", s.callCount)
	}
}

func TestRegistry_DispatchConfigChanged_SystemPromptNotUpdatedOnDelete(t *testing.T) {
	r := NewRegistry()
	m := &Manifest{
		Name:         "craft",
		Type:         Internal,
		SystemPrompt: "original",
		ConfigKeys:   []string{"skills.craft.system_prompt"},
	}
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, m)

	r.DispatchConfigChanged("skills.craft.system_prompt", "", true)

	if m.SystemPrompt != "original" {
		t.Errorf("SystemPrompt = %q, want %q (should not change on delete)", m.SystemPrompt, "original")
	}
}

func TestRegistry_DispatchConfigChanged_EnabledKey(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockSkill{name: "recall"})

	r.DispatchConfigChanged("skills.recall.enabled", "false", false)
	if !r.IsDisabled("recall") {
		t.Error("expected recall to be disabled after setting enabled=false")
	}

	r.DispatchConfigChanged("skills.recall.enabled", "true", false)
	if r.IsDisabled("recall") {
		t.Error("expected recall to be re-enabled after setting enabled=true")
	}

	r.DispatchConfigChanged("skills.recall.enabled", "false", false)
	r.DispatchConfigChanged("skills.recall.enabled", "", true) // deleted
	if r.IsDisabled("recall") {
		t.Error("expected recall to be re-enabled after deleting override")
	}
}

func TestRegistry_AllSkills(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockSkill{name: "a"})
	r.Register(&mockCapSkill{mockSkill: mockSkill{name: "b"}, caps: []string{"web"}})
	r.DisableSkill("a")

	all := r.AllSkills()
	if len(all) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(all))
	}

	for _, ss := range all {
		switch ss.Skill.Name() {
		case "a":
			if ss.Enabled {
				t.Error("expected 'a' to be disabled")
			}
			if !ss.HasCapabilities {
				t.Error("expected 'a' to have capabilities (no requirements)")
			}
		case "b":
			if !ss.Enabled {
				t.Error("expected 'b' to be enabled")
			}
			if ss.HasCapabilities {
				t.Error("expected 'b' to lack capabilities (web not set)")
			}
		}
	}
}

func TestRegistry_SecretKeys(t *testing.T) {
	r := NewRegistry()
	r.RegisterWithManifest(&mockSkill{name: "a"}, &Manifest{
		Name:       "a",
		Type:       Internal,
		ConfigKeys: []string{"skills.a.url", "skills.a.api_key"},
		SecretKeys: []string{"skills.a.api_key"},
	})
	r.RegisterWithManifest(&mockSkill{name: "b"}, &Manifest{
		Name:       "b",
		Type:       Internal,
		ConfigKeys: []string{"skills.b.token"},
		SecretKeys: []string{"skills.b.token"},
	})

	secrets := r.SecretKeys()
	if len(secrets) != 2 {
		t.Fatalf("SecretKeys() len = %d, want 2", len(secrets))
	}
	if !secrets["skills.a.api_key"] {
		t.Error("missing skills.a.api_key in SecretKeys()")
	}
	if !secrets["skills.b.token"] {
		t.Error("missing skills.b.token in SecretKeys()")
	}
	if secrets["skills.a.url"] {
		t.Error("skills.a.url should not be in SecretKeys()")
	}
}

// mockConfigGetter implements ConfigValueGetter for testing.
type mockConfigGetter struct {
	values    map[string]string
	overrides map[string]bool
	secrets   map[string]bool
}

func (m *mockConfigGetter) GetEffective(key string) (string, bool) {
	v, ok := m.values[key]
	return v, ok
}
func (m *mockConfigGetter) HasOverride(key string) bool {
	return m.overrides[key]
}
func (m *mockConfigGetter) IsSecretKey(key string) bool {
	return m.secrets[key]
}

func TestRegistry_GetSkillConfigSchema(t *testing.T) {
	r := NewRegistry()
	r.RegisterWithManifest(&mockSkill{name: "craft_search"}, &Manifest{
		Name:       "craft",
		Type:       Internal,
		ConfigKeys: []string{"skills.craft.api_url", "skills.craft.api_key", "skills.craft.system_prompt"},
		SecretKeys: []string{"skills.craft.api_key"},
	})

	getter := &mockConfigGetter{
		values:    map[string]string{"skills.craft.api_url": "https://example.com", "skills.craft.api_key": "secret123"},
		overrides: map[string]bool{"skills.craft.api_url": true, "skills.craft.api_key": true},
		secrets:   map[string]bool{"skills.craft.api_key": true},
	}

	fields, ok := r.GetSkillConfigSchema("craft", getter)
	if !ok {
		t.Fatal("expected schema for craft")
	}
	if len(fields) != 3 {
		t.Fatalf("fields len = %d, want 3", len(fields))
	}

	urlField := fields[0]
	if urlField.Key != "skills.craft.api_url" {
		t.Errorf("field[0].Key = %q", urlField.Key)
	}
	if urlField.Secret {
		t.Error("api_url should not be secret")
	}
	if urlField.Value != "https://example.com" {
		t.Errorf("api_url value = %q", urlField.Value)
	}
	if !urlField.HasOverride {
		t.Error("api_url should have override")
	}

	keyField := fields[1]
	if keyField.Key != "skills.craft.api_key" {
		t.Errorf("field[1].Key = %q", keyField.Key)
	}
	if !keyField.Secret {
		t.Error("api_key should be secret")
	}
	if keyField.Value != "" {
		t.Errorf("secret field should not have value, got %q", keyField.Value)
	}
	if !keyField.HasValue {
		t.Error("api_key should have has_value=true (override exists)")
	}
}

func TestRegistry_GetSkillConfigSchema_NotFound(t *testing.T) {
	r := NewRegistry()
	_, ok := r.GetSkillConfigSchema("nonexistent", nil)
	if ok {
		t.Error("expected not found")
	}
}

func TestRegistry_CapabilityGating(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockCapSkill{mockSkill: mockSkill{name: "websearch"}, caps: []string{"web"}})

	if _, ok := r.Get("websearch"); ok {
		t.Error("expected websearch to be gated without web capability")
	}

	r.SetCapabilities([]string{"web"})
	if _, ok := r.Get("websearch"); !ok {
		t.Error("expected websearch to be available with web capability")
	}
}

func TestRegistry_AddCapability(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockCapSkill{mockSkill: mockSkill{name: "craft_search"}, caps: []string{"craft"}})

	if _, ok := r.Get("craft_search"); ok {
		t.Error("expected craft_search gated without craft capability")
	}

	r.AddCapability("craft")

	if _, ok := r.Get("craft_search"); !ok {
		t.Error("expected craft_search available after AddCapability")
	}
}

func TestRegistry_ConfigHandler_DispatchToFirstRegistered(t *testing.T) {
	r := NewRegistry()
	handler := &mockReloadableSkill{mockSkill: mockSkill{name: "craft_search"}}
	m := &Manifest{Name: "craft", Type: Internal, ConfigKeys: []string{"skills.craft.api_key"}}
	r.RegisterWithManifest(handler, m)
	r.RegisterInGroup(&mockSkill{name: "craft_read"}, "craft")

	r.DispatchConfigChanged("skills.craft.api_key", "new-key", false)

	if handler.callCount != 1 {
		t.Errorf("expected config handler (craft_search) to be called, callCount = %d", handler.callCount)
	}
}
