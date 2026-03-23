package credential

import (
	"context"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// storageRepo is the subset of storage.Repository used by the adapter.
type storageRepo interface {
	SaveCredential(ctx context.Context, c *domain.Credential) error
	GetCredential(ctx context.Context, id int64) (*domain.Credential, error)
	GetCredentialByName(ctx context.Context, name string) (*domain.Credential, error)
	GetCredentialByNameAndOwner(ctx context.Context, name, ownerID string) (*domain.Credential, error)
	ListCredentials(ctx context.Context, filter storage.CredentialFilter) ([]domain.Credential, error)
	UpdateCredential(ctx context.Context, c *domain.Credential) error
	DeleteCredential(ctx context.Context, id int64) error

	SaveCredentialBinding(ctx context.Context, b *domain.CredentialBinding) error
	DeleteCredentialBinding(ctx context.Context, credentialID int64, consumerType, consumerID string) error
	ListCredentialBindings(ctx context.Context, credentialID int64) ([]domain.CredentialBinding, error)
	ListCredentialBindingsByConsumer(ctx context.Context, consumerType, consumerID string) ([]domain.CredentialBinding, error)

	SaveCredentialAudit(ctx context.Context, a *domain.CredentialAudit) error
	ListCredentialAudit(ctx context.Context, credentialID int64, limit int) ([]domain.CredentialAudit, error)
}

// storageAdapter adapts storage.Repository to credential.Repository.
type storageAdapter struct {
	repo storageRepo
}

// NewStorageAdapter wraps a storage.Repository to satisfy credential.Repository.
func NewStorageAdapter(r storageRepo) Repository {
	return &storageAdapter{repo: r}
}

// storageAdapter methods implement credential.Repository by delegating to storage.Repository.

func (a *storageAdapter) SaveCredential(ctx context.Context, c *domain.Credential) error { //nolint:revive // interface implementation
	return a.repo.SaveCredential(ctx, c)
}

func (a *storageAdapter) GetCredential(ctx context.Context, id int64) (*domain.Credential, error) { //nolint:revive // interface implementation
	return a.repo.GetCredential(ctx, id)
}

func (a *storageAdapter) GetCredentialByName(ctx context.Context, name string) (*domain.Credential, error) { //nolint:revive // interface implementation
	return a.repo.GetCredentialByName(ctx, name)
}

func (a *storageAdapter) GetCredentialByNameAndOwner(ctx context.Context, name, ownerID string) (*domain.Credential, error) { //nolint:revive // interface implementation
	return a.repo.GetCredentialByNameAndOwner(ctx, name, ownerID)
}

func (a *storageAdapter) ListCredentials(ctx context.Context, filter CredentialFilter) ([]domain.Credential, error) { //nolint:revive // interface implementation
	return a.repo.ListCredentials(ctx, storage.CredentialFilter{
		Scope:   filter.Scope,
		OwnerID: filter.OwnerID,
		Type:    filter.Type,
	})
}

func (a *storageAdapter) UpdateCredential(ctx context.Context, c *domain.Credential) error { //nolint:revive // interface implementation
	return a.repo.UpdateCredential(ctx, c)
}

func (a *storageAdapter) DeleteCredential(ctx context.Context, id int64) error { //nolint:revive // interface implementation
	return a.repo.DeleteCredential(ctx, id)
}

func (a *storageAdapter) SaveCredentialBinding(ctx context.Context, b *domain.CredentialBinding) error { //nolint:revive // interface implementation
	return a.repo.SaveCredentialBinding(ctx, b)
}

func (a *storageAdapter) DeleteCredentialBinding(ctx context.Context, credentialID int64, consumerType, consumerID string) error { //nolint:revive // interface implementation
	return a.repo.DeleteCredentialBinding(ctx, credentialID, consumerType, consumerID)
}

func (a *storageAdapter) ListCredentialBindings(ctx context.Context, credentialID int64) ([]domain.CredentialBinding, error) { //nolint:revive // interface implementation
	return a.repo.ListCredentialBindings(ctx, credentialID)
}

func (a *storageAdapter) ListCredentialBindingsByConsumer(ctx context.Context, consumerType, consumerID string) ([]domain.CredentialBinding, error) { //nolint:revive // interface implementation
	return a.repo.ListCredentialBindingsByConsumer(ctx, consumerType, consumerID)
}

func (a *storageAdapter) SaveCredentialAudit(ctx context.Context, a2 *domain.CredentialAudit) error { //nolint:revive // interface implementation
	return a.repo.SaveCredentialAudit(ctx, a2)
}

func (a *storageAdapter) ListCredentialAudit(ctx context.Context, credentialID int64, limit int) ([]domain.CredentialAudit, error) { //nolint:revive // interface implementation
	return a.repo.ListCredentialAudit(ctx, credentialID, limit)
}
