package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

const (
	keyringService     = "iulita"
	keyringAccountKey  = "config-encryption-key"
	keyringAccountAPI  = "claude-api-key"
	keyringAccountTG   = "telegram-token"
	keyringAccountJWT  = "jwt-secret"
	encryptionKeyBytes = 32
)

// KeyStore manages secrets with a fallback chain:
// env var → system keyring → file.
type KeyStore struct {
	paths *Paths
}

// NewKeyStore creates a KeyStore with the given paths.
func NewKeyStore(paths *Paths) *KeyStore {
	return &KeyStore{paths: paths}
}

// GetEncryptionKey resolves the encryption key using the priority chain:
// 1. IULITA_CONFIG_KEY env var
// 2. System keyring
// 3. File at ~/.config/iulita/encryption.key
// Returns nil if no key is found (encryption disabled).
func (ks *KeyStore) GetEncryptionKey() ([]byte, error) {
	// 1. Env var
	if keyHex := os.Getenv("IULITA_CONFIG_KEY"); keyHex != "" {
		return hex.DecodeString(keyHex)
	}

	// 2. Keyring
	if val, err := keyring.Get(keyringService, keyringAccountKey); err == nil && val != "" {
		return hex.DecodeString(val)
	}

	// 3. File
	keyFile := ks.paths.EncryptionKeyFile()
	data, err := os.ReadFile(keyFile)
	if err == nil && len(data) > 0 {
		return hex.DecodeString(string(data))
	}

	return nil, nil
}

// EnsureEncryptionKey returns the existing encryption key or generates and stores a new one.
func (ks *KeyStore) EnsureEncryptionKey() ([]byte, error) {
	key, err := ks.GetEncryptionKey()
	if err != nil {
		return nil, err
	}
	if key != nil {
		return key, nil
	}

	// Generate new key
	key = make([]byte, encryptionKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating encryption key: %w", err)
	}

	keyHex := hex.EncodeToString(key)

	// Try keyring first
	if err := keyring.Set(keyringService, keyringAccountKey, keyHex); err == nil {
		return key, nil
	}

	// Fallback: write to file
	if err := ks.paths.EnsureDirs(); err != nil {
		return nil, fmt.Errorf("creating config dir: %w", err)
	}
	keyFile := ks.paths.EncryptionKeyFile()
	if err := os.WriteFile(keyFile, []byte(keyHex), 0600); err != nil {
		return nil, fmt.Errorf("writing encryption key file: %w", err)
	}

	return key, nil
}

// EnsureJWTSecret returns the existing JWT secret or generates and stores a new one.
// Priority: IULITA_JWT_SECRET env → keyring → generate new + save to keyring.
func (ks *KeyStore) EnsureJWTSecret() (string, error) {
	if val := os.Getenv("IULITA_JWT_SECRET"); val != "" {
		return val, nil
	}
	if val, err := keyring.Get(keyringService, keyringAccountJWT); err == nil && val != "" {
		return val, nil
	}
	// Generate new secret.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating JWT secret: %w", err)
	}
	secret := hex.EncodeToString(b)
	// Save to keyring (best effort — if keyring unavailable, return the generated secret anyway).
	_ = keyring.Set(keyringService, keyringAccountJWT, secret)
	return secret, nil
}

// GetSecret resolves a secret by name using the priority chain:
// 1. Environment variable (envVar)
// 2. System keyring (keyring account = account)
// Returns empty string if not found.
func (ks *KeyStore) GetSecret(envVar, account string) string {
	if val := os.Getenv(envVar); val != "" {
		return val
	}
	if val, err := keyring.Get(keyringService, account); err == nil {
		return val
	}
	return ""
}

// SaveSecret stores a secret in the system keyring.
// Returns an error if the keyring is unavailable.
func (ks *KeyStore) SaveSecret(account, value string) error {
	return keyring.Set(keyringService, account, value)
}

// KeyringAvailable returns true if the system keyring is accessible.
func (ks *KeyStore) KeyringAvailable() bool {
	testKey := "iulita-keyring-test"
	err := keyring.Set(keyringService, testKey, "test")
	if err != nil {
		return false
	}
	_ = keyring.Delete(keyringService, testKey)
	return true
}
