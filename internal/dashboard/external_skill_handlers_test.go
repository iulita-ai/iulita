package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

// --- Mock external skill manager ---

type mockExtSkillMgr struct {
	skills       []domain.InstalledSkill
	searchResult []ExternalSkillResult
	installed    *domain.InstalledSkill
	warnings     []string
	installErr   error
	uninstallErr error
	enableErr    error
	disableErr   error
	searchErr    error
	listErr      error
	getErr       error

	installCalls   []extInstallCall
	uninstallCalls []string
}

type extInstallCall struct {
	source, ref string
}

func (m *mockExtSkillMgr) ListInstalled(_ context.Context) ([]domain.InstalledSkill, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.skills, nil
}

func (m *mockExtSkillMgr) GetInstalled(_ context.Context, slug string) (*domain.InstalledSkill, error) {
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

func (m *mockExtSkillMgr) Install(_ context.Context, source, ref string) (*domain.InstalledSkill, []string, error) {
	m.installCalls = append(m.installCalls, extInstallCall{source, ref})
	if m.installErr != nil {
		return nil, nil, m.installErr
	}
	return m.installed, m.warnings, nil
}

func (m *mockExtSkillMgr) Uninstall(_ context.Context, slug string) error {
	m.uninstallCalls = append(m.uninstallCalls, slug)
	return m.uninstallErr
}

func (m *mockExtSkillMgr) Enable(_ context.Context, slug string) error {
	return m.enableErr
}

func (m *mockExtSkillMgr) Disable(_ context.Context, slug string) error {
	return m.disableErr
}

func (m *mockExtSkillMgr) Search(_ context.Context, source, query string, limit int) ([]ExternalSkillResult, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	return m.searchResult, nil
}

func (m *mockExtSkillMgr) ResolveMarketplace(_ context.Context, source, ref string) (*ExternalSkillDetail, error) {
	return &ExternalSkillDetail{Slug: ref, Name: ref, Source: source}, nil
}

// --- Test helpers ---

const testJWTSecret = "test-secret-key-for-external-skill-tests"

func buildExtSkillServer(t *testing.T, mgr ExternalSkillManager) *Server {
	t.Helper()
	logger := zap.NewNop()

	// Create an isolated SQLite store for auth.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Create an admin user.
	hash, _ := auth.HashPassword("test-pass")
	admin := &domain.User{
		ID:           "admin-1",
		Username:     "admin",
		Role:         "admin",
		PasswordHash: hash,
	}
	if err := store.CreateUser(context.Background(), admin); err != nil {
		t.Fatal(err)
	}

	authSvc := auth.NewService(store, testJWTSecret, time.Hour, 24*time.Hour)
	srv := New(Config{
		Address:      ":0",
		Logger:       logger,
		StaticFS:     fstest.MapFS{"index.html": {Data: []byte("ok")}},
		AuthService:  authSvc,
		SkillManager: mgr,
	})
	return srv
}

func testAdminToken(t *testing.T) string {
	t.Helper()
	now := time.Now()
	claims := auth.Claims{
		UserID:   "admin-1",
		Username: "admin",
		Role:     "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   "admin-1",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func doExtRequest(t *testing.T, srv *Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAdminToken(t))

	resp, err := srv.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var result map[string]any
	respBody, _ := io.ReadAll(resp.Body)
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, &result)
	}
	return resp.StatusCode, result
}

// --- Tests ---

func TestHandleListExternalSkills_Empty(t *testing.T) {
	mgr := &mockExtSkillMgr{skills: []domain.InstalledSkill{}}
	srv := buildExtSkillServer(t, mgr)

	status, _ := doExtRequest(t, srv, "GET", "/api/skills/external/", nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}

func TestHandleListExternalSkills_WithSkills(t *testing.T) {
	mgr := &mockExtSkillMgr{
		skills: []domain.InstalledSkill{
			{Slug: "weather", Name: "Weather", Version: "1.0.0"},
			{Slug: "calc", Name: "Calculator", Version: "2.0.0"},
		},
	}
	srv := buildExtSkillServer(t, mgr)

	status, _ := doExtRequest(t, srv, "GET", "/api/skills/external/", nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}

func TestHandleInstallExternalSkill_Success(t *testing.T) {
	mgr := &mockExtSkillMgr{
		installed: &domain.InstalledSkill{
			Slug: "weather", Name: "Weather", Version: "1.0.0", Isolation: "text_only", Enabled: true,
		},
	}
	srv := buildExtSkillServer(t, mgr)

	status, result := doExtRequest(t, srv, "POST", "/api/skills/external/install", map[string]string{
		"source": "clawhub",
		"ref":    "weather",
	})
	if status != 201 {
		t.Fatalf("expected 201, got %d: %v", status, result)
	}
	if result["warnings"] == nil {
		t.Fatal("expected warnings array")
	}
}

func TestHandleInstallExternalSkill_MissingRef(t *testing.T) {
	mgr := &mockExtSkillMgr{}
	srv := buildExtSkillServer(t, mgr)

	status, _ := doExtRequest(t, srv, "POST", "/api/skills/external/install", map[string]string{
		"source": "clawhub",
	})
	if status != 400 {
		t.Fatalf("expected 400, got %d", status)
	}
}

func TestHandleInstallExternalSkill_Error(t *testing.T) {
	mgr := &mockExtSkillMgr{
		installErr: fmt.Errorf("max installed skills reached (50)"),
	}
	srv := buildExtSkillServer(t, mgr)

	status, _ := doExtRequest(t, srv, "POST", "/api/skills/external/install", map[string]string{
		"source": "clawhub",
		"ref":    "weather",
	})
	if status != 400 {
		t.Fatalf("expected 400, got %d", status)
	}
}

func TestHandleUninstallExternalSkill_Success(t *testing.T) {
	mgr := &mockExtSkillMgr{}
	srv := buildExtSkillServer(t, mgr)

	status, result := doExtRequest(t, srv, "DELETE", "/api/skills/external/weather", nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, result)
	}
	if result["status"] != "deleted" {
		t.Fatalf("expected deleted status, got: %v", result)
	}
}

func TestHandleUninstallExternalSkill_NotFound(t *testing.T) {
	mgr := &mockExtSkillMgr{
		uninstallErr: fmt.Errorf("get skill \"nonexistent\": not found"),
	}
	srv := buildExtSkillServer(t, mgr)

	status, _ := doExtRequest(t, srv, "DELETE", "/api/skills/external/nonexistent", nil)
	if status != 404 {
		t.Fatalf("expected 404, got %d", status)
	}
}

func TestHandleSearchExternalSkills_Success(t *testing.T) {
	mgr := &mockExtSkillMgr{
		searchResult: []ExternalSkillResult{
			{Slug: "weather-brief", Name: "Weather Brief"},
			{Slug: "weather-full", Name: "Full Weather"},
		},
	}
	srv := buildExtSkillServer(t, mgr)

	status, result := doExtRequest(t, srv, "POST", "/api/skills/external/search", map[string]any{
		"source": "clawhub",
		"query":  "weather",
	})
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, result)
	}
	count, ok := result["count"].(float64)
	if !ok || count != 2 {
		t.Fatalf("expected count 2, got: %v", result["count"])
	}
}

func TestHandleSearchExternalSkills_MissingQuery(t *testing.T) {
	mgr := &mockExtSkillMgr{}
	srv := buildExtSkillServer(t, mgr)

	status, _ := doExtRequest(t, srv, "POST", "/api/skills/external/search", map[string]any{
		"source": "clawhub",
	})
	if status != 400 {
		t.Fatalf("expected 400, got %d", status)
	}
}

func TestHandleEnableExternalSkill_Success(t *testing.T) {
	mgr := &mockExtSkillMgr{}
	srv := buildExtSkillServer(t, mgr)

	status, result := doExtRequest(t, srv, "PUT", "/api/skills/external/weather/enable", nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, result)
	}
	if result["enabled"] != true {
		t.Fatalf("expected enabled=true, got: %v", result)
	}
}

func TestHandleDisableExternalSkill_Success(t *testing.T) {
	mgr := &mockExtSkillMgr{}
	srv := buildExtSkillServer(t, mgr)

	status, result := doExtRequest(t, srv, "PUT", "/api/skills/external/weather/disable", nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, result)
	}
	if result["enabled"] != false {
		t.Fatalf("expected enabled=false, got: %v", result)
	}
}

func TestHandleEnableExternalSkill_NotFound(t *testing.T) {
	mgr := &mockExtSkillMgr{
		enableErr: fmt.Errorf("skill not found"),
	}
	srv := buildExtSkillServer(t, mgr)

	status, _ := doExtRequest(t, srv, "PUT", "/api/skills/external/nonexistent/enable", nil)
	if status != 404 {
		t.Fatalf("expected 404, got %d", status)
	}
}

func TestHandleUpdateExternalSkill_Success(t *testing.T) {
	mgr := &mockExtSkillMgr{
		skills: []domain.InstalledSkill{
			{Slug: "weather", Name: "Weather", Version: "1.0.0", Source: "clawhub", SourceRef: "weather"},
		},
		installed: &domain.InstalledSkill{
			Slug: "weather", Name: "Weather", Version: "2.0.0", Isolation: "text_only", Enabled: true,
		},
	}
	srv := buildExtSkillServer(t, mgr)

	status, result := doExtRequest(t, srv, "POST", "/api/skills/external/weather/update", nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, result)
	}
}

func TestHandleUpdateExternalSkill_NotFound(t *testing.T) {
	mgr := &mockExtSkillMgr{skills: []domain.InstalledSkill{}}
	srv := buildExtSkillServer(t, mgr)

	status, _ := doExtRequest(t, srv, "POST", "/api/skills/external/nonexistent/update", nil)
	if status != 404 {
		t.Fatalf("expected 404, got %d", status)
	}
}

func TestHandleInstallExternalSkill_DefaultSource(t *testing.T) {
	mgr := &mockExtSkillMgr{
		installed: &domain.InstalledSkill{
			Slug: "test", Name: "Test", Version: "1.0.0", Isolation: "text_only", Enabled: true,
		},
	}
	srv := buildExtSkillServer(t, mgr)

	status, _ := doExtRequest(t, srv, "POST", "/api/skills/external/install", map[string]string{
		"ref": "test",
	})
	if status != 201 {
		t.Fatalf("expected 201, got %d", status)
	}
	if len(mgr.installCalls) != 1 || mgr.installCalls[0].source != "clawhub" {
		t.Fatalf("expected default clawhub source, got: %v", mgr.installCalls)
	}
}
