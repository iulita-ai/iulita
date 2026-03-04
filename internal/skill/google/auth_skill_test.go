package google

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
)

// mockAuthConfigStore implements AuthConfigStore for testing.
type mockAuthConfigStore struct {
	values map[string]string
	sets   []configSetCall
}

type configSetCall struct {
	Key, Value, UpdatedBy string
	Encrypt               bool
}

func (m *mockAuthConfigStore) GetEffective(key string) (string, bool) {
	v, ok := m.values[key]
	return v, ok
}

func (m *mockAuthConfigStore) Set(_ context.Context, key, value, updatedBy string, encrypt bool) error {
	m.sets = append(m.sets, configSetCall{key, value, updatedBy, encrypt})
	if m.values == nil {
		m.values = make(map[string]string)
	}
	m.values[key] = value
	return nil
}

func newTestAuthSkill(store TokenStore, cfgStore AuthConfigStore) *AuthSkill {
	client := NewClientWithOptions(ClientOptions{
		Store:  store,
		Logger: zap.NewNop(),
	})
	return NewAuthSkill(client, cfgStore)
}

func TestAuthSkill_Metadata(t *testing.T) {
	s := newTestAuthSkill(nil, nil)
	if s.Name() != "google_auth" {
		t.Errorf("expected google_auth, got %s", s.Name())
	}
	if s.Description() == "" {
		t.Error("expected non-empty description")
	}
	schema := s.InputSchema()
	if len(schema) == 0 {
		t.Error("expected non-empty schema")
	}
	caps := s.RequiredCapabilities()
	if len(caps) != 1 || caps[0] != "google" {
		t.Errorf("expected [google], got %v", caps)
	}
}

func TestAuthSkill_Status_NoCredentials(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent")

	s := newTestAuthSkill(nil, nil)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"status"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "none") {
		t.Errorf("expected 'none' in status, got: %s", result)
	}
	if !containsStr(result, "No credentials configured") {
		t.Errorf("expected help text, got: %s", result)
	}
}

func TestAuthSkill_Status_EnvToken(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "test-tok")

	s := newTestAuthSkill(nil, nil)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"status"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "env_token") {
		t.Errorf("expected 'env_token' in status, got: %s", result)
	}
}

func TestAuthSkill_Status_DefaultAction(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "tok")

	s := newTestAuthSkill(nil, nil)
	// Empty action should default to status.
	result, err := s.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "env_token") {
		t.Errorf("expected status output, got: %s", result)
	}
}

func TestAuthSkill_ListAccounts_NoUser(t *testing.T) {
	s := newTestAuthSkill(nil, nil)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"list_accounts"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "No user context") {
		t.Errorf("expected no user message, got: %s", result)
	}
}

func TestAuthSkill_ListAccounts_WithAccounts(t *testing.T) {
	store := &mockTokenStore{accounts: []domain.GoogleAccount{
		{ID: 1, UserID: "u1", AccountEmail: "a@g.com", AccountAlias: "personal", IsDefault: true},
		{ID: 2, UserID: "u1", AccountEmail: "b@g.com"},
	}}
	s := newTestAuthSkill(store, nil)

	ctx := skill.WithUserID(context.Background(), "u1")
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"list_accounts"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "a@g.com") || !containsStr(result, "b@g.com") {
		t.Errorf("expected both accounts, got: %s", result)
	}
}

func TestAuthSkill_SetCredentialsFile_NotAdmin(t *testing.T) {
	s := newTestAuthSkill(nil, &mockAuthConfigStore{})

	ctx := skill.WithUserRole(context.Background(), "user")
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"set_credentials_file","value":"/path/to/creds.json"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "Only admin") {
		t.Errorf("expected admin-only message, got: %s", result)
	}
}

func TestAuthSkill_SetCredentialsFile_Admin(t *testing.T) {
	// Create a valid credentials file.
	dir := t.TempDir()
	credFile := filepath.Join(dir, "sa.json")
	os.WriteFile(credFile, []byte(`{"type":"service_account","project_id":"test"}`), 0600)

	cfgStore := &mockAuthConfigStore{}
	s := newTestAuthSkill(nil, cfgStore)

	ctx := skill.WithUserID(skill.WithUserRole(context.Background(), "admin"), "admin-1")
	input := `{"action":"set_credentials_file","value":"` + credFile + `"}`
	result, err := s.Execute(ctx, json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, credFile) {
		t.Errorf("expected path in result, got: %s", result)
	}
	if !containsStr(result, "service_account") {
		t.Errorf("expected credential type in result, got: %s", result)
	}
	if len(cfgStore.sets) != 1 {
		t.Fatalf("expected 1 config set call, got %d", len(cfgStore.sets))
	}
	if cfgStore.sets[0].Key != "skills.google.credentials_file" {
		t.Errorf("unexpected key: %s", cfgStore.sets[0].Key)
	}
	if cfgStore.sets[0].Value != credFile {
		t.Errorf("unexpected value: %s", cfgStore.sets[0].Value)
	}
	// Verify client was updated.
	if s.client.GetCredentialsFile() != credFile {
		t.Errorf("client not updated: %s", s.client.GetCredentialsFile())
	}
}

func TestAuthSkill_SetCredentialsFile_NonexistentFile(t *testing.T) {
	cfgStore := &mockAuthConfigStore{}
	s := newTestAuthSkill(nil, cfgStore)

	ctx := skill.WithUserRole(context.Background(), "admin")
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"set_credentials_file","value":"/nonexistent/path.json"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "Cannot read file") {
		t.Errorf("expected file error message, got: %s", result)
	}
	if len(cfgStore.sets) != 0 {
		t.Error("should not have saved config for nonexistent file")
	}
}

func TestAuthSkill_SetCredentialsFile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	badFile := filepath.Join(dir, "bad.json")
	os.WriteFile(badFile, []byte(`not json`), 0600)

	cfgStore := &mockAuthConfigStore{}
	s := newTestAuthSkill(nil, cfgStore)

	ctx := skill.WithUserRole(context.Background(), "admin")
	input := `{"action":"set_credentials_file","value":"` + badFile + `"}`
	result, err := s.Execute(ctx, json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "not a valid Google credentials JSON") {
		t.Errorf("expected invalid JSON message, got: %s", result)
	}
	if len(cfgStore.sets) != 0 {
		t.Error("should not have saved config for invalid JSON")
	}
}

func TestAuthSkill_SetCredentialsFile_EmptyValue(t *testing.T) {
	s := newTestAuthSkill(nil, &mockAuthConfigStore{})

	ctx := skill.WithUserRole(context.Background(), "admin")
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"set_credentials_file","value":""}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "file path") {
		t.Errorf("expected prompt for file path, got: %s", result)
	}
}

func TestAuthSkill_SetCredentialsFile_NoCfgStore(t *testing.T) {
	s := newTestAuthSkill(nil, nil)

	ctx := skill.WithUserRole(context.Background(), "admin")
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"set_credentials_file","value":"/path.json"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "Config store not available") {
		t.Errorf("expected config store unavailable message, got: %s", result)
	}
}

func TestAuthSkill_SetScopes_NotAdmin(t *testing.T) {
	s := newTestAuthSkill(nil, &mockAuthConfigStore{})

	ctx := skill.WithUserRole(context.Background(), "user")
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"set_scopes","value":"readwrite"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "Only admin") {
		t.Errorf("expected admin-only message, got: %s", result)
	}
}

func TestAuthSkill_SetScopes_Preset(t *testing.T) {
	cfgStore := &mockAuthConfigStore{}
	s := newTestAuthSkill(nil, cfgStore)

	ctx := skill.WithUserID(skill.WithUserRole(context.Background(), "admin"), "admin-1")
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"set_scopes","value":"readwrite"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "readwrite") {
		t.Errorf("expected 'readwrite' in result, got: %s", result)
	}
	if len(cfgStore.sets) != 1 {
		t.Fatalf("expected 1 set call, got %d", len(cfgStore.sets))
	}
	if cfgStore.sets[0].Key != "skills.google.scopes" {
		t.Errorf("unexpected key: %s", cfgStore.sets[0].Key)
	}
}

func TestAuthSkill_SetScopes_EmptyValue(t *testing.T) {
	s := newTestAuthSkill(nil, &mockAuthConfigStore{})

	ctx := skill.WithUserRole(context.Background(), "admin")
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"set_scopes","value":""}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "scope preset") {
		t.Errorf("expected prompt for scopes, got: %s", result)
	}
}

func TestAuthSkill_GetConfig(t *testing.T) {
	cfgStore := &mockAuthConfigStore{values: map[string]string{
		"skills.google.client_id":     "my-client-id",
		"skills.google.client_secret": "secret",
		"skills.google.redirect_url":  "http://localhost:8080/callback",
	}}
	s := newTestAuthSkill(nil, cfgStore)

	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"get_config"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "client_id: (configured)") {
		t.Errorf("expected client_id configured, got: %s", result)
	}
	if !containsStr(result, "client_secret: (configured)") {
		t.Errorf("expected client_secret configured, got: %s", result)
	}
	if !containsStr(result, "http://localhost:8080/callback") {
		t.Errorf("expected redirect_url in output, got: %s", result)
	}
}

func TestAuthSkill_GetConfig_NoCfgStore(t *testing.T) {
	s := newTestAuthSkill(nil, nil)

	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"get_config"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "credentials_file: (not set)") {
		t.Errorf("expected default output, got: %s", result)
	}
}

func TestAuthSkill_UnknownAction(t *testing.T) {
	s := newTestAuthSkill(nil, nil)

	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"bogus"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "Unknown action") {
		t.Errorf("expected unknown action message, got: %s", result)
	}
}

func TestAuthSkill_InvalidJSON(t *testing.T) {
	s := newTestAuthSkill(nil, nil)

	_, err := s.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// --- upload_credentials tests ---

func TestAuthSkill_UploadCredentials_NotAdmin(t *testing.T) {
	s := newTestAuthSkill(nil, &mockAuthConfigStore{})

	ctx := skill.WithUserRole(context.Background(), "user")
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"upload_credentials"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "Only admin") {
		t.Errorf("expected admin-only message, got: %s", result)
	}
}

func TestAuthSkill_UploadCredentials_NoDocument(t *testing.T) {
	s := newTestAuthSkill(nil, &mockAuthConfigStore{})

	ctx := skill.WithUserRole(context.Background(), "admin")
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"upload_credentials"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "No file attached") {
		t.Errorf("expected no-file message, got: %s", result)
	}
}

func TestAuthSkill_UploadCredentials_NoJSONDocument(t *testing.T) {
	s := newTestAuthSkill(nil, &mockAuthConfigStore{})

	ctx := skill.WithUserRole(context.Background(), "admin")
	ctx = skill.WithDocuments(ctx, []skill.DocumentAttachment{
		{Data: []byte("hello"), MimeType: "text/plain", Filename: "readme.txt"},
	})
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"upload_credentials"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "No JSON file found") {
		t.Errorf("expected no-json message, got: %s", result)
	}
}

func TestAuthSkill_UploadCredentials_InvalidJSON(t *testing.T) {
	s := newTestAuthSkill(nil, &mockAuthConfigStore{})
	s.SetDataDir(t.TempDir())

	ctx := skill.WithUserRole(context.Background(), "admin")
	ctx = skill.WithDocuments(ctx, []skill.DocumentAttachment{
		{Data: []byte("not json at all"), MimeType: "application/json", Filename: "creds.json"},
	})
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"upload_credentials"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "not a valid Google credentials JSON") {
		t.Errorf("expected invalid JSON message, got: %s", result)
	}
}

func TestAuthSkill_UploadCredentials_NoDataDir(t *testing.T) {
	s := newTestAuthSkill(nil, &mockAuthConfigStore{})
	// dataDir is empty by default

	ctx := skill.WithUserRole(context.Background(), "admin")
	ctx = skill.WithDocuments(ctx, []skill.DocumentAttachment{
		{Data: []byte(`{"type":"service_account","project_id":"test"}`), MimeType: "application/json", Filename: "sa.json"},
	})
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"upload_credentials"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "Data directory not configured") {
		t.Errorf("expected data dir error, got: %s", result)
	}
}

func TestAuthSkill_UploadCredentials_ValidServiceAccount(t *testing.T) {
	cfgStore := &mockAuthConfigStore{}
	s := newTestAuthSkill(nil, cfgStore)
	dataDir := t.TempDir()
	s.SetDataDir(dataDir)

	saJSON := `{"type":"service_account","project_id":"my-project","client_email":"sa@my-project.iam.gserviceaccount.com","private_key":"-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----\n"}`

	ctx := skill.WithUserID(skill.WithUserRole(context.Background(), "admin"), "admin-1")
	ctx = skill.WithDocuments(ctx, []skill.DocumentAttachment{
		{Data: []byte(saJSON), MimeType: "application/json", Filename: "service-account.json"},
	})
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"upload_credentials"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "service_account") {
		t.Errorf("expected credential type in result, got: %s", result)
	}
	if !containsStr(result, "service-account.json") {
		t.Errorf("expected original filename in result, got: %s", result)
	}

	// Verify file was saved.
	destPath := filepath.Join(dataDir, "google", "credentials.json")
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("credentials file not saved: %v", err)
	}
	if string(data) != saJSON {
		t.Errorf("saved data mismatch")
	}

	// Verify file permissions (0600).
	info, _ := os.Stat(destPath)
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected 0600 permissions, got %o", perm)
	}

	// Verify config store was updated.
	if len(cfgStore.sets) != 1 {
		t.Fatalf("expected 1 config set call, got %d", len(cfgStore.sets))
	}
	if cfgStore.sets[0].Key != "skills.google.credentials_file" {
		t.Errorf("unexpected key: %s", cfgStore.sets[0].Key)
	}
	if cfgStore.sets[0].Value != destPath {
		t.Errorf("unexpected value: %s", cfgStore.sets[0].Value)
	}
	if cfgStore.sets[0].UpdatedBy != "admin-1" {
		t.Errorf("unexpected updatedBy: %s", cfgStore.sets[0].UpdatedBy)
	}

	// Verify client was updated.
	if s.client.GetCredentialsFile() != destPath {
		t.Errorf("client not updated: %s", s.client.GetCredentialsFile())
	}
}

func TestAuthSkill_UploadCredentials_PicksJSONFromMultipleAttachments(t *testing.T) {
	cfgStore := &mockAuthConfigStore{}
	s := newTestAuthSkill(nil, cfgStore)
	s.SetDataDir(t.TempDir())

	saJSON := `{"type":"service_account","project_id":"test"}`

	ctx := skill.WithUserRole(context.Background(), "admin")
	ctx = skill.WithDocuments(ctx, []skill.DocumentAttachment{
		{Data: []byte("image data"), MimeType: "image/png", Filename: "photo.png"},
		{Data: []byte(saJSON), MimeType: "application/json", Filename: "creds.json"},
		{Data: []byte("more stuff"), MimeType: "text/plain", Filename: "notes.txt"},
	})
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"upload_credentials"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsStr(result, "service_account") {
		t.Errorf("expected credential type, got: %s", result)
	}
	if !containsStr(result, "creds.json") {
		t.Errorf("expected filename, got: %s", result)
	}
}

func TestAuthSkill_UploadCredentials_NoCfgStore(t *testing.T) {
	s := newTestAuthSkill(nil, nil) // no cfgStore
	s.SetDataDir(t.TempDir())

	saJSON := `{"type":"service_account","project_id":"test"}`

	ctx := skill.WithUserRole(context.Background(), "admin")
	ctx = skill.WithDocuments(ctx, []skill.DocumentAttachment{
		{Data: []byte(saJSON), MimeType: "application/json", Filename: "sa.json"},
	})
	result, err := s.Execute(ctx, json.RawMessage(`{"action":"upload_credentials"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should still succeed — file saved, just no config persistence.
	if !containsStr(result, "service_account") {
		t.Errorf("expected success even without config store, got: %s", result)
	}
}
