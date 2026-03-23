package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// SaveCredential persists a new credential.
func (s *Store) SaveCredential(ctx context.Context, c *domain.Credential) error {
	_, err := s.db.NewInsert().Model(c).Exec(ctx)
	return err
}

// GetCredential retrieves a credential by ID.
func (s *Store) GetCredential(ctx context.Context, id int64) (*domain.Credential, error) {
	c := new(domain.Credential)
	err := s.db.NewSelect().Model(c).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("credential %d: %w", id, storage.ErrNotFound)
	}
	return c, err
}

// GetCredentialByName retrieves a global credential by name.
func (s *Store) GetCredentialByName(ctx context.Context, name string) (*domain.Credential, error) {
	c := new(domain.Credential)
	err := s.db.NewSelect().Model(c).Where("name = ? AND owner_id = ''", name).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("credential %q: %w", name, storage.ErrNotFound)
	}
	return c, err
}

// GetCredentialByNameAndOwner retrieves a user-scoped credential.
func (s *Store) GetCredentialByNameAndOwner(ctx context.Context, name, ownerID string) (*domain.Credential, error) {
	c := new(domain.Credential)
	err := s.db.NewSelect().Model(c).
		Where("name = ? AND owner_id = ?", name, ownerID).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// ListCredentials returns credentials matching the filter.
func (s *Store) ListCredentials(ctx context.Context, filter storage.CredentialFilter) ([]domain.Credential, error) {
	var creds []domain.Credential
	q := s.db.NewSelect().Model(&creds)
	if filter.Scope != "" {
		q = q.Where("scope = ?", filter.Scope)
	}
	if filter.OwnerID != "" {
		q = q.Where("owner_id = ?", filter.OwnerID)
	}
	if filter.Type != "" {
		q = q.Where("type = ?", filter.Type)
	}
	err := q.OrderExpr("name ASC").Scan(ctx)
	return creds, err
}

// UpdateCredential updates an existing credential.
func (s *Store) UpdateCredential(ctx context.Context, c *domain.Credential) error {
	c.UpdatedAt = time.Now()
	_, err := s.db.NewUpdate().Model(c).Where("id = ?", c.ID).Exec(ctx)
	return err
}

// DeleteCredential removes a credential by ID.
func (s *Store) DeleteCredential(ctx context.Context, id int64) error {
	_, err := s.db.NewDelete().Model((*domain.Credential)(nil)).Where("id = ?", id).Exec(ctx)
	return err
}

// SaveCredentialBinding creates a credential binding (idempotent).
func (s *Store) SaveCredentialBinding(ctx context.Context, b *domain.CredentialBinding) error {
	_, err := s.db.NewInsert().Model(b).
		On("CONFLICT (credential_id, consumer_type, consumer_id) DO NOTHING").
		Exec(ctx)
	return err
}

// DeleteCredentialBinding removes a specific credential binding.
func (s *Store) DeleteCredentialBinding(ctx context.Context, credentialID int64, consumerType, consumerID string) error {
	_, err := s.db.NewDelete().
		Model((*domain.CredentialBinding)(nil)).
		Where("credential_id = ? AND consumer_type = ? AND consumer_id = ?",
			credentialID, consumerType, consumerID).
		Exec(ctx)
	return err
}

// ListCredentialBindings returns all bindings for a credential.
func (s *Store) ListCredentialBindings(ctx context.Context, credentialID int64) ([]domain.CredentialBinding, error) {
	var bindings []domain.CredentialBinding
	err := s.db.NewSelect().Model(&bindings).
		Where("credential_id = ?", credentialID).
		OrderExpr("created_at ASC").
		Scan(ctx)
	return bindings, err
}

// ListCredentialBindingsByConsumer returns bindings for a consumer.
func (s *Store) ListCredentialBindingsByConsumer(ctx context.Context, consumerType, consumerID string) ([]domain.CredentialBinding, error) {
	var bindings []domain.CredentialBinding
	err := s.db.NewSelect().Model(&bindings).
		Where("consumer_type = ? AND consumer_id = ?", consumerType, consumerID).
		Scan(ctx)
	return bindings, err
}

// SaveCredentialAudit persists an audit entry.
func (s *Store) SaveCredentialAudit(ctx context.Context, a *domain.CredentialAudit) error {
	_, err := s.db.NewInsert().Model(a).Exec(ctx)
	return err
}

// ListChannelInstanceCredentialBindings returns instance->credential mappings.
func (s *Store) ListChannelInstanceCredentialBindings(ctx context.Context) (map[string]storage.ChannelCredentialBinding, error) {
	type row struct {
		ConsumerID     string `bun:"consumer_id"`
		CredentialID   int64  `bun:"credential_id"`
		CredentialName string `bun:"credential_name"`
	}
	var rows []row
	err := s.db.NewSelect().
		TableExpr("credential_bindings AS cb").
		ColumnExpr("cb.consumer_id, cb.credential_id, c.name AS credential_name").
		Join("JOIN credentials AS c ON c.id = cb.credential_id").
		Where("cb.consumer_type = ?", domain.CredentialConsumerChannelInstance).
		Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	result := make(map[string]storage.ChannelCredentialBinding, len(rows))
	for _, r := range rows {
		result[r.ConsumerID] = storage.ChannelCredentialBinding{
			CredentialID:   r.CredentialID,
			CredentialName: r.CredentialName,
		}
	}
	return result, nil
}

// ListCredentialAudit returns audit entries for a credential.
func (s *Store) ListCredentialAudit(ctx context.Context, credentialID int64, limit int) ([]domain.CredentialAudit, error) {
	var entries []domain.CredentialAudit
	q := s.db.NewSelect().Model(&entries).
		Where("credential_id = ?", credentialID).
		OrderExpr("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Scan(ctx)
	return entries, err
}
