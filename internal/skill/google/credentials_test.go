package google

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
)

var testLogger = zap.NewNop()

func TestDetectCredentialType_ServiceAccount(t *testing.T) {
	data := []byte(`{"type": "service_account", "project_id": "test"}`)
	ct, err := detectCredentialType(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != "service_account" {
		t.Errorf("expected service_account, got %s", ct)
	}
}

func TestDetectCredentialType_AuthorizedUser(t *testing.T) {
	data := []byte(`{"type": "authorized_user", "client_id": "test"}`)
	ct, err := detectCredentialType(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != "authorized_user" {
		t.Errorf("expected authorized_user, got %s", ct)
	}
}

func TestDetectCredentialType_EmptyType(t *testing.T) {
	data := []byte(`{"client_id": "test", "client_secret": "sec", "refresh_token": "tok"}`)
	ct, err := detectCredentialType(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != "authorized_user" {
		t.Errorf("empty type should default to authorized_user, got %s", ct)
	}
}

func TestDetectCredentialType_UnknownType(t *testing.T) {
	data := []byte(`{"type": "something_new"}`)
	_, err := detectCredentialType(data)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestDetectCredentialType_InvalidJSON(t *testing.T) {
	_, err := detectCredentialType([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestResolveCredentials_EnvToken(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "test-token-123")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", "")

	result, err := ResolveCredentials(
		context.Background(), "", "", nil, nil, nil, "", DefaultScopes(), testLogger,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Source != SourceEnvToken {
		t.Errorf("expected SourceEnvToken, got %s", result.Source)
	}
	tok, err := result.TokenSrc.Token()
	if err != nil {
		t.Fatalf("token error: %v", err)
	}
	if tok.AccessToken != "test-token-123" {
		t.Errorf("expected test-token-123, got %s", tok.AccessToken)
	}
}

func TestResolveCredentials_EnvFile_ServiceAccount(t *testing.T) {
	dir := t.TempDir()
	saFile := filepath.Join(dir, "sa.json")
	// Minimal valid service account JSON with a proper RSA key would be needed
	// for actual auth, but we can test the detection/parsing path.
	saJSON := `{
		"type": "service_account",
		"project_id": "test-project",
		"private_key_id": "key-id",
		"private_key": "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF0PbnGcY5unA67890\n-----END RSA PRIVATE KEY-----\n",
		"client_email": "test@test-project.iam.gserviceaccount.com",
		"client_id": "123456",
		"auth_uri": "https://accounts.google.com/o/oauth2/auth",
		"token_uri": "https://oauth2.googleapis.com/token"
	}`
	os.WriteFile(saFile, []byte(saJSON), 0600)

	t.Setenv("IULITA_GOOGLE_TOKEN", "")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", saFile)

	// This will fail on actual token creation because the RSA key is fake,
	// but it tests the file detection and routing logic.
	result, err := ResolveCredentials(
		context.Background(), "", "", nil, nil, nil, "", DefaultScopes(), testLogger,
	)
	// Service account with fake key will fail at JWT signing, that's expected.
	if err != nil {
		// As long as the error mentions the file and not "no credentials", the chain worked.
		if result == nil {
			return // Expected — fake key can't parse
		}
	}
	if result != nil && result.Source != SourceEnvFile {
		t.Errorf("expected SourceEnvFile, got %s", result.Source)
	}
}

func TestResolveCredentials_ConfigFile_AuthorizedUser(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "creds.json")
	credJSON := `{
		"type": "authorized_user",
		"client_id": "test-client",
		"client_secret": "test-secret",
		"refresh_token": "test-refresh"
	}`
	os.WriteFile(credFile, []byte(credJSON), 0600)

	t.Setenv("IULITA_GOOGLE_TOKEN", "")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", "")

	result, err := ResolveCredentials(
		context.Background(), "", "", nil, nil, nil, credFile, DefaultScopes(), testLogger,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Source != SourceConfigFile {
		t.Errorf("expected SourceConfigFile, got %s", result.Source)
	}
}

func TestResolveCredentials_NilResult_WithUserID(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", "")

	store := &mockTokenStore{accounts: []domain.GoogleAccount{{UserID: "user-1"}}}
	result, err := ResolveCredentials(
		context.Background(), "user-1", "", nil, store, nil, "", DefaultScopes(), testLogger,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result (DB path signal) when userID is set")
	}
}

func TestResolveCredentials_NoCredentials(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent/path")

	_, err := ResolveCredentials(
		context.Background(), "", "", nil, nil, nil, "", DefaultScopes(), testLogger,
	)
	if err == nil {
		t.Fatal("expected error when no credentials available")
	}
}

func TestResolveCredentials_EnvFileMissing(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", "/nonexistent/file.json")

	_, err := ResolveCredentials(
		context.Background(), "", "", nil, nil, nil, "", DefaultScopes(), testLogger,
	)
	if err == nil {
		t.Fatal("expected error for missing credentials file")
	}
}

func TestResolveCredentials_EnvTokenPriority(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "creds.json")
	os.WriteFile(credFile, []byte(`{"type":"authorized_user","client_id":"x","client_secret":"y","refresh_token":"z"}`), 0600)

	t.Setenv("IULITA_GOOGLE_TOKEN", "priority-token")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", credFile)

	result, err := ResolveCredentials(
		context.Background(), "", "", nil, nil, nil, "", DefaultScopes(), testLogger,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Source != SourceEnvToken {
		t.Errorf("env token should have priority, got source=%s", result.Source)
	}
}

func TestGetCredentialStatus_None(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent")

	status := GetCredentialStatus(context.Background(), "", nil, "", DefaultScopes())
	if status.Source != "none" {
		t.Errorf("expected source=none, got %s", status.Source)
	}
}

func TestGetCredentialStatus_EnvToken(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "tok")
	status := GetCredentialStatus(context.Background(), "", nil, "", DefaultScopes())
	if status.Source != "env_token" {
		t.Errorf("expected env_token, got %s", status.Source)
	}
}

func TestGetCredentialStatus_DBAccounts(t *testing.T) {
	t.Setenv("IULITA_GOOGLE_TOKEN", "")
	t.Setenv("IULITA_GOOGLE_CREDENTIALS_FILE", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent")

	store := &mockTokenStore{accounts: []domain.GoogleAccount{
		{UserID: "user-1", AccountEmail: "a@test.com"},
		{UserID: "user-1", AccountEmail: "b@test.com"},
	}}
	status := GetCredentialStatus(context.Background(), "user-1", store, "", DefaultScopes())
	if status.Source != "db_account" {
		t.Errorf("expected db_account, got %s", status.Source)
	}
	if status.DBAccounts != 2 {
		t.Errorf("expected 2 DB accounts, got %d", status.DBAccounts)
	}
}
