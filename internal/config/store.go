package config

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/knadh/koanf/v2"
	"go.uber.org/zap"
)

// ConfigRepository is the subset of storage.Repository needed by ConfigStore.
// Defined here to avoid an import cycle (config -> storage -> config).
type ConfigRepository interface {
	GetConfigOverride(ctx context.Context, key string) (*domain.ConfigOverride, error)
	ListConfigOverrides(ctx context.Context) ([]domain.ConfigOverride, error)
	SaveConfigOverride(ctx context.Context, o *domain.ConfigOverride) error
	DeleteConfigOverride(ctx context.Context, key string) error
}

// ChangePublisher publishes config change events (avoids importing eventbus).
type ChangePublisher interface {
	PublishConfigChanged(ctx context.Context, key string)
}

// Store provides layered configuration: base (TOML+env) overridden by DB values.
// Encrypted values are transparently decrypted on read.
type Store struct {
	base      *Config
	koanf     *koanf.Koanf // base config as koanf instance (for key-path lookups)
	repo      ConfigRepository
	encryptor *Encryptor
	publisher ChangePublisher
	logger    *zap.Logger

	mu          sync.RWMutex
	cache       map[string]*domain.ConfigOverride
	dynamicKeys map[string]bool // keys registered at runtime by skills
	secretKeys  map[string]bool // keys that must always be encrypted
}

// NewStore creates a ConfigStore with the given base config and DB repository.
// encryptor may be nil if encryption is not configured.
// k is the koanf instance from config.Load() for base value lookups by key path.
func NewStore(base *Config, k *koanf.Koanf, repo ConfigRepository, encryptor *Encryptor, logger *zap.Logger) *Store {
	return &Store{
		base:        base,
		koanf:       k,
		repo:        repo,
		encryptor:   encryptor,
		logger:      logger,
		cache:       make(map[string]*domain.ConfigOverride),
		dynamicKeys: make(map[string]bool),
	}
}

// SetPublisher sets the change publisher for notifying config changes.
func (s *Store) SetPublisher(p ChangePublisher) {
	s.publisher = p
}

// LoadOverrides reads all overrides from DB into the in-memory cache.
// Call this once at startup after migrations.
func (s *Store) LoadOverrides(ctx context.Context) error {
	overrides, err := s.repo.ListConfigOverrides(ctx)
	if err != nil {
		return fmt.Errorf("loading config overrides: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = make(map[string]*domain.ConfigOverride, len(overrides))
	for i := range overrides {
		s.cache[overrides[i].Key] = &overrides[i]
	}
	s.logger.Info("loaded config overrides", zap.Int("count", len(overrides)))
	return nil
}

// ReplayOverrides publishes change events for all cached skill overrides.
// Call this after SetPublisher and registerConfigReload so that skills
// pick up DB-stored values (e.g. API tokens) on startup.
func (s *Store) ReplayOverrides(ctx context.Context) {
	if s.publisher == nil {
		return
	}
	s.mu.RLock()
	keys := make([]string, 0, len(s.cache))
	for k := range s.cache {
		keys = append(keys, k)
	}
	s.mu.RUnlock()

	for _, k := range keys {
		s.publisher.PublishConfigChanged(ctx, k)
	}
	if len(keys) > 0 {
		s.logger.Info("replayed config overrides to skills", zap.Int("count", len(keys)))
	}
}

// Get returns the effective value for a config key.
// DB overrides take priority over base config. Encrypted values are decrypted.
// Returns empty string if key not found in overrides (caller should fall back to base config struct).
func (s *Store) Get(key string) (string, bool) {
	s.mu.RLock()
	o, ok := s.cache[key]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	if o.Encrypted && s.encryptor != nil {
		val, err := s.encryptor.Decrypt(o.Value)
		if err != nil {
			s.logger.Error("failed to decrypt config override", zap.String("key", key), zap.Error(err))
			return "", false
		}
		return val, true
	}
	return o.Value, true
}

// HasOverride returns true if the given key has a DB override (regardless of value).
func (s *Store) HasOverride(key string) bool {
	s.mu.RLock()
	_, ok := s.cache[key]
	s.mu.RUnlock()
	return ok
}

// GetBaseValue returns the base config value (from TOML + env) by dotted key path.
// Returns ("", false) if the key doesn't exist in the base config.
func (s *Store) GetBaseValue(key string) (string, bool) {
	if s.koanf == nil {
		return "", false
	}
	if !s.koanf.Exists(key) {
		return "", false
	}
	return fmt.Sprintf("%v", s.koanf.Get(key)), true
}

// GetEffective returns the effective value for a config key: DB override first, then base config.
// Encrypted overrides are decrypted. Returns ("", false) if not found anywhere.
func (s *Store) GetEffective(key string) (string, bool) {
	if val, ok := s.Get(key); ok {
		return val, true
	}
	return s.GetBaseValue(key)
}

// coreKeys lists core (non-skill) config keys that can be overridden at runtime.
// Skill-specific keys are registered dynamically via RegisterKey().
var coreKeys = map[string]bool{
	"app.system_prompt":               true,
	"app.auto_link_summary":           true,
	"app.max_links":                   true,
	"log.level":                       true,
	"log.encoding":                    true,
	"claude.model":                    true,
	"claude.max_tokens":               true,
	"claude.context_window":           true,
	"claude.base_url":                 true,
	"claude.thinking":                 true,
	"claude.streaming":                true,
	"claude.request_timeout":          true,
	"claude.api_key":                  true,
	"openai.api_key":                  true,
	"openai.model":                    true,
	"openai.max_tokens":               true,
	"openai.base_url":                 true,
	"openai.fallback":                 true,
	"ollama.url":                      true,
	"ollama.model":                    true,
	"telegram.token":                  true,
	"telegram.rate_limit":             true,
	"telegram.rate_window":            true,
	"scheduler.concurrency":           true,
	"scheduler.poll_interval":         true,
	"techfacts.enabled":               true,
	"techfacts.interval":              true,
	"techfacts.model":                 true,
	"heartbeat.enabled":               true,
	"heartbeat.interval":              true,
	"routing.enabled":                 true,
	"routing.default_provider":        true,
	"routing.classification_enabled":  true,
	"routing.classification_provider": true,
	"routing.max_actions_per_hour":    true,
	"cache.response_enabled":          true,
	"cache.response_ttl":              true,
	"cache.response_max_items":        true,
	"cache.embedding_enabled":         true,
	"cache.embedding_max_items":       true,
	"cost.enabled":                    true,
	"cost.daily_limit_usd":            true,
	"cost.alert_threshold":            true,
	"embedding.provider":              true,
	"embedding.model_dir":             true,
	"server.enabled":                  true,
	"_system.wizard_completed":        true,
	"_system.toml_ignored":            true,
	"skills.external.allow_shell":     true,
	"skills.external.allow_docker":    true,
	"skills.external.allow_wasm":      true,
}

// RegisterKey adds a config key that can be overridden at runtime.
// Called by skills during startup to declare their config keys.
func (s *Store) RegisterKey(key string) {
	s.mu.Lock()
	s.dynamicKeys[key] = true
	s.mu.Unlock()
}

// RegisterKeys adds multiple config keys at once.
func (s *Store) RegisterKeys(keys []string) {
	s.mu.Lock()
	for _, key := range keys {
		s.dynamicKeys[key] = true
	}
	s.mu.Unlock()
}

// KnownKeys returns all valid config keys (core + dynamically registered).
func (s *Store) KnownKeys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(coreKeys)+len(s.dynamicKeys))
	for k := range coreKeys {
		keys = append(keys, k)
	}
	for k := range s.dynamicKeys {
		keys = append(keys, k)
	}
	return keys
}

// isKnownKey checks if a key is a valid core or dynamically registered key.
func (s *Store) isKnownKey(key string) bool {
	if coreKeys[key] {
		return true
	}
	s.mu.RLock()
	ok := s.dynamicKeys[key]
	s.mu.RUnlock()
	return ok
}

// restartOnlyKeys are keys that cannot be hot-reloaded and require a restart.
var restartOnlyKeys = map[string]bool{
	"storage.path":            true,
	"server.address":          true,
	"proxy.url":               true,
	"security.config_key_env": true,
}

// SetSecretKeys registers keys that must always be encrypted when stored.
func (s *Store) SetSecretKeys(keys map[string]bool) {
	s.mu.Lock()
	s.secretKeys = keys
	s.mu.Unlock()
}

// IsSecretKey returns true if the key is registered as a secret.
func (s *Store) IsSecretKey(key string) bool {
	s.mu.RLock()
	ok := s.secretKeys[key]
	s.mu.RUnlock()
	return ok
}

// SetForImport creates or updates a config override, bypassing restart-only restrictions.
// Used by the TOML import wizard — values take effect after restart.
func (s *Store) SetForImport(ctx context.Context, key, value, updatedBy string, encrypt bool) error {
	if !s.isKnownKey(key) && !restartOnlyKeys[key] && !coreKeys[key] {
		return fmt.Errorf("unknown config key %q", key)
	}
	return s.doSet(ctx, key, value, updatedBy, encrypt)
}

// Set creates or updates a config override in DB and cache.
func (s *Store) Set(ctx context.Context, key, value, updatedBy string, encrypt bool) error {
	if restartOnlyKeys[key] {
		return fmt.Errorf("key %q cannot be changed at runtime (requires restart)", key)
	}
	if !s.isKnownKey(key) {
		return fmt.Errorf("unknown config key %q", key)
	}
	return s.doSet(ctx, key, value, updatedBy, encrypt)
}

func (s *Store) doSet(ctx context.Context, key, value, updatedBy string, encrypt bool) error {
	// Force encryption for secret keys.
	s.mu.RLock()
	if s.secretKeys[key] {
		encrypt = true
	}
	s.mu.RUnlock()
	// Reject placeholder values for secret keys to prevent overwriting real secrets.
	if encrypt && (value == "***" || value == "") {
		return fmt.Errorf("cannot store placeholder value for secret key %q", key)
	}
	if encrypt && s.encryptor == nil {
		// Store plain-text with a warning when no encryptor (Docker without IULITA_CONFIG_KEY).
		encrypt = false
		s.logger.Warn("storing secret key without encryption (no IULITA_CONFIG_KEY)", zap.String("key", key))
	}

	storeValue := value
	if encrypt {
		var err error
		storeValue, err = s.encryptor.Encrypt(value)
		if err != nil {
			return fmt.Errorf("encrypting config value: %w", err)
		}
	}

	o := &domain.ConfigOverride{
		Key:       key,
		Value:     storeValue,
		Encrypted: encrypt,
		UpdatedAt: time.Now(),
		UpdatedBy: updatedBy,
	}

	if err := s.repo.SaveConfigOverride(ctx, o); err != nil {
		return err
	}

	s.mu.Lock()
	s.cache[key] = o
	s.mu.Unlock()

	if s.publisher != nil {
		s.publisher.PublishConfigChanged(ctx, key)
	}

	s.logger.Info("config override set",
		zap.String("key", key),
		zap.Bool("encrypted", encrypt),
		zap.String("updated_by", updatedBy),
	)
	return nil
}

// Delete removes a config override, reverting to the base config value.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.repo.DeleteConfigOverride(ctx, key); err != nil {
		return err
	}

	s.mu.Lock()
	delete(s.cache, key)
	s.mu.Unlock()

	if s.publisher != nil {
		s.publisher.PublishConfigChanged(ctx, key)
	}

	s.logger.Info("config override deleted", zap.String("key", key))
	return nil
}

// List returns all overrides (with encrypted values masked).
func (s *Store) List() []ConfigEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]ConfigEntry, 0, len(s.cache))
	for _, o := range s.cache {
		entry := ConfigEntry{
			Key:       o.Key,
			Value:     o.Value,
			Encrypted: o.Encrypted,
			UpdatedAt: o.UpdatedAt,
			UpdatedBy: o.UpdatedBy,
		}
		if o.Encrypted {
			entry.Value = "***"
		}
		entries = append(entries, entry)
	}
	return entries
}

// ListDecrypted returns all overrides with encrypted values decrypted.
// For admin use only.
func (s *Store) ListDecrypted() []ConfigEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]ConfigEntry, 0, len(s.cache))
	for _, o := range s.cache {
		entry := ConfigEntry{
			Key:       o.Key,
			Encrypted: o.Encrypted,
			UpdatedAt: o.UpdatedAt,
			UpdatedBy: o.UpdatedBy,
		}
		if o.Encrypted && s.encryptor != nil {
			val, err := s.encryptor.Decrypt(o.Value)
			if err != nil {
				entry.Value = "***decrypt-error***"
			} else {
				entry.Value = val
			}
		} else {
			entry.Value = o.Value
		}
		entries = append(entries, entry)
	}
	return entries
}

// Base returns the original base config (read-only, no overrides applied).
func (s *Store) Base() *Config {
	return s.base
}

// EncryptionEnabled returns true if an encryption key is configured.
func (s *Store) EncryptionEnabled() bool {
	return s.encryptor != nil
}

// Encrypt encrypts a plaintext string using the configured encryptor.
// Returns error if encryption is not configured.
func (s *Store) Encrypt(plaintext string) (string, error) {
	if s.encryptor == nil {
		return "", fmt.Errorf("encryption not configured")
	}
	return s.encryptor.Encrypt(plaintext)
}

// Decrypt decrypts an encrypted string using the configured encryptor.
// Returns error if encryption is not configured.
func (s *Store) Decrypt(ciphertext string) (string, error) {
	if s.encryptor == nil {
		return "", fmt.Errorf("encryption not configured")
	}
	return s.encryptor.Decrypt(ciphertext)
}

// ConfigEntry is a config override for API responses.
type ConfigEntry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Encrypted bool      `json:"encrypted"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}
