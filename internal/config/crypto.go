package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// Encryptor provides AES-256-GCM encryption/decryption for config values.
type Encryptor struct {
	gcm cipher.AEAD
}

// NewEncryptor creates a new Encryptor with the given 32-byte key.
func NewEncryptor(key []byte) (*Encryptor, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	return &Encryptor{gcm: gcm}, nil
}

// NewEncryptorFromEnv creates an Encryptor using the key from the given env var.
// Returns nil (no error) if the env var is empty — encryption is optional.
func NewEncryptorFromEnv(envVar string) (*Encryptor, error) {
	if envVar == "" {
		envVar = "IULITA_CONFIG_KEY"
	}
	keyHex := os.Getenv(envVar)
	if keyHex == "" {
		return nil, nil
	}
	keyHex = strings.TrimSpace(keyHex)
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("decoding %s: must be hex-encoded: %w", envVar, err)
	}
	return NewEncryptor(key)
}

// EncryptionEnabled returns true — an Encryptor always has encryption enabled.
func (e *Encryptor) EncryptionEnabled() bool { return true }

// Encrypt encrypts plaintext and returns a base64-encoded ciphertext.
func (e *Encryptor) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	ciphertext := e.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decodes a base64-encoded ciphertext and returns the plaintext.
func (e *Encryptor) Decrypt(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decoding base64: %w", err)
	}
	nonceSize := e.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	plaintext, err := e.gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypting: %w", err)
	}
	return string(plaintext), nil
}
