package credential

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
)

// --- mock types ---

type mockRepo struct {
	mu          sync.Mutex
	credentials map[string]*domain.Credential
	nextID      int64
	bindings    []domain.CredentialBinding
	audits      []domain.CredentialAudit
}

func newMockRepo() *mockRepo {
	return &mockRepo{credentials: make(map[string]*domain.Credential), nextID: 1}
}

func (m *mockRepo) SaveCredential(_ context.Context, c *domain.Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.ID = m.nextID
	m.nextID++
	m.credentials[c.Name] = c
	return nil
}

func (m *mockRepo) GetCredential(_ context.Context, id int64) (*domain.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.credentials {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *mockRepo) GetCredentialByName(_ context.Context, name string) (*domain.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.credentials[name]; ok {
		return c, nil
	}
	return nil, errors.New("not found")
}

func (m *mockRepo) GetCredentialByNameAndOwner(_ context.Context, name, ownerID string) (*domain.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.credentials[name]; ok && c.OwnerID == ownerID {
		return c, nil
	}
	return nil, nil
}

func (m *mockRepo) ListCredentials(_ context.Context, _ CredentialFilter) ([]domain.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]domain.Credential, 0, len(m.credentials))
	for _, c := range m.credentials {
		result = append(result, *c)
	}
	return result, nil
}

func (m *mockRepo) UpdateCredential(_ context.Context, c *domain.Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.credentials[c.Name] = c
	return nil
}

func (m *mockRepo) DeleteCredential(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, c := range m.credentials {
		if c.ID == id {
			delete(m.credentials, name)
			return nil
		}
	}
	return nil
}

func (m *mockRepo) SaveCredentialBinding(_ context.Context, b *domain.CredentialBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bindings = append(m.bindings, *b)
	return nil
}

func (m *mockRepo) DeleteCredentialBinding(_ context.Context, credentialID int64, consumerType, consumerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var filtered []domain.CredentialBinding
	for _, b := range m.bindings {
		if b.CredentialID == credentialID && b.ConsumerType == consumerType && b.ConsumerID == consumerID {
			continue
		}
		filtered = append(filtered, b)
	}
	m.bindings = filtered
	return nil
}

func (m *mockRepo) ListCredentialBindings(_ context.Context, credentialID int64) ([]domain.CredentialBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []domain.CredentialBinding
	for _, b := range m.bindings {
		if b.CredentialID == credentialID {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *mockRepo) ListCredentialBindingsByConsumer(_ context.Context, consumerType, consumerID string) ([]domain.CredentialBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []domain.CredentialBinding
	for _, b := range m.bindings {
		if b.ConsumerType == consumerType && b.ConsumerID == consumerID {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *mockRepo) SaveCredentialAudit(_ context.Context, a *domain.CredentialAudit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits = append(m.audits, *a)
	return nil
}

func (m *mockRepo) ListCredentialAudit(_ context.Context, credentialID int64, limit int) ([]domain.CredentialAudit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []domain.CredentialAudit
	for _, a := range m.audits {
		if a.CredentialID != nil && *a.CredentialID == credentialID {
			result = append(result, a)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

type mockEncryptor struct {
	enabled bool
}

func (m *mockEncryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}

func (m *mockEncryptor) Decrypt(ciphertext string) (string, error) {
	if len(ciphertext) > 4 && ciphertext[:4] == "enc:" {
		return ciphertext[4:], nil
	}
	return ciphertext, nil
}

func (m *mockEncryptor) EncryptionEnabled() bool { return m.enabled }

type mockPublisher struct {
	mu    sync.Mutex
	calls []string
}

func (m *mockPublisher) PublishCredentialChanged(_ context.Context, name string) {
	m.mu.Lock()
	m.calls = append(m.calls, name)
	m.mu.Unlock()
}

type mockFallback struct {
	values map[string]string
}

func (m *mockFallback) GetWithoutCredentials(key string) (string, bool) {
	v, ok := m.values[key]
	return v, ok
}

type mockKeyring struct {
	secrets map[string]string
}

func (m *mockKeyring) GetSecret(_, account string) string {
	return m.secrets[account]
}

// --- tests ---

func newTestStore() (*Store, *mockRepo, *mockPublisher) {
	repo := newMockRepo()
	pub := &mockPublisher{}
	s := NewStore(repo, &mockEncryptor{enabled: true}, zap.NewNop())
	s.SetPublisher(pub)
	return s, repo, pub
}

func TestStore_Set_EncryptsValue(t *testing.T) {
	s, repo, _ := newTestStore()
	ctx := context.Background()

	_, err := s.Set(ctx, SetRequest{Name: "test.key", Value: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	cred := repo.credentials["test.key"]
	repo.mu.Unlock()

	if !cred.Encrypted {
		t.Error("expected Encrypted=true")
	}
	if cred.Value != "enc:secret" {
		t.Errorf("expected encrypted value 'enc:secret', got %q", cred.Value)
	}
}

func TestStore_Set_NoEncryptor(t *testing.T) {
	repo := newMockRepo()
	s := NewStore(repo, nil, zap.NewNop())

	_, err := s.Set(context.Background(), SetRequest{Name: "plain.key", Value: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	cred := repo.credentials["plain.key"]
	repo.mu.Unlock()

	if cred.Encrypted {
		t.Error("expected Encrypted=false without encryptor")
	}
	if cred.Value != "secret" {
		t.Errorf("expected plaintext 'secret', got %q", cred.Value)
	}
}

func TestStore_Set_RejectsPlaceholder(t *testing.T) {
	s, _, _ := newTestStore()
	ctx := context.Background()

	for _, v := range []string{"", "***"} {
		_, err := s.Set(ctx, SetRequest{Name: "test", Value: v})
		if err == nil {
			t.Errorf("expected error for value %q, got nil", v)
		}
	}
}

func TestStore_Get_DecryptsFromCache(t *testing.T) {
	s, _, _ := newTestStore()
	ctx := context.Background()

	s.Set(ctx, SetRequest{Name: "cached.key", Value: "myvalue"})

	val, ok := s.Get("cached.key")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if val != "myvalue" {
		t.Errorf("expected 'myvalue', got %q", val)
	}
}

func TestStore_Get_MissReturnsEmpty(t *testing.T) {
	s, _, _ := newTestStore()
	val, ok := s.Get("nonexistent")
	if ok || val != "" {
		t.Errorf("expected empty result, got %q, %v", val, ok)
	}
}

func TestStore_Resolve_EnvPriority(t *testing.T) {
	s, _, _ := newTestStore()
	ctx := context.Background()

	s.RegisterEnvMapping("claude.api_key", "TEST_CLAUDE_KEY")
	t.Setenv("TEST_CLAUDE_KEY", "env-value")

	// Also set in cache.
	s.Set(ctx, SetRequest{Name: "claude.api_key", Value: "db-value"})

	val, err := s.Resolve(ctx, "claude.api_key")
	if err != nil {
		t.Fatal(err)
	}
	if val != "env-value" {
		t.Errorf("expected env value 'env-value', got %q", val)
	}
}

func TestStore_Resolve_KeyringPriority(t *testing.T) {
	s, _, _ := newTestStore()
	ctx := context.Background()

	s.RegisterKeyringMapping("tg.token", "tg-keyring-account")
	s.SetKeyringProvider(&mockKeyring{secrets: map[string]string{"tg-keyring-account": "keyring-value"}})

	val, err := s.Resolve(ctx, "tg.token")
	if err != nil {
		t.Fatal(err)
	}
	if val != "keyring-value" {
		t.Errorf("expected keyring value, got %q", val)
	}
}

func TestStore_Resolve_FallbackToConfig(t *testing.T) {
	s, _, _ := newTestStore()
	ctx := context.Background()

	s.SetConfigFallback(&mockFallback{values: map[string]string{"old.key": "fallback-val"}})

	val, err := s.Resolve(ctx, "old.key")
	if err != nil {
		t.Fatal(err)
	}
	if val != "fallback-val" {
		t.Errorf("expected fallback value, got %q", val)
	}
}

func TestStore_Resolve_AllMiss(t *testing.T) {
	s, _, _ := newTestStore()
	_, err := s.Resolve(context.Background(), "missing.key")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_ResolveForUser_UserScopedFirst(t *testing.T) {
	repo := &mockRepoWithUserScoped{
		mockRepo: newMockRepo(),
		userCreds: map[string]*domain.Credential{
			"google.token:user1": {
				ID: 99, Name: "google.token", Scope: domain.CredentialScopeUser,
				OwnerID: "user1", Value: "user-token", Encrypted: false,
			},
		},
	}
	s := NewStore(repo, &mockEncryptor{enabled: true}, zap.NewNop())
	ctx := context.Background()

	// Save global credential.
	s.Set(ctx, SetRequest{Name: "google.token", Value: "global-token"})

	val, err := s.ResolveForUser(ctx, "google.token", "user1")
	if err != nil {
		t.Fatal(err)
	}
	if val != "user-token" {
		t.Errorf("expected user-token, got %q", val)
	}
}

// mockRepoWithUserScoped wraps mockRepo to support user-scoped credential lookups.
type mockRepoWithUserScoped struct {
	*mockRepo
	userCreds map[string]*domain.Credential // "name:ownerID" -> credential
}

func (m *mockRepoWithUserScoped) GetCredentialByNameAndOwner(_ context.Context, name, ownerID string) (*domain.Credential, error) {
	key := name + ":" + ownerID
	if c, ok := m.userCreds[key]; ok {
		return c, nil
	}
	return nil, nil
}

func TestStore_Delete_RemovesFromCacheAndPublishes(t *testing.T) {
	s, _, pub := newTestStore()
	ctx := context.Background()

	s.Set(ctx, SetRequest{Name: "del.key", Value: "val"})
	pub.mu.Lock()
	pub.calls = nil // reset
	pub.mu.Unlock()

	// Get the ID.
	s.mu.RLock()
	cred := s.cache["del.key"]
	s.mu.RUnlock()

	if err := s.Delete(ctx, cred.ID, "admin"); err != nil {
		t.Fatal(err)
	}

	if _, ok := s.Get("del.key"); ok {
		t.Error("expected cache miss after delete")
	}

	pub.mu.Lock()
	if len(pub.calls) != 1 || pub.calls[0] != "del.key" {
		t.Errorf("expected publisher called with 'del.key', got %v", pub.calls)
	}
	pub.mu.Unlock()
}

func TestStore_LoadAll_PopulatesCache(t *testing.T) {
	repo := newMockRepo()
	repo.credentials["a"] = &domain.Credential{ID: 1, Name: "a", Value: "v1"}
	repo.credentials["b"] = &domain.Credential{ID: 2, Name: "b", Value: "v2"}
	repo.credentials["c"] = &domain.Credential{ID: 3, Name: "c", Value: "v3"}

	s := NewStore(repo, nil, zap.NewNop())
	if err := s.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(s.cache) != 3 {
		t.Errorf("expected 3 entries in cache, got %d", len(s.cache))
	}
}

func TestStore_ReplayCredentials_PublishesAll(t *testing.T) {
	s, _, pub := newTestStore()
	ctx := context.Background()

	s.Set(ctx, SetRequest{Name: "k1", Value: "v1"})
	s.Set(ctx, SetRequest{Name: "k2", Value: "v2"})

	pub.mu.Lock()
	pub.calls = nil
	pub.mu.Unlock()

	s.ReplayCredentials(ctx)

	pub.mu.Lock()
	if len(pub.calls) != 2 {
		t.Errorf("expected 2 replay calls, got %d", len(pub.calls))
	}
	pub.mu.Unlock()
}

func TestStore_Rotate_SetsRotatedAt(t *testing.T) {
	s, _, _ := newTestStore()
	ctx := context.Background()

	s.Set(ctx, SetRequest{Name: "rot.key", Value: "old"})

	s.mu.RLock()
	cred := s.cache["rot.key"]
	s.mu.RUnlock()

	if err := s.Rotate(ctx, RotateRequest{ID: cred.ID, NewValue: "new", UpdatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}

	s.mu.RLock()
	rotated := s.cache["rot.key"]
	s.mu.RUnlock()

	if rotated.RotatedAt == nil {
		t.Error("expected RotatedAt to be set")
	}
}

func TestStore_WriteAudit_OnSetAndDelete(t *testing.T) {
	s, repo, _ := newTestStore()
	ctx := context.Background()

	s.Set(ctx, SetRequest{Name: "aud.key", Value: "v1", UpdatedBy: "admin"})

	repo.mu.Lock()
	if len(repo.audits) != 1 || repo.audits[0].Action != "created" {
		t.Errorf("expected 1 audit 'created', got %v", repo.audits)
	}
	repo.mu.Unlock()

	// Update.
	s.Set(ctx, SetRequest{Name: "aud.key", Value: "v2", UpdatedBy: "admin"})

	repo.mu.Lock()
	if len(repo.audits) != 2 || repo.audits[1].Action != "updated" {
		t.Errorf("expected 2nd audit 'updated', got %v", repo.audits)
	}
	repo.mu.Unlock()

	// Delete.
	s.mu.RLock()
	id := s.cache["aud.key"].ID
	s.mu.RUnlock()
	s.Delete(ctx, id, "admin")

	repo.mu.Lock()
	if len(repo.audits) != 3 || repo.audits[2].Action != "deleted" {
		t.Errorf("expected 3rd audit 'deleted', got %v", repo.audits)
	}
	repo.mu.Unlock()
}

func TestStore_IsAvailable(t *testing.T) {
	s, _, _ := newTestStore()
	ctx := context.Background()

	if s.IsAvailable(ctx, "missing") {
		t.Error("expected not available for missing key")
	}

	s.Set(ctx, SetRequest{Name: "avail.key", Value: "v"})
	if !s.IsAvailable(ctx, "avail.key") {
		t.Error("expected available after set")
	}
}

func TestStore_List(t *testing.T) {
	s, _, _ := newTestStore()
	ctx := context.Background()

	s.Set(ctx, SetRequest{Name: "l1", Value: "v1", Description: "first"})
	s.Set(ctx, SetRequest{Name: "l2", Value: "v2"})

	views := s.List()
	if len(views) != 2 {
		t.Fatalf("expected 2 views, got %d", len(views))
	}

	for _, v := range views {
		if !v.HasValue {
			t.Error("expected HasValue=true")
		}
	}
}

func TestStore_Bind_CreatesAudit(t *testing.T) {
	s, repo, _ := newTestStore()
	ctx := context.Background()

	s.Set(ctx, SetRequest{Name: "bind.key", Value: "v"})
	s.mu.RLock()
	id := s.cache["bind.key"].ID
	s.mu.RUnlock()

	repo.mu.Lock()
	initialAudits := len(repo.audits)
	repo.mu.Unlock()

	if err := s.Bind(ctx, id, "skill", "todoist", "admin"); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	if len(repo.audits) != initialAudits+1 {
		t.Errorf("expected audit entry for bind")
	}
	lastAudit := repo.audits[len(repo.audits)-1]
	if lastAudit.Action != "bound" {
		t.Errorf("expected 'bound' action, got %q", lastAudit.Action)
	}
	repo.mu.Unlock()
}

func TestNilProvider(t *testing.T) {
	var p NilProvider
	_, err := p.Resolve(context.Background(), "any")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound from NilProvider")
	}
	if p.IsAvailable(context.Background(), "any") {
		t.Error("expected not available from NilProvider")
	}
}

func TestStore_MigrateFromConfigOverrides(t *testing.T) {
	s, repo, _ := newTestStore()
	ctx := context.Background()

	source := &mockMigrationSource{
		secretKeys: []string{"claude.api_key", "telegram.token", "empty.key"},
		values: map[string]string{
			"claude.api_key": "sk-ant-12345",
			"telegram.token": "123:ABCDEF",
			// empty.key has no value — should be skipped
		},
		deleted: make(map[string]bool),
	}

	migrated, err := s.MigrateFromConfigOverrides(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if migrated != 2 {
		t.Errorf("expected 2 migrated, got %d", migrated)
	}

	// Verify credentials in cache.
	val, ok := s.Get("claude.api_key")
	if !ok || val != "sk-ant-12345" {
		t.Errorf("expected 'sk-ant-12345', got %q (ok=%v)", val, ok)
	}
	val, ok = s.Get("telegram.token")
	if !ok || val != "123:ABCDEF" {
		t.Errorf("expected '123:ABCDEF', got %q (ok=%v)", val, ok)
	}

	// Verify deleted from config_overrides.
	if !source.deleted["claude.api_key"] || !source.deleted["telegram.token"] {
		t.Errorf("expected config_overrides deleted, got %v", source.deleted)
	}

	// Verify audit entries.
	repo.mu.Lock()
	if len(repo.audits) < 2 {
		t.Errorf("expected at least 2 audit entries, got %d", len(repo.audits))
	}
	repo.mu.Unlock()

	// Second run: should not migrate again (already in credentials).
	migrated2, _ := s.MigrateFromConfigOverrides(ctx, source)
	if migrated2 != 0 {
		t.Errorf("expected 0 migrated on second run, got %d", migrated2)
	}
}

func TestStore_MigrateGuessType(t *testing.T) {
	s, _, _ := newTestStore()
	ctx := context.Background()

	source := &mockMigrationSource{
		secretKeys: []string{"skills.todoist.api_token", "claude.api_key"},
		values: map[string]string{
			"skills.todoist.api_token": "tok-123",
			"claude.api_key":           "sk-456",
		},
		deleted: make(map[string]bool),
	}

	s.MigrateFromConfigOverrides(ctx, source)

	// Check via repo that types were guessed correctly.
	s.mu.RLock()
	for _, c := range s.cache {
		switch c.Name {
		case "skills.todoist.api_token":
			if c.Type != domain.CredentialTypeBotToken {
				t.Errorf("expected bot_token for token key, got %s", c.Type)
			}
		case "claude.api_key":
			if c.Type != domain.CredentialTypeAPIKey {
				t.Errorf("expected api_key for api_key key, got %s", c.Type)
			}
		}
	}
	s.mu.RUnlock()
}

type mockMigrationSource struct {
	secretKeys []string
	values     map[string]string
	deleted    map[string]bool
}

func (m *mockMigrationSource) GetSecretKeys() []string {
	return m.secretKeys
}

func (m *mockMigrationSource) GetWithoutCredentials(key string) (string, bool) {
	v, ok := m.values[key]
	return v, ok
}

func (m *mockMigrationSource) DeleteConfigOverride(_ context.Context, key string) error {
	m.deleted[key] = true
	return nil
}

// Ensure Store satisfies CredentialProvider at compile time.
var _ CredentialProvider = (*Store)(nil)
var _ CredentialProvider = NilProvider{}

// Suppress unused import warning.
var _ = time.Now
