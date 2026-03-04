package skillinfo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/skillinfo"
)

// --- Mock skill ---

type mockSkill struct {
	name string
	desc string
	caps []string
}

func (m *mockSkill) Name() string                                                 { return m.name }
func (m *mockSkill) Description() string                                          { return m.desc }
func (m *mockSkill) InputSchema() json.RawMessage                                 { return json.RawMessage(`{}`) }
func (m *mockSkill) Execute(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil }
func (m *mockSkill) RequiredCapabilities() []string                               { return m.caps }

// --- Mock config store ---

type mockConfigStore struct {
	data      map[string]string
	overrides map[string]bool
	secrets   map[string]bool
	setErr    error
	deleteErr error
	setCalls  []setCall
	deletions []string
}

type setCall struct {
	key, value, updatedBy string
	encrypt               bool
}

func newMockConfigStore() *mockConfigStore {
	return &mockConfigStore{
		data:      make(map[string]string),
		overrides: make(map[string]bool),
		secrets:   make(map[string]bool),
	}
}

func (m *mockConfigStore) GetEffective(key string) (string, bool) {
	v, ok := m.data[key]
	return v, ok
}

func (m *mockConfigStore) HasOverride(key string) bool {
	return m.overrides[key]
}

func (m *mockConfigStore) IsSecretKey(key string) bool {
	return m.secrets[key]
}

func (m *mockConfigStore) Set(_ context.Context, key, value, updatedBy string, encrypt bool) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.setCalls = append(m.setCalls, setCall{key, value, updatedBy, encrypt})
	m.data[key] = value
	m.overrides[key] = true
	return nil
}

func (m *mockConfigStore) Delete(_ context.Context, key string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletions = append(m.deletions, key)
	delete(m.data, key)
	delete(m.overrides, key)
	return nil
}

// --- Helpers ---

func adminCtx() context.Context {
	ctx := context.Background()
	ctx = skill.WithUserID(ctx, "user-1")
	ctx = skill.WithUserRole(ctx, "admin")
	return ctx
}

func userCtx() context.Context {
	ctx := context.Background()
	ctx = skill.WithUserID(ctx, "user-2")
	ctx = skill.WithUserRole(ctx, "user")
	return ctx
}

func execute(t *testing.T, s *skillinfo.Skill, ctx context.Context, in map[string]string) string {
	t.Helper()
	raw, _ := json.Marshal(in)
	result, err := s.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return result
}

// --- Tests ---

func TestList_BackwardCompatible(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&mockSkill{name: "datetime", desc: "Get current time"})

	s := skillinfo.New(reg, nil)
	// Empty input = list action (backward compatible).
	result, err := s.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "datetime") {
		t.Fatalf("expected datetime in list, got: %s", result)
	}
}

func TestList_ShowsAllSkillsWithStatus(t *testing.T) {
	reg := skill.NewRegistry()
	reg.SetCapabilities([]string{"memory"})

	m := &skill.Manifest{Name: "memory", Description: "Memory", Type: skill.Internal, ConfigKeys: []string{"skills.memory.triggers"}}
	reg.RegisterWithManifest(&mockSkill{name: "remember", desc: "Remember facts", caps: []string{"memory"}}, m)
	reg.RegisterInGroup(&mockSkill{name: "recall", desc: "Recall facts", caps: []string{"memory"}}, "memory")
	reg.Register(&mockSkill{name: "datetime", desc: "Get time"})

	// Disable recall.
	reg.DisableSkill("recall")

	s := skillinfo.New(reg, nil)
	result := execute(t, s, context.Background(), nil)

	if !strings.Contains(result, "remember") {
		t.Error("expected remember in output")
	}
	if !strings.Contains(result, "recall") {
		t.Error("expected recall in output (even disabled)")
	}
	if !strings.Contains(result, "[DISABLED]") {
		t.Error("expected [DISABLED] status for recall")
	}
	if !strings.Contains(result, "[enabled]") {
		t.Error("expected [enabled] status for remember")
	}
	if !strings.Contains(result, "datetime") {
		t.Error("expected datetime as standalone")
	}
	if !strings.Contains(result, "Skill Groups:") {
		t.Error("expected Skill Groups header")
	}
	if !strings.Contains(result, "[memory]") {
		t.Error("expected [memory] group header")
	}
	if !strings.Contains(result, "Standalone Skills:") {
		t.Error("expected Standalone Skills header")
	}
}

func TestList_ShowsNoCredentials(t *testing.T) {
	reg := skill.NewRegistry()
	// Don't set "craft" capability.
	m := &skill.Manifest{Name: "craft", Description: "Craft", Type: skill.Internal}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search Craft", caps: []string{"craft"}}, m)

	s := skillinfo.New(reg, nil)
	result := execute(t, s, context.Background(), nil)

	if !strings.Contains(result, "[NO CREDENTIALS]") {
		t.Fatalf("expected [NO CREDENTIALS] in output, got: %s", result)
	}
}

func TestList_ShowsConfigurable(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{Name: "craft", Description: "Craft", Type: skill.Internal, ConfigKeys: []string{"skills.craft.api_key"}}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)

	s := skillinfo.New(reg, nil)
	result := execute(t, s, context.Background(), nil)

	if !strings.Contains(result, "(configurable)") {
		t.Fatalf("expected (configurable) marker, got: %s", result)
	}
}

func TestList_TextOnlySkills(t *testing.T) {
	reg := skill.NewRegistry()
	reg.AddManifest(&skill.Manifest{Name: "greetings", Description: "Greeting rules", Type: skill.TextOnly, SystemPrompt: "..."})

	s := skillinfo.New(reg, nil)
	result := execute(t, s, context.Background(), nil)

	if !strings.Contains(result, "Text Skills") {
		t.Error("expected Text Skills section")
	}
	if !strings.Contains(result, "greetings") {
		t.Error("expected greetings text skill")
	}
}

func TestList_EmptyRegistry(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, nil)
	result := execute(t, s, context.Background(), nil)
	if result != "No skills registered." {
		t.Fatalf("expected empty message, got: %s", result)
	}
}

func TestEnable_RequiresAdmin(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&mockSkill{name: "test", desc: "Test"})
	s := skillinfo.New(reg, nil)

	result := execute(t, s, userCtx(), map[string]string{"action": "enable", "skill_name": "test"})
	if !strings.Contains(result, "admin privileges") {
		t.Fatalf("expected admin error, got: %s", result)
	}
}

func TestEnable_IndividualSkill(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&mockSkill{name: "test", desc: "Test"})
	reg.DisableSkill("test")
	store := newMockConfigStore()

	s := skillinfo.New(reg, store)
	result := execute(t, s, adminCtx(), map[string]string{"action": "enable", "skill_name": "test"})

	if !strings.Contains(result, "Enabled skill \"test\"") {
		t.Fatalf("unexpected result: %s", result)
	}
	if reg.IsDisabled("test") {
		t.Error("test should be enabled")
	}
	if len(store.deletions) != 1 || store.deletions[0] != "skills.test.enabled" {
		t.Errorf("expected config deletion, got: %v", store.deletions)
	}
}

func TestEnable_Group(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{Name: "memory", Type: skill.Internal}
	reg.RegisterWithManifest(&mockSkill{name: "remember", desc: "Remember"}, m)
	reg.RegisterInGroup(&mockSkill{name: "recall", desc: "Recall"}, "memory")
	reg.DisableGroup("memory")
	store := newMockConfigStore()

	s := skillinfo.New(reg, store)
	result := execute(t, s, adminCtx(), map[string]string{"action": "enable", "skill_name": "memory"})

	if !strings.Contains(result, "Enabled skill group \"memory\"") {
		t.Fatalf("unexpected result: %s", result)
	}
	if reg.IsDisabled("remember") || reg.IsDisabled("recall") {
		t.Error("group skills should be enabled")
	}
}

func TestEnable_NotFound(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, nil)

	result := execute(t, s, adminCtx(), map[string]string{"action": "enable", "skill_name": "nonexistent"})
	if !strings.Contains(result, "not found") {
		t.Fatalf("expected not found, got: %s", result)
	}
}

func TestEnable_MissingName(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, nil)

	result := execute(t, s, adminCtx(), map[string]string{"action": "enable"})
	if !strings.Contains(result, "skill_name is required") {
		t.Fatalf("expected required error, got: %s", result)
	}
}

func TestDisable_RequiresAdmin(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&mockSkill{name: "test", desc: "Test"})
	s := skillinfo.New(reg, nil)

	result := execute(t, s, userCtx(), map[string]string{"action": "disable", "skill_name": "test"})
	if !strings.Contains(result, "admin privileges") {
		t.Fatalf("expected admin error, got: %s", result)
	}
}

func TestDisable_IndividualSkill(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&mockSkill{name: "test", desc: "Test"})
	store := newMockConfigStore()

	s := skillinfo.New(reg, store)
	result := execute(t, s, adminCtx(), map[string]string{"action": "disable", "skill_name": "test"})

	if !strings.Contains(result, "Disabled skill \"test\"") {
		t.Fatalf("unexpected result: %s", result)
	}
	if !reg.IsDisabled("test") {
		t.Error("test should be disabled")
	}
	if len(store.setCalls) != 1 || store.setCalls[0].key != "skills.test.enabled" || store.setCalls[0].value != "false" {
		t.Errorf("expected config set call, got: %v", store.setCalls)
	}
}

func TestDisable_Group(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{Name: "craft", Type: skill.Internal}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)
	reg.RegisterInGroup(&mockSkill{name: "craft_read", desc: "Read"}, "craft")
	store := newMockConfigStore()

	s := skillinfo.New(reg, store)
	result := execute(t, s, adminCtx(), map[string]string{"action": "disable", "skill_name": "craft"})

	if !strings.Contains(result, "Disabled skill group \"craft\"") {
		t.Fatalf("unexpected result: %s", result)
	}
	if !reg.IsDisabled("craft_search") || !reg.IsDisabled("craft_read") {
		t.Error("group skills should be disabled")
	}
}

func TestDisable_NotFound(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, nil)

	result := execute(t, s, adminCtx(), map[string]string{"action": "disable", "skill_name": "nonexistent"})
	if !strings.Contains(result, "not found") {
		t.Fatalf("expected not found, got: %s", result)
	}
}

func TestGetConfig_RequiresAdmin(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, newMockConfigStore())

	result := execute(t, s, userCtx(), map[string]string{"action": "get_config", "skill_name": "craft"})
	if !strings.Contains(result, "admin privileges") {
		t.Fatalf("expected admin error, got: %s", result)
	}
}

func TestGetConfig_ShowsValues(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{
		Name:       "craft",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.craft.api_url", "skills.craft.api_key"},
		SecretKeys: []string{"skills.craft.api_key"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)

	store := newMockConfigStore()
	store.data["skills.craft.api_url"] = "https://craft.example.com"
	store.data["skills.craft.api_key"] = "secret-key-123"
	store.secrets["skills.craft.api_key"] = true

	s := skillinfo.New(reg, store)
	result := execute(t, s, adminCtx(), map[string]string{"action": "get_config", "skill_name": "craft"})

	if !strings.Contains(result, "skills.craft.api_url: https://craft.example.com") {
		t.Errorf("expected api_url value in output, got: %s", result)
	}
	// Secret should be masked.
	if strings.Contains(result, "secret-key-123") {
		t.Error("secret value should NOT be visible in output")
	}
	if !strings.Contains(result, "skills.craft.api_key: *** [set]") {
		t.Errorf("expected masked secret with [set], got: %s", result)
	}
}

func TestGetConfig_SecretNotSet(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{
		Name:       "craft",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.craft.api_key"},
		SecretKeys: []string{"skills.craft.api_key"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)

	store := newMockConfigStore()
	store.secrets["skills.craft.api_key"] = true
	// No value set.

	s := skillinfo.New(reg, store)
	result := execute(t, s, adminCtx(), map[string]string{"action": "get_config", "skill_name": "craft"})

	if !strings.Contains(result, "[not set]") {
		t.Fatalf("expected [not set] for empty secret, got: %s", result)
	}
}

func TestGetConfig_BySkillName(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{
		Name:       "craft",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.craft.api_url"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)
	reg.RegisterInGroup(&mockSkill{name: "craft_read", desc: "Read"}, "craft")

	store := newMockConfigStore()
	store.data["skills.craft.api_url"] = "https://example.com"

	s := skillinfo.New(reg, store)
	// Use skill name, not manifest name — should resolve.
	result := execute(t, s, adminCtx(), map[string]string{"action": "get_config", "skill_name": "craft_read"})

	if !strings.Contains(result, "skills.craft.api_url") {
		t.Fatalf("expected config for craft manifest via craft_read skill, got: %s", result)
	}
}

func TestGetConfig_NotFound(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, newMockConfigStore())

	result := execute(t, s, adminCtx(), map[string]string{"action": "get_config", "skill_name": "nonexistent"})
	if !strings.Contains(result, "No configuration found") {
		t.Fatalf("expected not found, got: %s", result)
	}
}

func TestGetConfig_NoConfigStore(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, nil)

	result := execute(t, s, adminCtx(), map[string]string{"action": "get_config", "skill_name": "craft"})
	if !strings.Contains(result, "Config store not available") {
		t.Fatalf("expected unavailable, got: %s", result)
	}
}

func TestGetConfig_ShowsOverrideMarker(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{
		Name:       "websearch",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.web.api_key"},
		SecretKeys: []string{"skills.web.api_key"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "websearch", desc: "Web search"}, m)

	store := newMockConfigStore()
	store.data["skills.web.api_key"] = "key"
	store.secrets["skills.web.api_key"] = true
	store.overrides["skills.web.api_key"] = true

	s := skillinfo.New(reg, store)
	result := execute(t, s, adminCtx(), map[string]string{"action": "get_config", "skill_name": "websearch"})

	if !strings.Contains(result, "(override)") {
		t.Fatalf("expected (override) marker, got: %s", result)
	}
}

func TestSetConfig_RequiresAdmin(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, newMockConfigStore())

	result := execute(t, s, userCtx(), map[string]string{
		"action": "set_config", "skill_name": "craft",
		"config_key": "skills.craft.api_url", "config_value": "https://new.url",
	})
	if !strings.Contains(result, "admin privileges") {
		t.Fatalf("expected admin error, got: %s", result)
	}
}

func TestSetConfig_Success(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{
		Name:       "craft",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.craft.api_url", "skills.craft.api_key"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)

	store := newMockConfigStore()
	s := skillinfo.New(reg, store)

	result := execute(t, s, adminCtx(), map[string]string{
		"action": "set_config", "skill_name": "craft",
		"config_key": "skills.craft.api_url", "config_value": "https://new.url",
	})

	if !strings.Contains(result, `Updated "skills.craft.api_url" = "https://new.url"`) {
		t.Fatalf("unexpected result: %s", result)
	}
	if len(store.setCalls) != 1 {
		t.Fatalf("expected 1 set call, got %d", len(store.setCalls))
	}
	if store.setCalls[0].value != "https://new.url" {
		t.Errorf("wrong value: %s", store.setCalls[0].value)
	}
}

func TestSetConfig_SecretNotEchoed(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{
		Name:       "craft",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.craft.api_key"},
		SecretKeys: []string{"skills.craft.api_key"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)

	store := newMockConfigStore()
	store.secrets["skills.craft.api_key"] = true
	s := skillinfo.New(reg, store)

	result := execute(t, s, adminCtx(), map[string]string{
		"action": "set_config", "skill_name": "craft",
		"config_key": "skills.craft.api_key", "config_value": "my-secret-key",
	})

	if strings.Contains(result, "my-secret-key") {
		t.Error("secret value should NOT appear in response")
	}
	if !strings.Contains(result, "secret value not displayed") {
		t.Fatalf("expected secret confirmation, got: %s", result)
	}
	// Verify encrypt=true was passed.
	if len(store.setCalls) != 1 || !store.setCalls[0].encrypt {
		t.Error("expected encrypt=true for secret key")
	}
}

func TestSetConfig_InvalidKey(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{
		Name:       "craft",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.craft.api_url"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)

	store := newMockConfigStore()
	s := skillinfo.New(reg, store)

	result := execute(t, s, adminCtx(), map[string]string{
		"action": "set_config", "skill_name": "craft",
		"config_key": "skills.craft.nonexistent", "config_value": "val",
	})

	if !strings.Contains(result, "not a valid config key") {
		t.Fatalf("expected invalid key error, got: %s", result)
	}
}

func TestSetConfig_EmptyValueDeletesOverride(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{
		Name:       "craft",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.craft.api_url"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)

	store := newMockConfigStore()
	store.data["skills.craft.api_url"] = "old-value"
	s := skillinfo.New(reg, store)

	result := execute(t, s, adminCtx(), map[string]string{
		"action": "set_config", "skill_name": "craft",
		"config_key": "skills.craft.api_url", "config_value": "",
	})

	if !strings.Contains(result, "Removed override") {
		t.Fatalf("expected removal message, got: %s", result)
	}
	if len(store.deletions) != 1 || store.deletions[0] != "skills.craft.api_url" {
		t.Errorf("expected deletion, got: %v", store.deletions)
	}
}

func TestSetConfig_MissingFields(t *testing.T) {
	reg := skill.NewRegistry()
	store := newMockConfigStore()
	s := skillinfo.New(reg, store)

	tests := []struct {
		name   string
		input  map[string]string
		expect string
	}{
		{"missing skill_name", map[string]string{"action": "set_config", "config_key": "k", "config_value": "v"}, "skill_name is required"},
		{"missing config_key", map[string]string{"action": "set_config", "skill_name": "craft"}, "config_key is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := execute(t, s, adminCtx(), tt.input)
			if !strings.Contains(result, tt.expect) {
				t.Fatalf("expected %q, got: %s", tt.expect, result)
			}
		})
	}
}

func TestSetConfig_StoreError(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{
		Name:       "craft",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.craft.api_url"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)

	store := newMockConfigStore()
	store.setErr = fmt.Errorf("database error")
	s := skillinfo.New(reg, store)

	result := execute(t, s, adminCtx(), map[string]string{
		"action": "set_config", "skill_name": "craft",
		"config_key": "skills.craft.api_url", "config_value": "val",
	})

	if !strings.Contains(result, "Failed to set") {
		t.Fatalf("expected failure message, got: %s", result)
	}
}

func TestDisable_CannotDisableSelf(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, nil)
	// Register the skill itself so it exists.
	reg.Register(s)

	result := execute(t, s, adminCtx(), map[string]string{"action": "disable", "skill_name": "skills"})
	if !strings.Contains(result, "Cannot disable") {
		t.Fatalf("expected self-disable guard, got: %s", result)
	}
}

func TestDisable_PersistenceError(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&mockSkill{name: "test", desc: "Test"})

	store := newMockConfigStore()
	store.setErr = fmt.Errorf("db write failed")
	s := skillinfo.New(reg, store)

	result := execute(t, s, adminCtx(), map[string]string{"action": "disable", "skill_name": "test"})
	if !strings.Contains(result, "Warning") || !strings.Contains(result, "revert on restart") {
		t.Fatalf("expected persistence warning, got: %s", result)
	}
	// Skill should still be disabled in memory.
	if !reg.IsDisabled("test") {
		t.Error("skill should be disabled in memory despite persistence failure")
	}
}

func TestUnknownAction(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, nil)

	result := execute(t, s, adminCtx(), map[string]string{"action": "reset"})
	if !strings.Contains(result, "Unknown action") {
		t.Fatalf("expected unknown action error, got: %s", result)
	}
}

func TestSetConfig_BySkillName(t *testing.T) {
	reg := skill.NewRegistry()
	m := &skill.Manifest{
		Name:       "craft",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.craft.api_url"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search"}, m)
	reg.RegisterInGroup(&mockSkill{name: "craft_read", desc: "Read"}, "craft")

	store := newMockConfigStore()
	s := skillinfo.New(reg, store)

	// Use skill name (craft_read) to resolve manifest (craft).
	result := execute(t, s, adminCtx(), map[string]string{
		"action": "set_config", "skill_name": "craft_read",
		"config_key": "skills.craft.api_url", "config_value": "https://new.url",
	})

	if !strings.Contains(result, "Updated") {
		t.Fatalf("expected success via skill name resolution, got: %s", result)
	}
}

// --- Integration-style test ---

func TestFullFlow(t *testing.T) {
	reg := skill.NewRegistry()
	reg.SetCapabilities([]string{"memory"})

	memManifest := &skill.Manifest{
		Name:       "memory",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.memory.triggers", "skills.memory.half_life_days"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "remember", desc: "Remember facts", caps: []string{"memory"}}, memManifest)
	reg.RegisterInGroup(&mockSkill{name: "recall", desc: "Recall facts", caps: []string{"memory"}}, "memory")
	reg.RegisterInGroup(&mockSkill{name: "forget", desc: "Forget facts", caps: []string{"memory"}}, "memory")

	craftManifest := &skill.Manifest{
		Name:       "craft",
		Type:       skill.Internal,
		ConfigKeys: []string{"skills.craft.api_url", "skills.craft.api_key"},
		SecretKeys: []string{"skills.craft.api_key"},
	}
	reg.RegisterWithManifest(&mockSkill{name: "craft_search", desc: "Search", caps: []string{"craft"}}, craftManifest)
	// No "craft" capability = NO CREDENTIALS.

	reg.Register(&mockSkill{name: "datetime", desc: "Get time"})

	store := newMockConfigStore()
	store.secrets["skills.craft.api_key"] = true
	store.data["skills.memory.triggers"] = "remember,запомни"

	s := skillinfo.New(reg, store)
	ctx := adminCtx()

	// 1. List all.
	result := execute(t, s, ctx, map[string]string{"action": "list"})
	if !strings.Contains(result, "[memory]") {
		t.Error("missing memory group")
	}
	if !strings.Contains(result, "[craft]") {
		t.Error("missing craft group")
	}
	if !strings.Contains(result, "[NO CREDENTIALS]") {
		t.Error("craft should show NO CREDENTIALS")
	}
	if !strings.Contains(result, "datetime") {
		t.Error("missing standalone datetime")
	}

	// 2. Disable memory group.
	result = execute(t, s, ctx, map[string]string{"action": "disable", "skill_name": "memory"})
	if !strings.Contains(result, "Disabled skill group") {
		t.Error("expected group disable")
	}
	if !reg.IsDisabled("remember") || !reg.IsDisabled("recall") || !reg.IsDisabled("forget") {
		t.Error("all memory skills should be disabled")
	}

	// 3. Re-enable.
	result = execute(t, s, ctx, map[string]string{"action": "enable", "skill_name": "memory"})
	if !strings.Contains(result, "Enabled skill group") {
		t.Error("expected group enable")
	}
	if reg.IsDisabled("remember") || reg.IsDisabled("recall") || reg.IsDisabled("forget") {
		t.Error("all memory skills should be enabled")
	}

	// 4. Get config.
	result = execute(t, s, ctx, map[string]string{"action": "get_config", "skill_name": "memory"})
	if !strings.Contains(result, "skills.memory.triggers: remember,запомни") {
		t.Errorf("expected triggers value, got: %s", result)
	}

	// 5. Set config.
	result = execute(t, s, ctx, map[string]string{
		"action": "set_config", "skill_name": "memory",
		"config_key": "skills.memory.half_life_days", "config_value": "30",
	})
	if !strings.Contains(result, "Updated") {
		t.Errorf("expected update confirmation, got: %s", result)
	}

	// 6. Get config for craft secret — should be masked.
	result = execute(t, s, ctx, map[string]string{"action": "get_config", "skill_name": "craft"})
	if !strings.Contains(result, "[not set]") {
		t.Errorf("craft api_key should be [not set], got: %s", result)
	}

	// 7. Set secret.
	result = execute(t, s, ctx, map[string]string{
		"action": "set_config", "skill_name": "craft",
		"config_key": "skills.craft.api_key", "config_value": "super-secret",
	})
	if strings.Contains(result, "super-secret") {
		t.Error("secret should not be echoed")
	}

	// 8. Non-admin cannot disable.
	result = execute(t, s, userCtx(), map[string]string{"action": "disable", "skill_name": "memory"})
	if !strings.Contains(result, "admin privileges") {
		t.Error("non-admin should be rejected")
	}
}
