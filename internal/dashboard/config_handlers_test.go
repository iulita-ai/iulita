package dashboard

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func putConfig(t *testing.T, srv *Server, key, value string) (status int, out map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"value": value})
	req := httptest.NewRequest(http.MethodPut, "/api/config/"+key, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

// TestHandleSetConfig_RestartOnlyKeyPersists pins the bug fix: restart-only keys
// (proxy.url) must be saved via SetForImport (not rejected by Set) and the
// response must flag restart_required.
func TestHandleSetConfig_RestartOnlyKeyPersists(t *testing.T) {
	cs := buildConfigStore(t, t.TempDir())
	srv := buildWizardServer(t, cs) // configStore set, no auth → config routes open

	code, out := putConfig(t, srv, "proxy.url", "http://proxy.local:3128")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", code, out)
	}
	if out["restart_required"] != true {
		t.Errorf("expected restart_required=true, got %v", out["restart_required"])
	}
	if v, ok := cs.Get("proxy.url"); !ok || v != "http://proxy.local:3128" {
		t.Errorf("proxy.url not persisted: %q ok=%v", v, ok)
	}
}

// TestHandleSetConfig_NormalKeyHotReloadable verifies a coreKeys key saves via
// Set and reports restart_required=false.
func TestHandleSetConfig_NormalKeyHotReloadable(t *testing.T) {
	cs := buildConfigStore(t, t.TempDir())
	srv := buildWizardServer(t, cs)

	code, out := putConfig(t, srv, "claude.model", "claude-opus-4-6")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", code, out)
	}
	if out["restart_required"] != false {
		t.Errorf("expected restart_required=false, got %v", out["restart_required"])
	}
	if v, ok := cs.Get("claude.model"); !ok || v != "claude-opus-4-6" {
		t.Errorf("claude.model not persisted: %q ok=%v", v, ok)
	}
}

// TestHandleSetConfig_UnknownKeyRejected ensures unknown keys still 400.
func TestHandleSetConfig_UnknownKeyRejected(t *testing.T) {
	cs := buildConfigStore(t, t.TempDir())
	srv := buildWizardServer(t, cs)

	code, _ := putConfig(t, srv, "bogus.key", "x")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown key, got %d", code)
	}
	if _, ok := cs.Get("bogus.key"); ok {
		t.Error("unknown key must not be persisted")
	}
}
