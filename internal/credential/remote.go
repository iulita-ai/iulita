package credential

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// ResolvePrivate resolves an encrypted owner-scoped credential directly from the
// repository. It deliberately has no cache, global, environment or config fallback.
func (s *Store) ResolvePrivate(ctx context.Context, owner, name, kind string) (string, error) {
	return s.ResolvePrivateAtVersion(ctx, owner, name, kind, "")
}

// ResolvePrivateAtVersion decrypts only the exact encrypted revision approved.
func (s *Store) ResolvePrivateAtVersion(ctx context.Context, owner, name, kind, version string) (string, error) {
	if owner == "" || !s.EncryptionEnabled() {
		return "", fmt.Errorf("private credential denied")
	}
	c, err := s.repo.GetCredentialByNameAndOwner(ctx, name, owner)
	if err != nil || c == nil || c.OwnerID != owner || c.Scope != domain.CredentialScopeUser || !c.Encrypted || string(c.Type) != kind || (c.ExpiresAt != nil && !c.ExpiresAt.After(time.Now())) {
		return "", fmt.Errorf("private credential denied")
	}
	if version != "" && version != privateVersion(c) {
		return "", fmt.Errorf("private credential revision changed")
	}
	v, de := s.encryptor.Decrypt(c.Value)
	if de != nil || v == "" {
		return "", fmt.Errorf("private credential unavailable")
	}
	return v, nil
}

// SealPrivate encrypts private execution artifacts using the configured vault key.
func (s *Store) SealPrivate(value string) (string, error) {
	if !s.EncryptionEnabled() {
		return "", fmt.Errorf("encryption required")
	}
	return s.encryptor.Encrypt(value)
}

// OpenPrivate decrypts private artifacts using the configured vault key.
func (s *Store) OpenPrivate(value string) (string, error) {
	if !s.EncryptionEnabled() {
		return "", fmt.Errorf("encryption required")
	}
	return s.encryptor.Decrypt(value)
}

// PrivateVersion identifies the encrypted persisted revision without exposing it.
func (s *Store) PrivateVersion(ctx context.Context, owner, name, kind string) (string, error) {
	if owner == "" || !s.EncryptionEnabled() {
		return "", fmt.Errorf("private credential denied")
	}
	c, e := s.repo.GetCredentialByNameAndOwner(ctx, name, owner)
	if e != nil || c == nil || c.OwnerID != owner || c.Scope != domain.CredentialScopeUser || !c.Encrypted || string(c.Type) != kind || (c.ExpiresAt != nil && !c.ExpiresAt.After(time.Now())) {
		return "", fmt.Errorf("private credential denied")
	}
	version := privateVersion(c)
	if version == "" {
		return "", fmt.Errorf("invalid private credential metadata")
	}
	return version, nil
}
func privateVersion(c *domain.Credential) string {
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(append(b, []byte(c.Value)...))
	return hex.EncodeToString(h[:])
}
