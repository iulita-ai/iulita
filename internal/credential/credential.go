package credential

import (
	"context"
	"errors"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// ErrNotFound is returned when a credential does not exist.
var ErrNotFound = errors.New("credential not found")

// Repository is the storage interface for credentials.
// Defined locally to avoid import cycles (credential -> storage -> credential).
type Repository interface {
	// Credential CRUD
	SaveCredential(ctx context.Context, c *domain.Credential) error
	GetCredential(ctx context.Context, id int64) (*domain.Credential, error)
	GetCredentialByName(ctx context.Context, name string) (*domain.Credential, error)
	GetCredentialByNameAndOwner(ctx context.Context, name, ownerID string) (*domain.Credential, error)
	ListCredentials(ctx context.Context, filter CredentialFilter) ([]domain.Credential, error)
	UpdateCredential(ctx context.Context, c *domain.Credential) error
	DeleteCredential(ctx context.Context, id int64) error

	// Bindings
	SaveCredentialBinding(ctx context.Context, b *domain.CredentialBinding) error
	DeleteCredentialBinding(ctx context.Context, credentialID int64, consumerType, consumerID string) error
	ListCredentialBindings(ctx context.Context, credentialID int64) ([]domain.CredentialBinding, error)
	ListCredentialBindingsByConsumer(ctx context.Context, consumerType, consumerID string) ([]domain.CredentialBinding, error)

	// Audit
	SaveCredentialAudit(ctx context.Context, a *domain.CredentialAudit) error
	ListCredentialAudit(ctx context.Context, credentialID int64, limit int) ([]domain.CredentialAudit, error)
}

// CredentialFilter specifies list query criteria.
//
//nolint:revive // used as credential.CredentialFilter in dashboard/storage
type CredentialFilter struct {
	Scope   domain.CredentialScope
	OwnerID string
	Type    domain.CredentialType
}

// CryptoProvider encrypts and decrypts values.
// Implemented by *config.Encryptor.
type CryptoProvider interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
	EncryptionEnabled() bool
}

// ChangePublisher publishes credential change events.
type ChangePublisher interface {
	PublishCredentialChanged(ctx context.Context, name string)
}

// CredentialProvider is the interface consumed by skills, channelmgr, and config.Store.
//
//nolint:revive // used as credential.CredentialProvider in dashboard/storage
type CredentialProvider interface {
	Resolve(ctx context.Context, name string) (string, error)
	ResolveForUser(ctx context.Context, name, userID string) (string, error)
	IsAvailable(ctx context.Context, name string) bool
}

// ConfigFallback is a read-only view into config.Store for fallback resolution.
// IMPORTANT: Must NOT call GetEffective (which delegates back to credential store),
// only the direct DB-override + base-config path to avoid infinite recursion.
type ConfigFallback interface {
	GetWithoutCredentials(key string) (string, bool)
}

// KeyringProvider abstracts keyring lookups.
type KeyringProvider interface {
	GetSecret(envVar, account string) string
}

// MigrationSource provides decrypted secret key values from the old config store.
type MigrationSource interface {
	// GetSecretKeys returns the names of all registered secret keys.
	GetSecretKeys() []string
	// GetWithoutCredentials returns the raw value for a key (DB override → base config).
	GetWithoutCredentials(key string) (string, bool)
	// DeleteConfigOverride removes a key from config_overrides.
	DeleteConfigOverride(ctx context.Context, key string) error
}

// SetRequest is the input for Store.Set().
type SetRequest struct {
	Name        string
	Type        domain.CredentialType
	Scope       domain.CredentialScope
	OwnerID     string
	Value       string
	Description string
	Tags        []string
	UpdatedBy   string
	ExpiresAt   *time.Time
}

// RotateRequest is the input for Store.Rotate().
type RotateRequest struct {
	ID        int64
	NewValue  string
	UpdatedBy string
}

// CredentialView is the safe API-facing representation (value always masked).
//
//nolint:revive // used as credential.CredentialView in dashboard/storage
type CredentialView struct {
	ID          int64                  `json:"id"`
	Name        string                 `json:"name"`
	Type        domain.CredentialType  `json:"type"`
	Scope       domain.CredentialScope `json:"scope"`
	OwnerID     string                 `json:"owner_id,omitempty"`
	Encrypted   bool                   `json:"encrypted"`
	Description string                 `json:"description"`
	Tags        []string               `json:"tags"`
	CreatedBy   string                 `json:"created_by"`
	UpdatedBy   string                 `json:"updated_by"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	RotatedAt   *time.Time             `json:"rotated_at,omitempty"`
	ExpiresAt   *time.Time             `json:"expires_at,omitempty"`
	HasValue    bool                   `json:"has_value"`
}

// CredentialDetailView extends CredentialView with bindings for single-resource GET.
//
//nolint:revive // used as credential.CredentialDetailView in dashboard/storage
type CredentialDetailView struct {
	CredentialView
	Bindings []domain.CredentialBinding `json:"bindings"`
}

// ToView converts a domain.Credential to a safe CredentialView (no value exposed).
func ToView(c *domain.Credential) CredentialView {
	var tags []string
	if c.Tags != "" && c.Tags != "[]" {
		// Best-effort JSON parse; fall back to empty.
		_ = jsonUnmarshalTags(c.Tags, &tags) //nolint:errcheck // best-effort tag parse
	}
	return CredentialView{
		ID:          c.ID,
		Name:        c.Name,
		Type:        c.Type,
		Scope:       c.Scope,
		OwnerID:     c.OwnerID,
		Encrypted:   c.Encrypted,
		Description: c.Description,
		Tags:        tags,
		CreatedBy:   c.CreatedBy,
		UpdatedBy:   c.UpdatedBy,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
		RotatedAt:   c.RotatedAt,
		ExpiresAt:   c.ExpiresAt,
		HasValue:    c.Value != "",
	}
}
