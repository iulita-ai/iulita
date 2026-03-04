package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"golang.org/x/oauth2"
	googleoauth "golang.org/x/oauth2/google"
)

// ErrNoCredentials is returned when no Google credentials are found in any source.
var ErrNoCredentials = errors.New("no Google credentials configured — connect an account in Settings or set IULITA_GOOGLE_CREDENTIALS_FILE")

// CredentialSource identifies how a token was obtained.
type CredentialSource string

const (
	SourceEnvToken   CredentialSource = "env_token"
	SourceEnvFile    CredentialSource = "env_credentials_file"
	SourceConfigFile CredentialSource = "config_credentials_file"
	SourceDBAccount  CredentialSource = "db_account"
	SourceADC        CredentialSource = "adc"
)

// ResolveResult carries the resolved oauth2.TokenSource and its origin.
type ResolveResult struct {
	Source   CredentialSource
	TokenSrc oauth2.TokenSource
	Email    string // empty for non-DB sources
}

// credentialFileJSON is the minimal JSON structure for auto-detection.
type credentialFileJSON struct {
	Type         string `json:"type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

// ResolveCredentials walks the credential priority chain:
//  1. IULITA_GOOGLE_TOKEN env var (raw access token)
//  2. IULITA_GOOGLE_CREDENTIALS_FILE env var (JSON file path)
//  3. configFilePath (from google.credentials_file config key)
//  4. DB accounts (per-user, existing flow)
//  5. ADC fallback (GOOGLE_APPLICATION_CREDENTIALS, gcloud well-known path)
//
// Steps 1-3 and 5 are global (not per-user). Step 4 is per-user.
// If userID is empty, DB lookup is skipped.
func ResolveCredentials(
	ctx context.Context,
	userID, accountAlias string,
	oauthConfig *oauth2.Config,
	store TokenStore,
	crypto CryptoProvider,
	configFilePath string,
	scopes []string,
	logger *zap.Logger,
) (*ResolveResult, error) {
	// 1. Direct access token from env var.
	if token := os.Getenv("IULITA_GOOGLE_TOKEN"); token != "" {
		logger.Debug("using google token from IULITA_GOOGLE_TOKEN env var")
		return &ResolveResult{
			Source:   SourceEnvToken,
			TokenSrc: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"}),
		}, nil
	}

	// 2. Credentials file from env var.
	if envFile := os.Getenv("IULITA_GOOGLE_CREDENTIALS_FILE"); envFile != "" {
		ts, err := tokenSourceFromCredentialsFile(ctx, envFile, scopes, logger)
		if err != nil {
			return nil, fmt.Errorf("IULITA_GOOGLE_CREDENTIALS_FILE (%s): %w", envFile, err)
		}
		logger.Debug("using google credentials from IULITA_GOOGLE_CREDENTIALS_FILE", zap.String("path", envFile))
		return &ResolveResult{Source: SourceEnvFile, TokenSrc: ts}, nil
	}

	// 3. Credentials file from config key.
	if configFilePath != "" {
		ts, err := tokenSourceFromCredentialsFile(ctx, configFilePath, scopes, logger)
		if err != nil {
			return nil, fmt.Errorf("google.credentials_file (%s): %w", configFilePath, err)
		}
		logger.Debug("using google credentials from config file", zap.String("path", configFilePath))
		return &ResolveResult{Source: SourceConfigFile, TokenSrc: ts}, nil
	}

	// 4. DB accounts (per-user).
	if userID != "" && store != nil {
		// This is handled by the caller (Client.resolveTokenSource) which
		// already has the full DB lookup + persistingTokenSource logic.
		// Return nil to signal "try DB path".
		return nil, nil
	}

	// 5. ADC fallback.
	ts, err := tokenSourceFromADC(ctx, scopes)
	if err == nil {
		logger.Debug("using google Application Default Credentials")
		return &ResolveResult{Source: SourceADC, TokenSrc: ts}, nil
	}

	return nil, ErrNoCredentials
}

// detectCredentialType reads the JSON "type" field to determine the credential kind.
// Returns "service_account" or "authorized_user".
func detectCredentialType(data []byte) (string, error) {
	var f credentialFileJSON
	if err := json.Unmarshal(data, &f); err != nil {
		return "", fmt.Errorf("parsing credentials JSON: %w", err)
	}
	switch f.Type {
	case "service_account":
		return "service_account", nil
	case "authorized_user", "":
		return "authorized_user", nil
	default:
		return "", fmt.Errorf("unknown credential type %q", f.Type)
	}
}

// tokenSourceFromCredentialsFile reads a JSON file and creates the appropriate TokenSource.
func tokenSourceFromCredentialsFile(ctx context.Context, path string, scopes []string, logger *zap.Logger) (oauth2.TokenSource, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("reading credentials file: %w", err)
	}

	credType, err := detectCredentialType(data)
	if err != nil {
		return nil, err
	}

	logger.Info("detected google credential type", zap.String("type", credType), zap.String("path", absPath))

	switch credType {
	case "service_account":
		return tokenSourceFromServiceAccount(ctx, data, scopes)
	case "authorized_user":
		return tokenSourceFromAuthorizedUser(ctx, data, scopes)
	default:
		return nil, fmt.Errorf("unsupported credential type: %s", credType)
	}
}

// tokenSourceFromServiceAccount creates a TokenSource from service account JSON.
func tokenSourceFromServiceAccount(ctx context.Context, data []byte, scopes []string) (oauth2.TokenSource, error) {
	creds, err := googleoauth.CredentialsFromJSONWithParams(ctx, data, googleoauth.CredentialsParams{Scopes: scopes})
	if err != nil {
		return nil, fmt.Errorf("parsing service account credentials: %w", err)
	}
	return creds.TokenSource, nil
}

// tokenSourceFromAuthorizedUser creates a TokenSource from authorized_user JSON.
func tokenSourceFromAuthorizedUser(ctx context.Context, data []byte, scopes []string) (oauth2.TokenSource, error) {
	creds, err := googleoauth.CredentialsFromJSONWithParams(ctx, data, googleoauth.CredentialsParams{Scopes: scopes})
	if err != nil {
		return nil, fmt.Errorf("parsing authorized_user credentials: %w", err)
	}
	return creds.TokenSource, nil
}

// tokenSourceFromADC attempts to find Application Default Credentials.
func tokenSourceFromADC(ctx context.Context, scopes []string) (oauth2.TokenSource, error) {
	creds, err := googleoauth.FindDefaultCredentials(ctx, scopes...)
	if err != nil {
		return nil, err
	}
	return creds.TokenSource, nil
}

// CredentialStatus describes the current state of Google credential resolution.
type CredentialStatus struct {
	Source         string `json:"source"`          // "env_token", "env_file", "config_file", "db_account", "adc", "none"
	CredentialType string `json:"credential_type"` // "service_account", "authorized_user", "access_token", ""
	FilePath       string `json:"file_path"`       // for file-based sources
	DBAccounts     int    `json:"db_accounts"`     // number of DB-stored accounts
	ActiveScopes   string `json:"active_scopes"`   // scope preset name or "custom"
}

// GetCredentialStatus returns diagnostic info about current credential configuration.
func GetCredentialStatus(
	ctx context.Context,
	userID string,
	store TokenStore,
	configFilePath string,
	scopes []string,
) CredentialStatus {
	status := CredentialStatus{
		Source:       "none",
		ActiveScopes: FormatScopesForDisplay(scopes),
	}

	// Check env token.
	if os.Getenv("IULITA_GOOGLE_TOKEN") != "" {
		status.Source = "env_token"
		status.CredentialType = "access_token"
		return status
	}

	// Check env file.
	if envFile := os.Getenv("IULITA_GOOGLE_CREDENTIALS_FILE"); envFile != "" {
		status.Source = "env_credentials_file"
		status.FilePath = envFile
		if data, err := os.ReadFile(envFile); err == nil {
			status.CredentialType, _ = detectCredentialType(data)
		}
		return status
	}

	// Check config file.
	if configFilePath != "" {
		status.Source = "config_credentials_file"
		status.FilePath = configFilePath
		if data, err := os.ReadFile(configFilePath); err == nil {
			status.CredentialType, _ = detectCredentialType(data)
		}
		return status
	}

	// Check DB accounts.
	if userID != "" && store != nil {
		accounts, err := store.ListGoogleAccounts(ctx, userID)
		if err == nil {
			status.DBAccounts = len(accounts)
			if len(accounts) > 0 {
				status.Source = "db_account"
				status.CredentialType = "authorized_user"
				return status
			}
		}
	}

	// Check ADC.
	if _, err := googleoauth.FindDefaultCredentials(ctx); err == nil {
		status.Source = "adc"
		return status
	}

	return status
}
