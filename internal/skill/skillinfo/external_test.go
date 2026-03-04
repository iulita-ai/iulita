package skillinfo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/skillinfo"
	"github.com/iulita-ai/iulita/internal/skillmgr"
)

// --- Mock external skill manager ---

type mockExtMgr struct {
	skills       []domain.InstalledSkill
	searchResult []skillmgr.SkillRef
	installed    *domain.InstalledSkill
	warnings     []string
	installErr   error
	uninstallErr error
	searchErr    error
	listErr      error
	getErr       error

	installCalls   []installCall
	uninstallCalls []string
}

type installCall struct {
	source, ref string
}

func (m *mockExtMgr) Search(_ context.Context, source, query string, limit int) ([]skillmgr.SkillRef, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	return m.searchResult, nil
}

func (m *mockExtMgr) Install(_ context.Context, source, ref string) (*domain.InstalledSkill, []string, error) {
	m.installCalls = append(m.installCalls, installCall{source, ref})
	if m.installErr != nil {
		return nil, nil, m.installErr
	}
	return m.installed, m.warnings, nil
}

func (m *mockExtMgr) Uninstall(_ context.Context, slug string) error {
	m.uninstallCalls = append(m.uninstallCalls, slug)
	return m.uninstallErr
}

func (m *mockExtMgr) ListInstalled(_ context.Context) ([]domain.InstalledSkill, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.skills, nil
}

func (m *mockExtMgr) GetInstalled(_ context.Context, slug string) (*domain.InstalledSkill, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	for i := range m.skills {
		if m.skills[i].Slug == slug {
			return &m.skills[i], nil
		}
	}
	return nil, fmt.Errorf("skill %q not found", slug)
}

// --- Helper ---

func executeJSON(t *testing.T, s *skillinfo.Skill, ctx context.Context, in any) string {
	t.Helper()
	raw, _ := json.Marshal(in)
	result, err := s.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return result
}

// --- Tests ---

func TestSearchExternal_RequiresAdmin(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, userCtx(), map[string]any{
		"action": "search_external",
		"query":  "weather",
	})
	if !strings.Contains(result, "admin") {
		t.Fatalf("expected admin error, got: %s", result)
	}
}

func TestSearchExternal_NilManager(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, nil)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "search_external",
		"query":  "weather",
	})
	if !strings.Contains(result, "not enabled") {
		t.Fatalf("expected not enabled, got: %s", result)
	}
}

func TestSearchExternal_MissingQuery(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "search_external",
	})
	if !strings.Contains(result, "query is required") {
		t.Fatalf("expected query required, got: %s", result)
	}
}

func TestSearchExternal_Success(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{
		searchResult: []skillmgr.SkillRef{
			{Slug: "weather-brief", Name: "Weather Brief", Version: "1.0.0", Author: "test"},
			{Slug: "weather-full", Name: "Full Weather", Description: "Detailed weather"},
		},
	}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "search_external",
		"query":  "weather",
	})
	if !strings.Contains(result, "weather-brief") {
		t.Fatalf("expected weather-brief, got: %s", result)
	}
	if !strings.Contains(result, "weather-full") {
		t.Fatalf("expected weather-full, got: %s", result)
	}
	if !strings.Contains(result, "Found 2") {
		t.Fatalf("expected Found 2, got: %s", result)
	}
}

func TestSearchExternal_NoResults(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{searchResult: []skillmgr.SkillRef{}}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "search_external",
		"query":  "nonexistent",
	})
	if !strings.Contains(result, "No skills found") {
		t.Fatalf("expected no skills found, got: %s", result)
	}
}

func TestInstallExternal_RequiresAdmin(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, userCtx(), map[string]any{
		"action": "install_external",
		"ref":    "weather-brief",
	})
	if !strings.Contains(result, "admin") {
		t.Fatalf("expected admin error, got: %s", result)
	}
}

func TestInstallExternal_MissingRef(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "install_external",
	})
	if !strings.Contains(result, "ref is required") {
		t.Fatalf("expected ref required, got: %s", result)
	}
}

func TestInstallExternal_Success(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{
		installed: &domain.InstalledSkill{
			Slug:      "weather-brief",
			Name:      "Weather Brief",
			Version:   "1.0.0",
			Isolation: "text_only",
			Enabled:   true,
		},
	}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "install_external",
		"ref":    "weather-brief",
	})
	if !strings.Contains(result, "Installed skill") {
		t.Fatalf("expected installed, got: %s", result)
	}
	if !strings.Contains(result, "Weather Brief") {
		t.Fatalf("expected name, got: %s", result)
	}
	// Default source should be clawhub.
	if len(mgr.installCalls) != 1 || mgr.installCalls[0].source != "clawhub" {
		t.Fatalf("expected clawhub source, got: %v", mgr.installCalls)
	}
}

func TestInstallExternal_WithWarnings(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{
		installed: &domain.InstalledSkill{
			Slug: "test", Name: "Test", Version: "1.0.0", Isolation: "text_only", Enabled: true,
		},
		warnings: []string{"deprecated frontmatter key", "missing description"},
	}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "install_external",
		"ref":    "test",
	})
	if !strings.Contains(result, "Warnings") {
		t.Fatalf("expected warnings, got: %s", result)
	}
	if !strings.Contains(result, "deprecated frontmatter") {
		t.Fatalf("expected warning text, got: %s", result)
	}
}

func TestInstallExternal_Error(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{installErr: fmt.Errorf("max installed skills reached (50)")}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "install_external",
		"ref":    "something",
	})
	if !strings.Contains(result, "Install failed") {
		t.Fatalf("expected install failed, got: %s", result)
	}
}

func TestInstallExternal_CustomSource(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{
		installed: &domain.InstalledSkill{
			Slug: "custom", Name: "Custom", Version: "1.0.0", Isolation: "text_only", Enabled: true,
		},
	}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	executeJSON(t, s, adminCtx(), map[string]any{
		"action": "install_external",
		"source": "url",
		"ref":    "https://example.com/skill.zip",
	})
	if len(mgr.installCalls) != 1 || mgr.installCalls[0].source != "url" {
		t.Fatalf("expected url source, got: %v", mgr.installCalls)
	}
}

func TestUninstallExternal_RequiresAdmin(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, userCtx(), map[string]any{
		"action": "uninstall_external",
		"slug":   "weather-brief",
	})
	if !strings.Contains(result, "admin") {
		t.Fatalf("expected admin error, got: %s", result)
	}
}

func TestUninstallExternal_MissingSlug(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "uninstall_external",
	})
	if !strings.Contains(result, "slug is required") {
		t.Fatalf("expected slug required, got: %s", result)
	}
}

func TestUninstallExternal_Success(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "uninstall_external",
		"slug":   "weather-brief",
	})
	if !strings.Contains(result, "Uninstalled") {
		t.Fatalf("expected uninstalled, got: %s", result)
	}
	if len(mgr.uninstallCalls) != 1 || mgr.uninstallCalls[0] != "weather-brief" {
		t.Fatalf("expected uninstall call, got: %v", mgr.uninstallCalls)
	}
}

func TestUninstallExternal_Error(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{uninstallErr: fmt.Errorf("skill not found")}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "uninstall_external",
		"slug":   "nonexistent",
	})
	if !strings.Contains(result, "Uninstall failed") {
		t.Fatalf("expected uninstall failed, got: %s", result)
	}
}

func TestListExternal_Empty(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{skills: []domain.InstalledSkill{}}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "list_external",
	})
	if !strings.Contains(result, "No external skills") {
		t.Fatalf("expected no skills, got: %s", result)
	}
}

func TestListExternal_WithSkills(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{
		skills: []domain.InstalledSkill{
			{Slug: "weather", Name: "Weather", Version: "1.0.0", Isolation: "text_only", Enabled: true, Source: "clawhub"},
			{Slug: "calc", Name: "Calculator", Version: "2.0.0", Isolation: "docker", Enabled: false, Source: "url"},
		},
	}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "list_external",
	})
	if !strings.Contains(result, "weather") {
		t.Fatalf("expected weather, got: %s", result)
	}
	if !strings.Contains(result, "calc") {
		t.Fatalf("expected calc, got: %s", result)
	}
	if !strings.Contains(result, "DISABLED") {
		t.Fatalf("expected DISABLED status, got: %s", result)
	}
}

func TestListExternal_NilManager(t *testing.T) {
	reg := skill.NewRegistry()
	s := skillinfo.New(reg, nil)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "list_external",
	})
	if !strings.Contains(result, "not enabled") {
		t.Fatalf("expected not enabled, got: %s", result)
	}
}

func TestUpdateExternal_RequiresAdmin(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, userCtx(), map[string]any{
		"action": "update_external",
		"slug":   "weather",
	})
	if !strings.Contains(result, "admin") {
		t.Fatalf("expected admin error, got: %s", result)
	}
}

func TestUpdateExternal_MissingSlug(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "update_external",
	})
	if !strings.Contains(result, "slug is required") {
		t.Fatalf("expected slug required, got: %s", result)
	}
}

func TestUpdateExternal_Success(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{
		skills: []domain.InstalledSkill{
			{Slug: "weather", Name: "Weather", Version: "1.0.0", Source: "clawhub", SourceRef: "weather", InstalledAt: time.Now()},
		},
		installed: &domain.InstalledSkill{
			Slug: "weather", Name: "Weather", Version: "2.0.0", Source: "clawhub", Isolation: "text_only", Enabled: true,
		},
	}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "update_external",
		"slug":   "weather",
	})
	if !strings.Contains(result, "Updated") {
		t.Fatalf("expected updated, got: %s", result)
	}
	if !strings.Contains(result, "v1.0.0 -> v2.0.0") {
		t.Fatalf("expected version change, got: %s", result)
	}
	// Should have called uninstall then install.
	if len(mgr.uninstallCalls) != 1 || mgr.uninstallCalls[0] != "weather" {
		t.Fatalf("expected uninstall call, got: %v", mgr.uninstallCalls)
	}
	if len(mgr.installCalls) != 1 || mgr.installCalls[0].source != "clawhub" {
		t.Fatalf("expected install call, got: %v", mgr.installCalls)
	}
}

func TestUpdateExternal_SkillNotFound(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{skills: []domain.InstalledSkill{}}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "update_external",
		"slug":   "nonexistent",
	})
	if !strings.Contains(result, "not found") {
		t.Fatalf("expected not found, got: %s", result)
	}
}

func TestUpdateExternal_InstallFails(t *testing.T) {
	reg := skill.NewRegistry()
	mgr := &mockExtMgr{
		skills: []domain.InstalledSkill{
			{Slug: "weather", Name: "Weather", Version: "1.0.0", Source: "clawhub", SourceRef: "weather"},
		},
		installErr: fmt.Errorf("download failed"),
	}
	s := skillinfo.NewWithExternalManager(reg, nil, mgr)

	result := executeJSON(t, s, adminCtx(), map[string]any{
		"action": "update_external",
		"slug":   "weather",
	})
	if !strings.Contains(result, "failed to reinstall") {
		t.Fatalf("expected reinstall failure, got: %s", result)
	}
	if !strings.Contains(result, "Manual reinstall") {
		t.Fatalf("expected manual reinstall hint, got: %s", result)
	}
}
