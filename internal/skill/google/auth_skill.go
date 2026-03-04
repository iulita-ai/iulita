package google

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iulita-ai/iulita/internal/skill"
)

// AuthConfigStore abstracts config store operations for the auth skill.
type AuthConfigStore interface {
	GetEffective(key string) (string, bool)
	Set(ctx context.Context, key, value, updatedBy string, encrypt bool) error
}

// AuthSkill provides chat-based Google auth management across all channels.
type AuthSkill struct {
	client   *Client
	cfgStore AuthConfigStore // nil = read-only
	dataDir  string          // directory for storing uploaded credential files
}

// NewAuthSkill creates a new Google auth management skill.
func NewAuthSkill(client *Client, cfgStore AuthConfigStore) *AuthSkill {
	return &AuthSkill{client: client, cfgStore: cfgStore}
}

// SetDataDir sets the directory for storing uploaded credential files.
func (s *AuthSkill) SetDataDir(dir string) {
	s.dataDir = dir
}

func (s *AuthSkill) Name() string { return "google_auth" }

func (s *AuthSkill) Description() string {
	return "Manage Google authentication and credentials. " +
		"Use action='status' to check current credential configuration. " +
		"Use action='list_accounts' to see connected Google accounts. " +
		"Use action='upload_credentials' when the user sends a Google credentials JSON file as a document attachment (Telegram). " +
		"Use action='set_credentials_file' to configure a service account or authorized user JSON file by path. " +
		"Use action='set_scopes' to change Google API scope preset (readonly, readwrite, full) or custom JSON array. " +
		"Use action='get_config' to view current Google skill configuration."
}

func (s *AuthSkill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {
			"type": "string",
			"enum": ["status", "list_accounts", "upload_credentials", "set_credentials_file", "set_scopes", "get_config"],
			"description": "Action to perform. Default is 'status'. Use 'upload_credentials' when the user attached a JSON file."
		},
		"value": {
			"type": "string",
			"description": "Value for set_credentials_file (file path) or set_scopes (preset name or JSON array). Not needed for upload_credentials."
		}
	}
}`)
}

func (s *AuthSkill) RequiredCapabilities() []string {
	return []string{"google"}
}

type authInput struct {
	Action string `json:"action"`
	Value  string `json:"value"`
}

func (s *AuthSkill) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in authInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	userID := skill.UserIDFrom(ctx)
	role := skill.UserRoleFrom(ctx)

	switch in.Action {
	case "", "status":
		return s.handleStatus(ctx, userID)
	case "list_accounts":
		return s.handleListAccounts(ctx, userID)
	case "upload_credentials":
		if role != "admin" {
			return "Only admin users can upload credentials.", nil
		}
		return s.handleUploadCredentials(ctx, userID)
	case "set_credentials_file":
		if role != "admin" {
			return "Only admin users can modify credentials configuration.", nil
		}
		return s.handleSetCredentialsFile(ctx, in.Value, userID)
	case "set_scopes":
		if role != "admin" {
			return "Only admin users can modify scope configuration.", nil
		}
		return s.handleSetScopes(ctx, in.Value, userID)
	case "get_config":
		return s.handleGetConfig()
	default:
		return fmt.Sprintf("Unknown action %q. Use: status, list_accounts, upload_credentials, set_credentials_file, set_scopes, get_config.", in.Action), nil
	}
}

func (s *AuthSkill) handleStatus(ctx context.Context, userID string) (string, error) {
	status := s.client.GetStatus(ctx, userID)
	var b strings.Builder
	b.WriteString("Google Credential Status:\n")
	b.WriteString(fmt.Sprintf("  Source: %s\n", status.Source))
	if status.CredentialType != "" {
		b.WriteString(fmt.Sprintf("  Type: %s\n", status.CredentialType))
	}
	if status.FilePath != "" {
		b.WriteString(fmt.Sprintf("  File: %s\n", status.FilePath))
	}
	if status.DBAccounts > 0 {
		b.WriteString(fmt.Sprintf("  DB Accounts: %d\n", status.DBAccounts))
	}
	b.WriteString(fmt.Sprintf("  Scopes: %s\n", status.ActiveScopes))

	if status.Source == "none" {
		b.WriteString("\nNo credentials configured. Options:\n")
		b.WriteString("  1. Set IULITA_GOOGLE_TOKEN env var (quick access token)\n")
		b.WriteString("  2. Set IULITA_GOOGLE_CREDENTIALS_FILE env var (JSON file path)\n")
		b.WriteString("  3. Use set_credentials_file action (service account or authorized user JSON)\n")
		b.WriteString("  4. Connect an account via the dashboard Settings page\n")
		b.WriteString("  5. Set up Application Default Credentials (gcloud auth application-default login)\n")
	}
	return b.String(), nil
}

func (s *AuthSkill) handleUploadCredentials(ctx context.Context, userID string) (string, error) {
	docs := skill.DocumentsFrom(ctx)
	if len(docs) == 0 {
		return "No file attached. Please send a Google credentials JSON file as a document attachment.", nil
	}

	// Find the first JSON document.
	var data []byte
	var filename string
	for _, doc := range docs {
		if doc.MimeType == "application/json" || strings.HasSuffix(doc.Filename, ".json") {
			data = doc.Data
			filename = doc.Filename
			break
		}
	}
	if data == nil {
		return "No JSON file found in attachments. Please send a .json credentials file.", nil
	}

	// Validate it's a Google credentials file.
	credType, err := detectCredentialType(data)
	if err != nil {
		return fmt.Sprintf("File %q is not a valid Google credentials JSON: %v", filename, err), nil
	}

	if s.dataDir == "" {
		return "Data directory not configured. Cannot save uploaded file.", nil
	}

	// Save to data dir with restricted permissions.
	credDir := filepath.Join(s.dataDir, "google")
	if err := os.MkdirAll(credDir, 0700); err != nil {
		return "", fmt.Errorf("creating credentials directory: %w", err)
	}

	destPath := filepath.Join(credDir, "credentials.json")
	if err := os.WriteFile(destPath, data, 0600); err != nil {
		return "", fmt.Errorf("saving credentials file: %w", err)
	}

	// Persist the path in config.
	if s.cfgStore != nil {
		updatedBy := userID
		if updatedBy == "" {
			updatedBy = "system"
		}
		if err := s.cfgStore.Set(ctx, "skills.google.credentials_file", destPath, updatedBy, false); err != nil {
			return "", fmt.Errorf("saving credentials_file config: %w", err)
		}
	}

	// Apply immediately.
	s.client.SetCredentialsFile(destPath)

	return fmt.Sprintf("Credentials file uploaded and saved (type: %s, from: %s).\nStored at: %s\nThis will be used for all users without a connected Google account.", credType, filename, destPath), nil
}

func (s *AuthSkill) handleListAccounts(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "No user context available.", nil
	}
	return s.client.AccountInfo(ctx, userID)
}

func (s *AuthSkill) handleSetCredentialsFile(ctx context.Context, path, userID string) (string, error) {
	if path == "" {
		return "Please provide a file path in the 'value' field.", nil
	}
	if s.cfgStore == nil {
		return "Config store not available. Set the file path manually in config.toml under skills.google.credentials_file.", nil
	}

	// Validate file exists and is readable JSON.
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Cannot read file %q: %v\nPlease check the path and try again.", path, err), nil
	}
	credType, err := detectCredentialType(data)
	if err != nil {
		return fmt.Sprintf("File %q is not a valid Google credentials JSON: %v", path, err), nil
	}

	updatedBy := userID
	if updatedBy == "" {
		updatedBy = "system"
	}

	if err := s.cfgStore.Set(ctx, "skills.google.credentials_file", path, updatedBy, false); err != nil {
		return "", fmt.Errorf("saving credentials_file config: %w", err)
	}

	// Apply immediately.
	s.client.SetCredentialsFile(path)

	return fmt.Sprintf("Credentials file set to: %s (type: %s)\nThis will be used for all users without a connected Google account.", path, credType), nil
}

func (s *AuthSkill) handleSetScopes(ctx context.Context, value, userID string) (string, error) {
	if value == "" {
		return "Please provide a scope preset (readonly, readwrite, full) or a JSON array of scope URLs in the 'value' field.", nil
	}
	if s.cfgStore == nil {
		return "Config store not available. Set scopes manually in config.toml under skills.google.scopes.", nil
	}

	// Validate the input parses.
	scopes := ParseScopesConfig(value)
	if len(scopes) == 0 {
		return "Could not parse scopes. Use a preset name (readonly, readwrite, full) or a JSON array of URLs.", nil
	}

	updatedBy := userID
	if updatedBy == "" {
		updatedBy = "system"
	}

	if err := s.cfgStore.Set(ctx, "skills.google.scopes", value, updatedBy, false); err != nil {
		return "", fmt.Errorf("saving scopes config: %w", err)
	}

	// Apply immediately.
	s.client.SetScopes(scopes)

	display := FormatScopesForDisplay(scopes)
	return fmt.Sprintf("Scopes updated to: %s (%d scope(s))\nNote: existing tokens may need to be re-authorized for new scopes to take effect.", display, len(scopes)), nil
}

func (s *AuthSkill) handleGetConfig() (string, error) {
	var b strings.Builder
	b.WriteString("Google Skill Configuration:\n")

	credFile := s.client.GetCredentialsFile()
	if credFile != "" {
		b.WriteString(fmt.Sprintf("  credentials_file: %s\n", credFile))
	} else {
		b.WriteString("  credentials_file: (not set)\n")
	}

	scopes := s.client.GetConfigScopes()
	display := FormatScopesForDisplay(scopes)
	b.WriteString(fmt.Sprintf("  scopes: %s\n", display))
	b.WriteString(fmt.Sprintf("  scope_count: %d\n", len(scopes)))

	if s.cfgStore != nil {
		if v, ok := s.cfgStore.GetEffective("skills.google.client_id"); ok && v != "" {
			b.WriteString("  client_id: (configured)\n")
		} else {
			b.WriteString("  client_id: (not set)\n")
		}
		if v, ok := s.cfgStore.GetEffective("skills.google.client_secret"); ok && v != "" {
			b.WriteString("  client_secret: (configured)\n")
		} else {
			b.WriteString("  client_secret: (not set)\n")
		}
		if v, ok := s.cfgStore.GetEffective("skills.google.redirect_url"); ok && v != "" {
			b.WriteString(fmt.Sprintf("  redirect_url: %s\n", v))
		}
	}

	return b.String(), nil
}
