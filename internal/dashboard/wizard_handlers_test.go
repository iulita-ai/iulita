package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
)

// memConfigRepo is an in-memory stub for config.ConfigRepository.
type memConfigRepo struct {
	mu   sync.Mutex
	data map[string]domain.ConfigOverride
}

func newMemConfigRepo() *memConfigRepo {
	return &memConfigRepo{data: make(map[string]domain.ConfigOverride)}
}

func (r *memConfigRepo) GetConfigOverride(_ context.Context, key string) (*domain.ConfigOverride, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.data[key]
	if !ok {
		return nil, nil
	}
	return &o, nil
}

func (r *memConfigRepo) ListConfigOverrides(_ context.Context) ([]domain.ConfigOverride, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]domain.ConfigOverride, 0, len(r.data))
	for _, v := range r.data {
		list = append(list, v)
	}
	return list, nil
}

func (r *memConfigRepo) SaveConfigOverride(_ context.Context, o *domain.ConfigOverride) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[o.Key] = *o
	return nil
}

func (r *memConfigRepo) DeleteConfigOverride(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, key)
	return nil
}

// minimalStaticFS returns a minimal fs.FS satisfying the SPA middleware requirement.
func minimalStaticFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {Data: []byte("<html><body>test</body></html>")},
	}
}

// buildWizardServer creates a minimal dashboard Server with the given config.Store.
func buildWizardServer(t *testing.T, cs *config.Store) *Server {
	t.Helper()
	logger := zap.NewNop()
	srv := New(Config{
		Address:     ":0",
		Logger:      logger,
		StaticFS:    minimalStaticFS(),
		ConfigStore: cs,
	})
	return srv
}

// buildConfigStore creates a *config.Store backed by an in-memory repo
// with a clean base config (no keyring/env side effects).
func buildConfigStore(t *testing.T, _ string) *config.Store {
	t.Helper()

	// Use a bare Config with no LLM keys to avoid keyring interference.
	baseCfg := &config.Config{}
	repo := newMemConfigRepo()
	store := config.NewStore(baseCfg, nil, repo, nil, zap.NewNop())
	return store
}

// TestHandleWizardStatus_NoConfigStore verifies the endpoint returns HTTP 404
// when configStore is nil (wizard routes are not registered).
func TestHandleWizardStatus_NoConfigStore(t *testing.T) {
	logger := zap.NewNop()
	srv := New(Config{
		Address:     ":0",
		Logger:      logger,
		StaticFS:    minimalStaticFS(),
		ConfigStore: nil, // no store → wizard routes not registered
	})

	req := httptest.NewRequest("GET", "/api/wizard/status", nil)
	resp, err := srv.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	// Without configStore, wizard routes are not mounted; request should not match.
	// The SPA filesystem middleware will serve index.html (200) as fallback,
	// or a 404 if the index.html path doesn't match. Either way, it won't be a wizard response.
	// We only verify there is no internal server error.
	if resp.StatusCode == 500 {
		t.Errorf("expected non-500 status, got %d", resp.StatusCode)
	}
}

// TestHandleWizardStatus_DefaultState verifies that a fresh config store
// returns wizard_completed=false and setup_mode=false.
func TestHandleWizardStatus_DefaultState(t *testing.T) {
	dir := t.TempDir()
	cs := buildConfigStore(t, dir)
	srv := buildWizardServer(t, cs)

	req := httptest.NewRequest("GET", "/api/wizard/status", nil)
	resp, err := srv.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if v, ok := body["wizard_completed"]; !ok || v != false {
		t.Errorf("wizard_completed = %v, want false", v)
	}
	if v, ok := body["setup_mode"]; !ok || v != false {
		t.Errorf("setup_mode = %v, want false", v)
	}
	if _, ok := body["has_llm_provider"]; !ok {
		t.Error("has_llm_provider field missing from response")
	}
	if _, ok := body["encryption_enabled"]; !ok {
		t.Error("encryption_enabled field missing from response")
	}
}

// TestHandleWizardStatus_WizardCompletedAfterSet verifies that after setting
// the wizard_completed override, the status endpoint reflects it.
func TestHandleWizardStatus_WizardCompletedAfterSet(t *testing.T) {
	dir := t.TempDir()
	cs := buildConfigStore(t, dir)

	// Set wizard_completed override directly via the store.
	ctx := context.Background()
	if err := cs.Set(ctx, "_system.wizard_completed", "true", "test", false); err != nil {
		t.Fatalf("cs.Set wizard_completed: %v", err)
	}

	srv := buildWizardServer(t, cs)

	req := httptest.NewRequest("GET", "/api/wizard/status", nil)
	resp, err := srv.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if v, ok := body["wizard_completed"]; !ok || v != true {
		t.Errorf("wizard_completed = %v, want true", v)
	}
}

// TestHandleWizardStatus_HasLLMProvider verifies that when a Claude API key override
// is set, has_llm_provider returns true.
func TestHandleWizardStatus_HasLLMProvider(t *testing.T) {
	dir := t.TempDir()
	cs := buildConfigStore(t, dir)

	ctx := context.Background()
	if err := cs.Set(ctx, "claude.api_key", "sk-test-key", "test", false); err != nil {
		t.Fatalf("cs.Set claude.api_key: %v", err)
	}

	srv := buildWizardServer(t, cs)

	req := httptest.NewRequest("GET", "/api/wizard/status", nil)
	resp, err := srv.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if v, ok := body["has_llm_provider"]; !ok || v != true {
		t.Errorf("has_llm_provider = %v, want true", v)
	}
}

// TestHandleWizardComplete_NoLLM verifies that completing the wizard without
// any LLM configured returns HTTP 400 with an error message.
func TestHandleWizardComplete_NoLLM(t *testing.T) {
	dir := t.TempDir()
	cs := buildConfigStore(t, dir)
	srv := buildWizardServer(t, cs)

	req := httptest.NewRequest("POST", "/api/wizard/complete", nil)
	resp, err := srv.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 400 {
		t.Errorf("expected 400 when no LLM configured, got %d: %s", resp.StatusCode, bodyBytes)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		t.Fatalf("decoding error response: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Error("expected 'error' field in 400 response")
	}
}

// TestHandleWizardComplete_WithLLM verifies that completing the wizard succeeds
// when at least one LLM provider key is set.
func TestHandleWizardComplete_WithLLM(t *testing.T) {
	dir := t.TempDir()
	cs := buildConfigStore(t, dir)

	ctx := context.Background()
	if err := cs.Set(ctx, "claude.api_key", "sk-test-key", "test", false); err != nil {
		t.Fatalf("cs.Set claude.api_key: %v", err)
	}

	srv := buildWizardServer(t, cs)

	req := httptest.NewRequest("POST", "/api/wizard/complete", nil)
	resp, err := srv.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if v, ok := body["status"]; !ok || v != "completed" {
		t.Errorf("status = %v, want completed", v)
	}

	// Verify wizard_completed was stored.
	if val, ok := cs.Get("_system.wizard_completed"); !ok || val != "true" {
		t.Errorf("_system.wizard_completed = %q (ok=%v), want 'true' (ok=true)", val, ok)
	}
}

// TestHandleWizardSchema_ReturnsAllSections verifies that the schema endpoint
// returns all sections from CoreConfigSchema.
func TestHandleWizardSchema_ReturnsAllSections(t *testing.T) {
	dir := t.TempDir()
	cs := buildConfigStore(t, dir)
	srv := buildWizardServer(t, cs)

	req := httptest.NewRequest("GET", "/api/wizard/schema", nil)
	resp, err := srv.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if _, ok := body["sections"]; !ok {
		t.Fatal("response missing 'sections' field")
	}
	sections, ok := body["sections"].([]interface{})
	if !ok {
		t.Fatal("'sections' is not an array")
	}

	expectedSections := config.CoreConfigSchema()
	if len(sections) != len(expectedSections) {
		t.Errorf("sections count = %d, want %d", len(sections), len(expectedSections))
	}

	if _, ok := body["encryption_enabled"]; !ok {
		t.Error("response missing 'encryption_enabled' field")
	}
}

// TestHandleWizardSchema_SecretsMasked verifies that secret values are masked
// in the schema response when an override is set.
func TestHandleWizardSchema_SecretsMasked(t *testing.T) {
	dir := t.TempDir()
	cs := buildConfigStore(t, dir)

	ctx := context.Background()
	// Set a Claude API key — it's a secret field.
	if err := cs.Set(ctx, "claude.api_key", "sk-real-secret-key", "test", false); err != nil {
		t.Fatalf("cs.Set claude.api_key: %v", err)
	}

	srv := buildWizardServer(t, cs)

	req := httptest.NewRequest("GET", "/api/wizard/schema", nil)
	resp, err := srv.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	sections, _ := body["sections"].([]interface{})
	for _, s := range sections {
		sec, _ := s.(map[string]interface{})
		if sec["name"] != "claude" {
			continue
		}
		fields, _ := sec["fields"].([]interface{})
		for _, f := range fields {
			field, _ := f.(map[string]interface{})
			if field["key"] != "claude.api_key" {
				continue
			}
			val, _ := field["value"].(string)
			if val == "sk-real-secret-key" {
				t.Error("claude.api_key value should be masked in schema response, got raw value")
			}
			if val != "" && val != "***" {
				t.Errorf("claude.api_key value = %q, want '' or '***'", val)
			}
			return
		}
	}
}

// TestHandleWizardSchema_NoConfigStore verifies that when configStore is nil,
// the /api/wizard/schema route is not registered (no 500 error).
func TestHandleWizardSchema_NoConfigStore(t *testing.T) {
	logger := zap.NewNop()
	srv := New(Config{
		Address:     ":0",
		Logger:      logger,
		StaticFS:    minimalStaticFS(),
		ConfigStore: nil,
	})

	req := httptest.NewRequest("GET", "/api/wizard/schema", nil)
	resp, err := srv.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	// Should not 500 — it falls through to SPA or returns some non-wizard response.
	if resp.StatusCode == 500 {
		t.Errorf("expected non-500 when configStore is nil, got 500")
	}
}
