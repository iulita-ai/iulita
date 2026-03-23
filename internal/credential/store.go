package credential

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
)

// Store manages the credential lifecycle: CRUD, encryption, caching, and hot-reload.
type Store struct {
	repo      Repository
	encryptor CryptoProvider
	publisher ChangePublisher
	fallback  ConfigFallback
	keyring   KeyringProvider
	logger    *zap.Logger

	mu              sync.RWMutex
	cache           map[string]*domain.Credential
	envMappings     map[string]string
	keyringMappings map[string]string
}

// NewStore creates a new credential Store.
func NewStore(repo Repository, encryptor CryptoProvider, logger *zap.Logger) *Store {
	return &Store{
		repo:            repo,
		encryptor:       encryptor,
		logger:          logger,
		cache:           make(map[string]*domain.Credential),
		envMappings:     make(map[string]string),
		keyringMappings: make(map[string]string),
	}
}

// SetPublisher wires the change publisher (called after eventbus is ready).
func (s *Store) SetPublisher(p ChangePublisher) {
	s.mu.Lock()
	s.publisher = p
	s.mu.Unlock()
}

// SetConfigFallback wires the config.Store fallback for the resolution chain.
func (s *Store) SetConfigFallback(f ConfigFallback) {
	s.mu.Lock()
	s.fallback = f
	s.mu.Unlock()
}

// SetKeyringProvider wires the keyring lookup.
func (s *Store) SetKeyringProvider(k KeyringProvider) {
	s.mu.Lock()
	s.keyring = k
	s.mu.Unlock()
}

// RegisterEnvMapping registers an environment variable name for a credential name.
func (s *Store) RegisterEnvMapping(name, envVar string) {
	s.mu.Lock()
	s.envMappings[name] = envVar
	s.mu.Unlock()
}

// RegisterKeyringMapping registers a keyring account name for a credential name.
func (s *Store) RegisterKeyringMapping(name, account string) {
	s.mu.Lock()
	s.keyringMappings[name] = account
	s.mu.Unlock()
}

// LoadAll loads all credentials from the DB into the in-memory cache.
func (s *Store) LoadAll(ctx context.Context) error {
	creds, err := s.repo.ListCredentials(ctx, CredentialFilter{})
	if err != nil {
		return fmt.Errorf("loading credentials: %w", err)
	}
	s.mu.Lock()
	s.cache = make(map[string]*domain.Credential, len(creds))
	for i := range creds {
		s.cache[cacheKey(creds[i].Name, creds[i].OwnerID)] = &creds[i]
	}
	s.mu.Unlock()
	s.logger.Info("loaded credentials", zap.Int("count", len(creds)))
	return nil
}

// ReplayCredentials publishes CredentialChanged for all cached entries
// so subscribers pick up DB-stored values on startup.
func (s *Store) ReplayCredentials(ctx context.Context) {
	s.mu.RLock()
	publisher := s.publisher
	names := make([]string, 0, len(s.cache))
	for _, c := range s.cache {
		names = append(names, c.Name)
	}
	s.mu.RUnlock()

	if publisher == nil {
		return
	}
	for _, name := range names {
		publisher.PublishCredentialChanged(ctx, name)
	}
	if len(names) > 0 {
		s.logger.Info("replayed credentials to subscribers", zap.Int("count", len(names)))
	}
}

// MigrateFromConfigOverrides moves secret keys from config_overrides to the credentials table.
// Only migrates keys that exist in config_overrides but NOT yet in credentials.
// After migration, removes the key from config_overrides.
func (s *Store) MigrateFromConfigOverrides(ctx context.Context, source MigrationSource) (migrated int, err error) {
	secretKeys := source.GetSecretKeys()
	for _, key := range secretKeys {
		// Skip if already exists in credentials.
		if _, ok := s.Get(key); ok {
			continue
		}

		// Read decrypted value from config_overrides or base config.
		val, ok := source.GetWithoutCredentials(key)
		if !ok || val == "" {
			continue
		}

		// Determine credential type from key name.
		credType := guessCredentialType(key)

		_, setErr := s.Set(ctx, SetRequest{
			Name:      key,
			Type:      credType,
			Scope:     domain.CredentialScopeGlobal,
			Value:     val,
			UpdatedBy: "migration",
		})
		if setErr != nil {
			s.logger.Warn("failed to migrate credential from config_overrides",
				zap.String("key", key), zap.Error(setErr))
			continue
		}

		// Remove from config_overrides after successful migration.
		if delErr := source.DeleteConfigOverride(ctx, key); delErr != nil {
			s.logger.Warn("failed to remove migrated config override",
				zap.String("key", key), zap.Error(delErr))
		}

		migrated++
		s.logger.Info("migrated credential from config_overrides",
			zap.String("key", key))
	}
	return migrated, nil
}

// guessCredentialType infers the credential type from its key name.
func guessCredentialType(key string) domain.CredentialType {
	switch {
	case strings.Contains(key, "token"):
		return domain.CredentialTypeBotToken
	case strings.Contains(key, "api_key"):
		return domain.CredentialTypeAPIKey
	case strings.Contains(key, "client_id"), strings.Contains(key, "client_secret"):
		return domain.CredentialTypeOAuth2Client
	default:
		return domain.CredentialTypeAPIKey
	}
}

// Set creates or updates a credential.
func (s *Store) Set(ctx context.Context, req SetRequest) (*domain.Credential, error) {
	if req.Value == "" || req.Value == "***" {
		return nil, fmt.Errorf("credential value must not be empty or a placeholder")
	}

	storeValue := req.Value
	encrypted := false

	if s.encryptor != nil && s.encryptor.EncryptionEnabled() {
		var err error
		storeValue, err = s.encryptor.Encrypt(req.Value)
		if err != nil {
			return nil, fmt.Errorf("encrypting credential: %w", err)
		}
		encrypted = true
	} else {
		s.logger.Warn("storing credential without encryption",
			zap.String("name", req.Name))
	}

	tagsJSON := "[]"
	if len(req.Tags) > 0 {
		b, _ := json.Marshal(req.Tags) //nolint:errcheck // tags marshal cannot fail for []string
		tagsJSON = string(b)
	}

	now := time.Now()
	cred := &domain.Credential{
		Name:        req.Name,
		Type:        req.Type,
		Scope:       req.Scope,
		OwnerID:     req.OwnerID,
		Value:       storeValue,
		Encrypted:   encrypted,
		Description: req.Description,
		Tags:        tagsJSON,
		UpdatedBy:   req.UpdatedBy,
		UpdatedAt:   now,
		ExpiresAt:   req.ExpiresAt,
	}
	if cred.Type == "" {
		cred.Type = domain.CredentialTypeAPIKey
	}
	if cred.Scope == "" {
		cred.Scope = domain.CredentialScopeGlobal
	}

	s.mu.RLock()
	existing, exists := s.cache[cacheKey(req.Name, req.OwnerID)]
	s.mu.RUnlock()

	if exists {
		cred.ID = existing.ID
		cred.CreatedBy = existing.CreatedBy
		cred.CreatedAt = existing.CreatedAt
		if err := s.repo.UpdateCredential(ctx, cred); err != nil {
			return nil, fmt.Errorf("updating credential: %w", err)
		}
	} else {
		cred.CreatedBy = req.UpdatedBy
		cred.CreatedAt = now
		if err := s.repo.SaveCredential(ctx, cred); err != nil {
			return nil, fmt.Errorf("saving credential: %w", err)
		}
	}

	s.mu.Lock()
	s.cache[cacheKey(cred.Name, cred.OwnerID)] = cred
	publisher := s.publisher
	s.mu.Unlock()

	action := domain.CredentialAuditCreated
	if exists {
		action = domain.CredentialAuditUpdated
	}
	s.writeAudit(ctx, cred, action, req.UpdatedBy, "")

	if publisher != nil {
		publisher.PublishCredentialChanged(ctx, cred.Name)
	}

	s.logger.Info("credential set",
		zap.String("name", cred.Name),
		zap.Bool("encrypted", encrypted),
		zap.String("updated_by", req.UpdatedBy),
	)
	return cred, nil
}

// Rotate updates a credential's value and marks rotated_at.
func (s *Store) Rotate(ctx context.Context, req RotateRequest) error {
	// Copy fields under lock to avoid pointer races.
	s.mu.RLock()
	ck := s.cacheKeyByID(req.ID)
	var credName string
	var credType domain.CredentialType
	var credScope domain.CredentialScope
	var credOwnerID, credDesc string
	var credExpiresAt *time.Time
	if c, ok := s.cache[ck]; ok {
		credName = c.Name
		credType = c.Type
		credScope = c.Scope
		credOwnerID = c.OwnerID
		credDesc = c.Description
		credExpiresAt = c.ExpiresAt
	}
	s.mu.RUnlock()

	if credName == "" {
		cred, err := s.repo.GetCredential(ctx, req.ID)
		if err != nil {
			return fmt.Errorf("credential %d not found: %w", req.ID, err)
		}
		credName = cred.Name
		credType = cred.Type
		credScope = cred.Scope
		credOwnerID = cred.OwnerID
		credDesc = cred.Description
		credExpiresAt = cred.ExpiresAt
	}

	cred, err := s.Set(ctx, SetRequest{
		Name:        credName,
		Type:        credType,
		Scope:       credScope,
		OwnerID:     credOwnerID,
		Value:       req.NewValue,
		Description: credDesc,
		UpdatedBy:   req.UpdatedBy,
		ExpiresAt:   credExpiresAt,
	})
	if err != nil {
		return err
	}

	// Persist RotatedAt to both cache and DB.
	now := time.Now()
	ck2 := cacheKey(credName, credOwnerID)
	s.mu.Lock()
	if c, ok := s.cache[ck2]; ok {
		c.RotatedAt = &now
	}
	s.mu.Unlock()

	cred.RotatedAt = &now
	if updateErr := s.repo.UpdateCredential(ctx, cred); updateErr != nil {
		s.logger.Warn("failed to persist rotated_at", zap.String("name", credName), zap.Error(updateErr))
	}

	auditCred := &domain.Credential{Name: credName, ID: cred.ID}
	s.writeAudit(ctx, auditCred, domain.CredentialAuditRotated, req.UpdatedBy, "")
	return nil
}

// Get retrieves a global credential value from cache and decrypts it.
func (s *Store) Get(name string) (string, bool) {
	s.mu.RLock()
	cred, ok := s.cache[cacheKey(name, "")]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	return s.decrypt(name, cred)
}

// GetByID retrieves a credential by ID from DB.
func (s *Store) GetByID(ctx context.Context, id int64) (*domain.Credential, error) {
	return s.repo.GetCredential(ctx, id)
}

// Delete removes a credential from the DB and cache.
func (s *Store) Delete(ctx context.Context, id int64, deletedBy string) error {
	cred, err := s.repo.GetCredential(ctx, id)
	if err != nil {
		return fmt.Errorf("credential %d not found: %w", id, err)
	}
	if err := s.repo.DeleteCredential(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, cacheKey(cred.Name, cred.OwnerID))
	publisher := s.publisher
	s.mu.Unlock()

	s.writeAudit(ctx, cred, domain.CredentialAuditDeleted, deletedBy, "")
	if publisher != nil {
		publisher.PublishCredentialChanged(ctx, cred.Name)
	}
	return nil
}

// List returns masked views of all cached credentials.
func (s *Store) List() []CredentialView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	views := make([]CredentialView, 0, len(s.cache))
	for _, c := range s.cache {
		views = append(views, ToView(c))
	}
	return views
}

// ListFromDB queries the DB directly with filtering.
func (s *Store) ListFromDB(ctx context.Context, filter CredentialFilter) ([]CredentialView, error) {
	creds, err := s.repo.ListCredentials(ctx, filter)
	if err != nil {
		return nil, err
	}
	views := make([]CredentialView, 0, len(creds))
	for i := range creds {
		views = append(views, ToView(&creds[i]))
	}
	return views, nil
}

// Bind creates a credential binding.
func (s *Store) Bind(ctx context.Context, credentialID int64, consumerType, consumerID, createdBy string) error {
	b := &domain.CredentialBinding{
		CredentialID: credentialID,
		ConsumerType: consumerType,
		ConsumerID:   consumerID,
		CreatedAt:    time.Now(),
		CreatedBy:    createdBy,
	}
	if err := s.repo.SaveCredentialBinding(ctx, b); err != nil {
		return err
	}
	cred, _ := s.repo.GetCredential(ctx, credentialID) //nolint:errcheck // best-effort audit context
	if cred != nil {
		s.writeAudit(ctx, cred, domain.CredentialAuditBound, createdBy,
			fmt.Sprintf(`{"consumer_type":%q,"consumer_id":%q}`, consumerType, consumerID))
	}
	return nil
}

// Unbind removes a credential binding.
func (s *Store) Unbind(ctx context.Context, credentialID int64, consumerType, consumerID, removedBy string) error {
	if err := s.repo.DeleteCredentialBinding(ctx, credentialID, consumerType, consumerID); err != nil {
		return err
	}
	cred, _ := s.repo.GetCredential(ctx, credentialID) //nolint:errcheck // best-effort audit context
	if cred != nil {
		s.writeAudit(ctx, cred, domain.CredentialAuditUnbound, removedBy,
			fmt.Sprintf(`{"consumer_type":%q,"consumer_id":%q}`, consumerType, consumerID))
	}
	return nil
}

// ListBindings returns all bindings for a credential.
func (s *Store) ListBindings(ctx context.Context, credentialID int64) ([]domain.CredentialBinding, error) {
	return s.repo.ListCredentialBindings(ctx, credentialID)
}

// ListBindingsByConsumer returns all credential bindings for a specific consumer.
func (s *Store) ListBindingsByConsumer(ctx context.Context, consumerType, consumerID string) ([]domain.CredentialBinding, error) {
	return s.repo.ListCredentialBindingsByConsumer(ctx, consumerType, consumerID)
}

// ResolveForConsumer returns the decrypted credential value bound to a specific consumer.
// Returns ErrNotFound if no binding exists for (consumerType, consumerID).
func (s *Store) ResolveForConsumer(ctx context.Context, consumerType, consumerID string) (string, error) {
	bindings, err := s.repo.ListCredentialBindingsByConsumer(ctx, consumerType, consumerID)
	if err != nil {
		return "", fmt.Errorf("listing bindings: %w", err)
	}
	if len(bindings) == 0 {
		return "", ErrNotFound
	}
	// Look up credential name for cache-based resolution.
	cred, err := s.repo.GetCredential(ctx, bindings[0].CredentialID)
	if err != nil {
		return "", fmt.Errorf("fetching credential %d: %w", bindings[0].CredentialID, err)
	}
	// Prefer the in-memory cache (already decrypted, no DB round-trip).
	if val, ok := s.Get(cred.Name); ok {
		return val, nil
	}
	// Fallback: decrypt from the DB record.
	val, ok := s.decrypt(cred.Name, cred)
	if !ok {
		return "", fmt.Errorf("credential %q has no decryptable value", cred.Name)
	}
	return val, nil
}

// ListCredentialAudit returns audit entries for a credential.
func (s *Store) ListCredentialAudit(ctx context.Context, credentialID int64, limit int) ([]domain.CredentialAudit, error) {
	return s.repo.ListCredentialAudit(ctx, credentialID, limit)
}

// EncryptionEnabled returns true if an encryptor is configured.
func (s *Store) EncryptionEnabled() bool {
	return s.encryptor != nil && s.encryptor.EncryptionEnabled()
}

// --- CredentialProvider implementation ---

// Resolve implements CredentialProvider.
func (s *Store) Resolve(ctx context.Context, name string) (string, error) {
	return s.resolveInternal(ctx, name)
}

// ResolveForUser implements CredentialProvider.
func (s *Store) ResolveForUser(ctx context.Context, name, userID string) (string, error) {
	if userID != "" {
		if cred, err := s.repo.GetCredentialByNameAndOwner(ctx, name, userID); err == nil && cred != nil {
			val, ok := s.decrypt(name, cred)
			if ok {
				return val, nil
			}
		}
	}
	return s.resolveInternal(ctx, name)
}

// IsAvailable implements CredentialProvider.
func (s *Store) IsAvailable(ctx context.Context, name string) bool {
	val, err := s.Resolve(ctx, name)
	return err == nil && val != ""
}

// cacheKey returns the cache key for a credential.
// Uses name + null byte + ownerID to avoid collisions between global and user-scoped credentials.
func cacheKey(name, ownerID string) string {
	if ownerID == "" {
		return name
	}
	return name + "\x00" + ownerID
}

// --- internal helpers ---

func (s *Store) resolveInternal(_ context.Context, name string) (string, error) {
	s.mu.RLock()
	envVar, hasEnv := s.envMappings[name]
	krAccount, hasKr := s.keyringMappings[name]
	fallback := s.fallback
	keyring := s.keyring
	s.mu.RUnlock()

	// 1. Env var mapping.
	if hasEnv && envVar != "" {
		if val := os.Getenv(envVar); val != "" {
			return val, nil
		}
	}

	// 2. Keyring mapping.
	if hasKr && krAccount != "" && keyring != nil {
		if val := keyring.GetSecret("", krAccount); val != "" {
			return val, nil
		}
	}

	// 3. In-memory cache (DB-backed).
	if val, ok := s.Get(name); ok && val != "" {
		return val, nil
	}

	// 4. ConfigFallback (config_overrides or TOML base — non-recursive path).
	if fallback != nil {
		if val, ok := fallback.GetWithoutCredentials(name); ok && val != "" {
			return val, nil
		}
	}

	return "", ErrNotFound
}

func (s *Store) decrypt(name string, cred *domain.Credential) (string, bool) {
	if !cred.Encrypted || s.encryptor == nil {
		return cred.Value, cred.Value != ""
	}
	val, err := s.encryptor.Decrypt(cred.Value)
	if err != nil {
		s.logger.Error("failed to decrypt credential",
			zap.String("name", name), zap.Error(err))
		return "", false
	}
	return val, val != ""
}

// cacheKeyByID finds the cache key for a credential by its DB ID.
// Must be called with s.mu held (at least RLock).
func (s *Store) cacheKeyByID(id int64) string {
	for key, c := range s.cache {
		if c.ID == id {
			return key
		}
	}
	return ""
}

func (s *Store) writeAudit(ctx context.Context, cred *domain.Credential, action, actor, detail string) {
	entry := &domain.CredentialAudit{
		CredentialName: cred.Name,
		Action:         action,
		Actor:          actor,
		Detail:         detail,
		CreatedAt:      time.Now(),
	}
	if cred.ID != 0 {
		entry.CredentialID = &cred.ID
	}
	if err := s.repo.SaveCredentialAudit(ctx, entry); err != nil {
		s.logger.Warn("failed to write credential audit",
			zap.String("name", cred.Name), zap.Error(err))
	}
}

// jsonUnmarshalTags is a helper to parse JSON tags.
func jsonUnmarshalTags(s string, out *[]string) error {
	return json.Unmarshal([]byte(s), out)
}
