package skillmgr

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
)

// memStore is an in-memory SkillStore for testing.
type memStore struct {
	skills map[string]*domain.InstalledSkill
}

func newMemStore() *memStore {
	return &memStore{skills: make(map[string]*domain.InstalledSkill)}
}

func (m *memStore) SaveInstalledSkill(_ context.Context, s *domain.InstalledSkill) error {
	m.skills[s.Slug] = s
	return nil
}

func (m *memStore) GetInstalledSkill(_ context.Context, slug string) (*domain.InstalledSkill, error) {
	s, ok := m.skills[slug]
	if !ok {
		return nil, os.ErrNotExist
	}
	return s, nil
}

func (m *memStore) ListInstalledSkills(_ context.Context) ([]domain.InstalledSkill, error) {
	var result []domain.InstalledSkill
	for _, s := range m.skills {
		result = append(result, *s)
	}
	return result, nil
}

func (m *memStore) UpdateInstalledSkill(_ context.Context, s *domain.InstalledSkill) error {
	m.skills[s.Slug] = s
	return nil
}

func (m *memStore) DeleteInstalledSkill(_ context.Context, slug string) error {
	delete(m.skills, slug)
	return nil
}

func setupTestManager(t *testing.T) (*Manager, *memStore, string) {
	t.Helper()
	dir := t.TempDir()
	store := newMemStore()
	registry := skill.NewRegistry()
	cfg := config.ExternalSkillsConfig{
		Enabled:      true,
		Dir:          dir,
		MaxInstalled: 10,
		AllowShell:   false,
		AllowDocker:  false,
		AllowWASM:    true,
	}
	log := zap.NewNop()
	mgr := NewManager(store, registry, cfg, RuntimeCaps{}, log)
	return mgr, store, dir
}

func TestManagerLoadAllEmpty(t *testing.T) {
	mgr, _, _ := setupTestManager(t)
	if err := mgr.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerLoadAllDisabled(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	registry := skill.NewRegistry()
	cfg := config.ExternalSkillsConfig{Enabled: false, Dir: dir}
	mgr := NewManager(store, registry, cfg, RuntimeCaps{}, zap.NewNop())

	if err := mgr.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerLoadAllWithTextSkill(t *testing.T) {
	mgr, store, dir := setupTestManager(t)

	// Create a text-only skill on disk.
	skillDir := filepath.Join(dir, "greet")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: greet\ndescription: Greet users\n---\nAlways greet users warmly."), 0644)

	store.skills["greet"] = &domain.InstalledSkill{
		Slug:        "greet",
		Name:        "greet",
		Source:      "local",
		Isolation:   domain.IsolationTextOnly,
		InstallDir:  skillDir,
		Enabled:     true,
		InstalledAt: time.Now(),
	}

	if err := mgr.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Check it was registered (system prompt injection + enable/disable support).
	allSkills := mgr.registry.AllSkills()
	found := false
	for _, s := range allSkills {
		if s.Skill.Name() == "greet" {
			found = true
			// Text-only skills have nil InputSchema — assistant.go skips them in tool list.
			if s.Skill.InputSchema() != nil {
				t.Error("text-only skill should have nil InputSchema")
			}
			break
		}
	}
	if !found {
		t.Error("skill 'greet' should be registered")
	}
}

func TestManagerLoadDisabledSkill(t *testing.T) {
	mgr, store, dir := setupTestManager(t)

	skillDir := filepath.Join(dir, "disabled-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: disabled-skill\ndescription: A disabled skill\n---\nInstructions."), 0644)

	store.skills["disabled-skill"] = &domain.InstalledSkill{
		Slug:        "disabled-skill",
		Name:        "disabled-skill",
		Source:      "local",
		Isolation:   domain.IsolationTextOnly,
		InstallDir:  skillDir,
		Enabled:     false,
		InstalledAt: time.Now(),
	}

	if err := mgr.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !mgr.registry.IsDisabled("disabled-skill") {
		t.Error("skill should be disabled in registry")
	}
}

func TestLoadTextOnlySkillWithWebfetchProxy(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	registry := skill.NewRegistry()
	cfg := config.ExternalSkillsConfig{
		Enabled:      true,
		Dir:          dir,
		MaxInstalled: 10,
	}
	caps := RuntimeCaps{
		ShellExecEnabled:  false,
		WebfetchAvailable: true,
		HTTPClient:        &http.Client{},
	}
	mgr := NewManager(store, registry, cfg, caps, zap.NewNop())

	// Create a text-only skill with clawdbot metadata requiring curl.
	skillDir := filepath.Join(dir, "weather")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(
		"---\nname: weather\ndescription: Get weather\n"+
			"metadata: '{\"clawdbot\":{\"requires\":{\"bins\":[\"curl\"]}}}'\n"+
			"---\nUse curl to get weather from https://wttr.in/London?format=3\n"), 0644)

	store.skills["weather"] = &domain.InstalledSkill{
		Slug:        "weather",
		Name:        "weather",
		Source:      "clawhub",
		Isolation:   domain.IsolationTextOnly,
		InstallDir:  skillDir,
		Enabled:     true,
		InstalledAt: time.Now(),
	}

	if err := mgr.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify the skill was registered as a proxy tool (has InputSchema).
	allSkills := registry.AllSkills()
	found := false
	for _, s := range allSkills {
		if s.Skill.Name() == "weather" {
			found = true
			if s.Skill.InputSchema() == nil {
				t.Error("webfetch proxy skill should have an InputSchema")
			}
			break
		}
	}
	if !found {
		t.Error("skill 'weather' should be registered")
	}
}

func TestLoadTextOnlySkillWithoutHTTPClient(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	registry := skill.NewRegistry()
	cfg := config.ExternalSkillsConfig{
		Enabled:      true,
		Dir:          dir,
		MaxInstalled: 10,
	}
	// WebfetchAvailable but no HTTPClient — should fall back to text-only.
	caps := RuntimeCaps{
		ShellExecEnabled:  false,
		WebfetchAvailable: true,
		HTTPClient:        nil,
	}
	mgr := NewManager(store, registry, cfg, caps, zap.NewNop())

	skillDir := filepath.Join(dir, "weather2")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(
		"---\nname: weather2\ndescription: Get weather\n"+
			"metadata: '{\"clawdbot\":{\"requires\":{\"bins\":[\"curl\"]}}}'\n"+
			"---\nUse curl for weather\n"), 0644)

	store.skills["weather2"] = &domain.InstalledSkill{
		Slug:        "weather2",
		Name:        "weather2",
		Source:      "clawhub",
		Isolation:   domain.IsolationTextOnly,
		InstallDir:  skillDir,
		Enabled:     true,
		InstalledAt: time.Now(),
	}

	if err := mgr.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Should be text-only (nil InputSchema) since no HTTP client provided.
	allSkills := registry.AllSkills()
	for _, s := range allSkills {
		if s.Skill.Name() == "weather2" {
			if s.Skill.InputSchema() != nil {
				t.Error("without HTTPClient, skill should remain text-only (nil schema)")
			}
			return
		}
	}
	t.Error("skill 'weather2' should be registered")
}

func TestManagerValidateIsolation(t *testing.T) {
	mgr, _, _ := setupTestManager(t)

	// text_only is always allowed.
	if err := mgr.validateIsolation("text_only"); err != nil {
		t.Errorf("text_only should be allowed: %v", err)
	}

	// shell is disabled by default.
	if err := mgr.validateIsolation("shell"); err == nil {
		t.Error("shell should be rejected when allow_shell=false")
	}

	// docker is disabled by default.
	if err := mgr.validateIsolation("docker"); err == nil {
		t.Error("docker should be rejected when allow_docker=false")
	}

	// wasm is enabled.
	if err := mgr.validateIsolation("wasm"); err != nil {
		t.Errorf("wasm should be allowed: %v", err)
	}
}

func TestManagerInstallDisabled(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	registry := skill.NewRegistry()
	cfg := config.ExternalSkillsConfig{Enabled: false, Dir: dir}
	mgr := NewManager(store, registry, cfg, RuntimeCaps{}, zap.NewNop())

	_, _, err := mgr.Install(context.Background(), "clawhub", "test-skill")
	if err == nil {
		t.Fatal("expected error when external skills disabled")
	}
}

func TestManagerInstallMaxReached(t *testing.T) {
	mgr, store, _ := setupTestManager(t)
	mgr.cfg.MaxInstalled = 1

	store.skills["existing"] = &domain.InstalledSkill{Slug: "existing"}

	_, _, err := mgr.Install(context.Background(), "clawhub", "new-skill")
	if err == nil {
		t.Fatal("expected error when max installed reached")
	}
}

func TestManagerUninstallNonexistent(t *testing.T) {
	mgr, _, _ := setupTestManager(t)
	err := mgr.Uninstall(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
}

func TestManagerEnableDisable(t *testing.T) {
	mgr, store, dir := setupTestManager(t)

	skillDir := filepath.Join(dir, "toggle-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: toggle-skill\ndescription: Test toggle\n---\nInstructions."), 0644)

	store.skills["toggle-skill"] = &domain.InstalledSkill{
		Slug:        "toggle-skill",
		Name:        "toggle-skill",
		Source:      "local",
		Isolation:   domain.IsolationTextOnly,
		InstallDir:  skillDir,
		Enabled:     true,
		InstalledAt: time.Now(),
	}

	mgr.LoadAll(context.Background())

	// Disable.
	if err := mgr.Disable(context.Background(), "toggle-skill"); err != nil {
		t.Fatal(err)
	}
	if !store.skills["toggle-skill"].Enabled {
		// Wait, Disable should set Enabled=false.
	}
	if store.skills["toggle-skill"].Enabled {
		t.Error("skill should be disabled in store")
	}

	// Enable.
	if err := mgr.Enable(context.Background(), "toggle-skill"); err != nil {
		t.Fatal(err)
	}
	if !store.skills["toggle-skill"].Enabled {
		t.Error("skill should be enabled in store")
	}
}

func TestTextOnlySkillAdapter(t *testing.T) {
	m := &skill.Manifest{
		Name:        "test-skill",
		Description: "A test skill",
	}
	s := newTextOnlySkill(m)

	if s.Name() != "test-skill" {
		t.Errorf("got name %q", s.Name())
	}
	if s.Description() != "A test skill" {
		t.Errorf("got description %q", s.Description())
	}
	if s.InputSchema() != nil {
		t.Error("text-only skill should have nil schema")
	}

	_, err := s.Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("text-only skill should return error on execute")
	}
}

func TestWebfetchProxySkillURLExtraction(t *testing.T) {
	// Simulate real weather SKILL.md with many example URLs.
	m := &skill.Manifest{
		Name:        "weather",
		Description: "Get weather",
		SystemPrompt: "Quick one-liner:\n```bash\ncurl -s \"wttr.in/London?format=3\"\n```\n" +
			"Compact format:\n```bash\ncurl -s \"wttr.in/London?format=%l:+%c+%t+%h+%w\"\n```\n" +
			"Full forecast:\n```bash\ncurl -s \"wttr.in/London?T\"\n```\n" +
			"Tips: wttr.in/New+York wttr.in/JFK\n" +
			"PNG: curl -s \"wttr.in/Berlin.png\" -o /tmp/weather.png\n" +
			"Fallback JSON:\n```bash\ncurl -s \"https://api.open-meteo.com/v1/forecast?latitude=51.5&longitude=-0.12&current_weather=true\"\n```\n" +
			"Docs: https://open-meteo.com/en/docs\n",
	}
	proxy := newWebfetchProxySkill(m, &http.Client{}, []string{"curl"})

	// curl bin → curl User-Agent.
	if proxy.userAgent != "curl/8.0" {
		t.Errorf("expected curl UA for curl-based skill, got: %s", proxy.userAgent)
	}

	// Should deduplicate wttr.in/London?* variants to a single hint.
	wttrCount := 0
	for _, h := range proxy.urlHints {
		if strings.Contains(h, "wttr.in/London") {
			wttrCount++
		}
	}
	if wttrCount != 1 {
		t.Errorf("should deduplicate wttr.in/London variants to 1, got %d in hints: %v", wttrCount, proxy.urlHints)
	}

	// Should filter out .png URLs.
	for _, h := range proxy.urlHints {
		if strings.Contains(h, ".png") {
			t.Errorf("should filter out .png URLs, got: %s", h)
		}
	}

	// Should filter out /docs URLs.
	for _, h := range proxy.urlHints {
		if strings.Contains(h, "/docs") {
			t.Errorf("should filter out /docs URLs, got: %s", h)
		}
	}

	// Test buildURLs substitutes London → Berlin.
	urls := proxy.buildURLs("Berlin")
	if len(urls) == 0 {
		t.Fatal("buildURLs should produce at least one URL")
	}

	hasBerlin := false
	for _, u := range urls {
		if strings.Contains(u, "Berlin") {
			hasBerlin = true
		}
	}
	if !hasBerlin {
		t.Errorf("buildURLs should substitute query into URLs, got: %v", urls)
	}
}

func TestWebfetchProxySkillTemplateVars(t *testing.T) {
	// Test {placeholder} template substitution — generic, no hardcoded domains.
	m := &skill.Manifest{
		Name:         "test-api",
		Description:  "Test API skill",
		SystemPrompt: "Use: https://api.example.com/v1/{query}/data\n",
	}
	proxy := newWebfetchProxySkill(m, &http.Client{}, nil)

	urls := proxy.buildURLs("test-input")
	if len(urls) == 0 {
		t.Fatal("buildURLs should produce URL from template var")
	}
	if !strings.Contains(urls[0], "test-input") {
		t.Errorf("should substitute {query}, got: %s", urls[0])
	}
	if strings.Contains(urls[0], "{query}") {
		t.Errorf("should not contain raw template var, got: %s", urls[0])
	}
}

func TestStripTemporalModifiers(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"London tomorrow", "London"},
		{"Saint Petersburg tomorrow", "Saint Petersburg"},
		{"спб на завтра", "спб"},
		{"New York next week", "New York"},
		{"Tokyo", "Tokyo"},       // no modifiers
		{"tomorrow", "tomorrow"}, // don't strip everything
		{"Berlin forecast", "Berlin"},
	}
	for _, tt := range tests {
		got := stripTemporalModifiers(tt.input)
		if got != tt.want {
			t.Errorf("stripTemporalModifiers(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExecutableSkillAdapter(t *testing.T) {
	m := &skill.Manifest{
		Name:        "exec-skill",
		Description: "An executable skill",
		External: &skill.ExternalManifestExt{
			InstallDir: "/tmp/test",
		},
	}
	s := newExecutableSkill(m, nil, "main.py")

	if s.Name() != "exec-skill" {
		t.Errorf("got name %q", s.Name())
	}

	schema := s.InputSchema()
	if schema == nil {
		t.Fatal("executable skill should have schema")
	}

	var schemaMap map[string]any
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		t.Fatal(err)
	}
	if schemaMap["type"] != "object" {
		t.Errorf("schema type should be object, got %v", schemaMap["type"])
	}
}
