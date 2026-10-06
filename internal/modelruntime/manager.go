package modelruntime

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/models"
)

type secretConnection struct {
	Connection Connection `json:"connection"`
	Secret     string     `json:"secret"`
}

func (c secretConnection) handle() Connection { out := c.Connection; out.APIKey = c.Secret; return out }

type stageState struct {
	ID           string                         `json:"id"`
	Actor        string                         `json:"actor"`
	BaseRevision uint64                         `json:"base_revision"`
	ConfigHash   string                         `json:"config_hash"`
	CreatedAt    time.Time                      `json:"created_at"`
	ExpiresAt    time.Time                      `json:"expires_at"`
	Settings     models.Settings                `json:"settings"`
	Connections  map[string]secretConnection    `json:"connections"`
	Evidence     map[string]map[string]Evidence `json:"evidence"`
}
type probeRecord struct {
	Revision    uint64    `json:"revision"`
	View        ProbeView `json:"view"`
	Actor       string    `json:"actor"`
	Key         string    `json:"key"`
	RequestHash string    `json:"request_hash"`
	Fingerprint string    `json:"fingerprint"`
	Generation  string    `json:"generation"`
	Provider    string    `json:"provider"`
	ExpiresAt   time.Time `json:"expires_at"`
}
type durableState struct {
	LegacySeeded      bool                           `json:"legacy_seeded,omitempty"`
	LegacyProfiles    map[string]string              `json:"legacy_profiles,omitempty"`
	LegacyBaseline    models.Settings                `json:"legacy_baseline,omitempty"`
	History           []HistoryView                  `json:"history,omitempty"`
	IdentitySalt      string                         `json:"identity_salt"`
	DeniedCredentials map[string]time.Time           `json:"denied_credentials"`
	Version           int                            `json:"version"`
	Revision          uint64                         `json:"revision"`
	Settings          models.Settings                `json:"settings"`
	Connections       map[string]secretConnection    `json:"connections"`
	Denied            map[string]time.Time           `json:"denied"`
	Evidence          map[string]map[string]Evidence `json:"evidence"`
	Stage             *stageState                    `json:"stage,omitempty"`
	Probes            map[string]probeRecord         `json:"probes"`
}
type admission struct {
	provider, generation string
	cancel               context.CancelFunc
}

// Manager serializes encrypted model configuration, probes and credential admission.
type Manager struct {
	mu               sync.Mutex
	activity         sync.WaitGroup
	repo             Repository
	cipher           Cipher
	factory          Factory
	authorize        Authorize
	state            durableState
	now              func() time.Time
	bindings         map[string]llm.ProfileBinding
	admissions       map[string]admission
	probeCancel      map[string]context.CancelFunc
	running          map[string]string // connection -> probe ID
	publisher        func(*llm.ProfileSnapshot) error
	activeRevision   uint64
	activationStatus string
	closed           bool
}

// New loads encrypted configuration and restores bounded probe lifecycle state.
func New(repo Repository, cipher Cipher, factory Factory, authorize Authorize) (*Manager, error) {
	if repo == nil || factory == nil {
		return nil, failure("runtime_unavailable", "Model runtime dependencies are unavailable", 503)
	}
	// A typed nil encryptor must not appear to provide encryption.
	if cipher != nil && reflect.ValueOf(cipher).Kind() == reflect.Pointer && reflect.ValueOf(cipher).IsNil() {
		cipher = nil
	}
	m := &Manager{repo: repo, cipher: cipher, factory: factory, authorize: authorize, now: time.Now, bindings: map[string]llm.ProfileBinding{}, admissions: map[string]admission{}, probeCancel: map[string]context.CancelFunc{}, running: map[string]string{}}
	m.state = durableState{IdentitySalt: randomID() + randomID(), DeniedCredentials: map[string]time.Time{}, Version: 1, Settings: models.Settings{SchemaVersion: 1}, Connections: map[string]secretConnection{}, Denied: map[string]time.Time{}, Evidence: map[string]map[string]Evidence{}, Probes: map[string]probeRecord{}}
	row, err := repo.GetConfigOverride(context.Background(), StateKey)
	if err != nil {
		return nil, failure("storage_unavailable", "Could not load model configuration", 503)
	}
	if row != nil {
		if cipher == nil || !row.Encrypted {
			return nil, failure("secret_encryption_unavailable", "Encrypted model configuration is unavailable", 503)
		}
		plain, err := cipher.Decrypt(row.Value)
		if err != nil || len(plain) > maxStateBytes {
			return nil, failure("invalid_runtime_state", "Could not decrypt model configuration", 503)
		}
		if json.Unmarshal([]byte(plain), &m.state) != nil || m.state.Version != 1 || m.state.Connections == nil || m.state.Denied == nil || m.state.Evidence == nil || m.state.Probes == nil {
			return nil, failure("invalid_runtime_state", "Model configuration cannot be loaded", 503)
		}
		changed := m.cleanupLocked()
		for provider, c := range m.state.Connections {
			source := normalizeSource(c.Connection.Source)
			if source != c.Connection.Source {
				c.Connection.Source = source
				m.state.Connections[provider] = c
				changed = true
			}
		}
		if m.state.Stage != nil {
			for provider, c := range m.state.Stage.Connections {
				source := normalizeSource(c.Connection.Source)
				if source != c.Connection.Source {
					c.Connection.Source = source
					m.state.Stage.Connections[provider] = c
					changed = true
				}
			}
		}
		if len(m.state.History) > 3 {
			m.state.History = m.state.History[len(m.state.History)-3:]
			changed = true
		}
		if m.state.IdentitySalt == "" {
			m.state.IdentitySalt = randomID() + randomID()
			changed = true
		}
		if m.state.DeniedCredentials == nil {
			m.state.DeniedCredentials = map[string]time.Time{}
			changed = true
		}
		for _, c := range m.state.Connections {
			if _, revoked := m.state.Denied[c.Connection.Generation]; revoked {
				identity := secretIdentity(c.Connection.Provider, c.Secret, m.state.IdentitySalt)
				if _, exists := m.state.DeniedCredentials[identity]; !exists {
					m.state.DeniedCredentials[identity] = m.now().UTC()
					changed = true
				}
			}
		}
		for id := range m.state.Probes {
			r := m.state.Probes[id]
			if r.View.Status == "running" || r.View.Status == "cancel_requested" {
				r.View.Status = "interrupted_unknown"
				r.View.ErrorCode = "restart_interrupted"
				m.state.Probes[id] = r
				changed = true
			}
		}
		if changed {
			if err := m.persistLocked(context.Background(), m.state, "startup"); err != nil {
				return nil, err
			}
		}
	}
	if len(m.state.Settings.Profiles) > 0 {
		b, err := m.buildBindingsLocked(m.state.Settings, m.state.Connections, m.state.Evidence, "")
		if err != nil {
			return nil, err
		}
		m.bindings = b
	}
	m.activeRevision = m.state.Revision
	m.activationStatus = "applied"
	return m, nil
}

func (m *Manager) authorizeActor(ctx context.Context, actor string) error {
	if actor == "" || len(actor) > 128 || m.authorize == nil {
		return failure("admin_required", "Administrator authorization is required", 403)
	}
	if err := m.authorize(ctx, actor); err != nil {
		var coded *Error
		if errors.As(err, &coded) && coded.Code == "password_change_required" {
			return failure("password_change_required", "Complete the required password change before using model settings", 403)
		}
		return failure("admin_required", "Administrator authorization or password change is required", 403)
	}
	return nil
}
func normalizeSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "env", "environment":
		return "environment"
	case "legacy", "legacy_effective":
		return "legacy_effective"
	}
	return source
}

func (m *Manager) checkRevisionLocked(expected uint64) error {
	if m.closed {
		return failure("runtime_stopped", "Model runtime is stopping", 503)
	}
	if expected != m.state.Revision {
		return failure("revision_conflict", "Model settings changed; review the current revision", 409)
	}
	return nil
}
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func secretIdentity(provider, key, salt string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	_, _ = mac.Write([]byte(provider + "\x00" + key))
	return hex.EncodeToString(mac.Sum(nil))
}

// hashJSON is used only with bounded model settings and metadata structs. An
// unsupported programmer-supplied value must not silently collide with nil JSON.
func hashJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic("model fingerprint metadata cannot be encoded")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func cloneSettings(s models.Settings) models.Settings {
	out := s
	out.Profiles = slices.Clone(s.Profiles)
	out.Policy.LegacyHints = maps.Clone(s.Policy.LegacyHints)
	out.Policy.LegacyProfileHints = maps.Clone(s.Policy.LegacyProfileHints)
	out.Policy.ForbiddenProviders = slices.Clone(s.Policy.ForbiddenProviders)
	out.Policy.Fallbacks = maps.Clone(s.Policy.Fallbacks)
	for role := range out.Policy.Fallbacks {
		out.Policy.Fallbacks[role] = slices.Clone(s.Policy.Fallbacks[role])
	}
	return out
}
func cloneConnections(connections map[string]secretConnection) map[string]secretConnection {
	out := maps.Clone(connections)
	for provider, c := range out {
		// APIKey is a transient factory handle; durable secrets live in Secret.
		c.Connection.APIKey = ""
		out[provider] = c
	}
	return out
}
func cloneState(s durableState) durableState {
	out := s
	out.Settings = cloneSettings(s.Settings)
	out.LegacyBaseline = cloneSettings(s.LegacyBaseline)
	out.LegacyProfiles = maps.Clone(s.LegacyProfiles)
	out.Denied = maps.Clone(s.Denied)
	out.DeniedCredentials = maps.Clone(s.DeniedCredentials)
	out.Connections = cloneConnections(s.Connections)
	out.Evidence = cloneEvidence(s.Evidence)
	out.History = slices.Clone(s.History)
	for i := range out.History {
		out.History[i].Settings = cloneSettings(s.History[i].Settings)
	}
	out.Probes = maps.Clone(s.Probes)
	for id := range out.Probes {
		r := out.Probes[id]
		r.View = copyProbeView(r.View)
		out.Probes[id] = r
	}
	if s.Stage != nil {
		stage := *s.Stage
		stage.Settings = cloneSettings(s.Stage.Settings)
		stage.Connections = cloneConnections(s.Stage.Connections)
		stage.Evidence = cloneEvidence(s.Stage.Evidence)
		out.Stage = &stage
	}
	return out
}
func cloneEvidence(e map[string]map[string]Evidence) map[string]map[string]Evidence {
	out := make(map[string]map[string]Evidence, len(e))
	for id, kinds := range e {
		out[id] = maps.Clone(kinds)
	}
	return out
}
func (m *Manager) persistLocked(ctx context.Context, s durableState, actor string) error {
	if m.cipher == nil {
		return failure("secret_encryption_unavailable", "Enable configuration encryption before changing models", 422)
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > maxStateBytes {
		return failure("runtime_state_limit", "Model metadata exceeds the storage limit", 422)
	}
	encrypted, err := m.cipher.Encrypt(string(raw))
	if err != nil {
		return failure("secret_encryption_unavailable", "Could not encrypt model configuration", 503)
	}
	if err := m.repo.SaveConfigOverride(ctx, &domain.ConfigOverride{Key: StateKey, Value: encrypted, Encrypted: true, UpdatedAt: m.now().UTC(), UpdatedBy: actor}); err != nil {
		row, readErr := m.repo.GetConfigOverride(context.Background(), StateKey)
		if readErr == nil && row != nil && row.Encrypted && row.Value == encrypted {
			return nil
		}
		return failure("storage_unavailable", "Model configuration was not committed", 503)
	}
	return nil
}

// SeedConnections imports only existing effective credentials, without enabling
// profiles or classifiers. A restart cannot replace a stored generation or undo
// deny merely because file/env settings contain an old key.
func (m *Manager) SeedConnections(ctx context.Context, connections []Connection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	candidate := cloneState(m.state)
	changed := false
	if len(connections) > 5 {
		return failure("invalid_connection", "Too many connections", 422)
	}
	for _, c := range connections {
		c.Source = normalizeSource(c.Source)
		if !models.ValidProvider(c.Provider) {
			return failure("invalid_connection", "Unknown connection provider", 422)
		}
		if c.Provider == "deepseek" || c.Provider == "zai" {
			if canonical, err := llm.OfficialModelEndpoint(c.Provider, c.Endpoint); err == nil {
				c.Endpoint = canonical
			}
		}
		if c.Endpoint != "" {
			u, err := url.Parse(c.Endpoint)
			if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(c.Endpoint) > 512 {
				return failure("untrusted_endpoint", "Credentials cannot be embedded in a model endpoint", 422)
			}
		}
		if old, ok := candidate.Connections[c.Provider]; ok {
			if c.APIKey != old.Secret || c.Endpoint != old.Connection.Endpoint {
				legacySource := old.Connection.Source == "legacy_effective" || old.Connection.Source == "TOML" || old.Connection.Source == "toml" || old.Connection.Source == "encrypted_legacy"
				if m.state.Settings.Policy.Everyday != "" || m.state.Stage != nil || !legacySource {
					return failure("effective_config_conflict", "Stored connection differs from effective startup configuration; stage the change explicitly", 409)
				}
				identity := secretIdentity(c.Provider, c.APIKey, candidate.IdentitySalt)
				if _, revoked := candidate.DeniedCredentials[identity]; revoked {
					return failure("credential_revoked", "A retired credential cannot be restored by startup configuration", 422)
				}
				candidate.Denied[old.Connection.Generation] = m.now().UTC()
				if old.Secret != "" {
					candidate.DeniedCredentials[secretIdentity(c.Provider, old.Secret, candidate.IdentitySalt)] = m.now().UTC()
				}
				if c.Provider != "ollama" && strings.TrimSpace(c.APIKey) == "" {
					return failure("credential_required", "The effective credential is empty", 422)
				}
				if len(c.APIKey) > 4096 || strings.ContainsAny(c.APIKey, "\x00\r\n") {
					return failure("invalid_credential", "Invalid credential format", 422)
				}
				secret := c.APIKey
				c.APIKey = ""
				c.Generation = randomID()
				c.Enabled = true
				if c.Source == "" {
					c.Source = "legacy_effective"
				}
				candidate.Connections[c.Provider] = secretConnection{Connection: c, Secret: secret}
				changed = true
				continue
			}
			if c.Source != "" && c.Source != old.Connection.Source {
				old.Connection.Source = c.Source
				candidate.Connections[c.Provider] = old
				changed = true
			}
			continue
		}
		if _, revoked := candidate.DeniedCredentials[secretIdentity(c.Provider, c.APIKey, candidate.IdentitySalt)]; revoked {
			return failure("credential_revoked", "A revoked credential cannot be imported as a new connection", 422)
		}
		if c.Provider != "ollama" && strings.TrimSpace(c.APIKey) == "" {
			continue
		}
		if len(c.APIKey) > 4096 || strings.ContainsAny(c.APIKey, "\x00\r\n") {
			return failure("invalid_credential", "Invalid credential format", 422)
		}
		c.Generation = randomID()
		if c.Source == "" {
			c.Source = "legacy_effective"
		}
		c.Enabled = true
		secret := c.APIKey
		c.APIKey = ""
		candidate.Connections[c.Provider] = secretConnection{Connection: c, Secret: secret}
		changed = true
	}
	if !changed {
		return nil
	}
	if err := m.persistLocked(ctx, candidate, "startup"); err != nil {
		return err
	}
	m.state = candidate
	m.cancelDeniedLocked()
	return nil
}

func profileFingerprint(p models.Profile, c Connection) string {
	p.Name = ""
	return hashJSON(struct {
		Profile                                      models.Profile
		Endpoint, Generation, Compatibility, Fixture string
	}{p, safeEndpoint(c.Endpoint), c.Generation, models.CompatibilityVersion(p.Connection, p.Model), FixtureVersion})
}
func safeConnection(c secretConnection, denied map[string]time.Time) ConnectionView {
	availability := "untested"
	if _, blocked := denied[c.Connection.Generation]; blocked || !c.Connection.Enabled {
		availability = "suspended"
	}
	return ConnectionView{c.Connection.Provider, safeEndpoint(c.Connection.Endpoint), c.Connection.Generation, c.Connection.Source, c.Secret != "" || c.Connection.Provider == "ollama", availability}
}
func (m *Manager) profileViewsLocked(settings models.Settings, connections map[string]secretConnection, evidence map[string]map[string]Evidence) []ProfileView {
	out := make([]ProfileView, 0, len(settings.Profiles))
	for _, p := range settings.Profiles {
		c := connections[p.Connection]
		fp := profileFingerprint(p, c.Connection)
		v := ProfileView{ID: p.ID, Fingerprint: fp, Eligibility: m.eligibilityLocked(p, c, evidence[p.ID])}
		if _, denied := m.state.Denied[c.Connection.Generation]; denied || c.Connection.Provider != "" && !c.Connection.Enabled {
			v.Eligibility = "suspended"
		}
		for _, provider := range m.state.Settings.Policy.ForbiddenProviders {
			if provider == p.Connection {
				v.Eligibility = "suspended"
			}
		}
		for _, kind := range []string{"text", "tools", "vision"} {
			if e, ok := evidence[p.ID][kind]; ok && e.Fingerprint == fp {
				v.Evidence = append(v.Evidence, e)
			}
		}
		out = append(out, v)
	}
	return out
}
func (m *Manager) stageViewLocked(s *stageState) StageView {
	v := StageView{ID: s.ID, BaseRevision: s.BaseRevision, ConfigHash: s.ConfigHash, CreatedAt: s.CreatedAt, ExpiresAt: s.ExpiresAt, Settings: cloneSettings(s.Settings), Profiles: m.profileViewsLocked(s.Settings, s.Connections, s.Evidence)}
	for _, provider := range sortedConnections(s.Connections) {
		v.Connections = append(v.Connections, safeConnection(s.Connections[provider], m.state.Denied))
	}
	return v
}
func safeEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	if parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.Path
}

func sortedConnections(connections map[string]secretConnection) []string {
	keys := make([]string, 0, len(connections))
	for k := range connections {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func (m *Manager) snapshotLocked() View {
	v := View{Revision: m.state.Revision, ActiveRevision: m.activeRevision, ActivationStatus: m.activationStatus, Settings: cloneSettings(m.state.Settings), Profiles: m.profileViewsLocked(m.state.Settings, m.state.Connections, m.state.Evidence), Health: "ready", EncryptionAvailable: m.cipher != nil}
	if m.state.Settings.Policy.Everyday == "" {
		v.Health = "setup_required"
	}
	if m.activationStatus == "failed" {
		v.Health = "degraded"
	}
	for _, provider := range sortedConnections(m.state.Connections) {
		v.Connections = append(v.Connections, safeConnection(m.state.Connections[provider], m.state.Denied))
	}
	forbidden := make(map[string]bool)
	for _, provider := range m.state.Settings.Policy.ForbiddenProviders {
		forbidden[provider] = true
	}
	for role, id := range m.state.Settings.Policy.Roles() {
		for _, p := range m.state.Settings.Profiles {
			if p.ID == id {
				c := m.state.Connections[p.Connection]
				_, blocked := m.state.Denied[c.Connection.Generation]
				if blocked || forbidden[p.Connection] || !c.Connection.Enabled {
					v.AffectedRoles = append(v.AffectedRoles, role)
					v.Health = "suspended"
				}
			}
		}
	}
	sort.Strings(v.AffectedRoles)
	if m.state.Stage != nil && m.now().Before(m.state.Stage.ExpiresAt) {
		s := m.stageViewLocked(m.state.Stage)
		v.Stage = &s
	}
	return v
}

// Snapshot returns an isolated, nonsecret view of the current configuration.
func (m *Manager) Snapshot() View { m.mu.Lock(); defer m.mu.Unlock(); return m.snapshotLocked() }

func (m *Manager) prepareLocked(settings models.Settings, mutations []ConnectionMutation, bases ...map[string]secretConnection) (map[string]secretConnection, []models.FieldError) {
	var errs []models.FieldError
	add := func(path, code, message string) {
		errs = append(errs, models.FieldError{Path: path, Code: code, Message: message})
	}
	if raw, marshalErr := json.Marshal(settings); marshalErr != nil || len(raw) > models.MaxSettingsBytes {
		add("settings", "payload_too_large", "Settings exceed the size limit")
	}
	errs = append(errs, settings.Validate()...)
	if settings.Policy.Classifier.Enabled {
		add("policy.classifier.enabled", "evaluation_required", "New classifier activation requires a completed evaluation")
	}
	for _, ids := range settings.Policy.Fallbacks {
		if len(ids) > 0 {
			add("policy.fallbacks", "protocol_verification_required", "Fallbacks require verified continuation compatibility")
		}
	}
	if len(m.state.Denied) > 8192 || len(m.state.DeniedCredentials) > 8192 {
		add("connections", "deny_metadata_limit", "Credential history is full; new configuration requires maintenance")
	}
	connections := cloneState(m.state).Connections
	if len(bases) > 0 {
		if bases[0] == nil {
			connections = nil
		} else {
			// Match JSON map decoding: supplied entries override existing entries.
			maps.Copy(connections, cloneConnections(bases[0]))
		}
	}
	if len(mutations) > 5 {
		add("connections", "invalid_connection", "Too many connection mutations")
	}
	seen := map[string]bool{}
	for _, u := range mutations {
		path := "connections." + u.Provider
		if seen[u.Provider] {
			add(path, "duplicate_connection", "Only one mutation per connection is allowed")
			continue
		}
		seen[u.Provider] = true
		if !models.ValidProvider(u.Provider) {
			add(path, "invalid_connection", "Unknown provider")
			continue
		}
		if u.Provider != "deepseek" && u.Provider != "zai" {
			add(path, "unsupported_mutation", "New connection identities currently support official DeepSeek and Z.ai APIs")
			continue
		}
		old, exists := connections[u.Provider]
		endpoint := u.Endpoint
		if endpoint == "" && exists {
			endpoint = old.Connection.Endpoint
		}
		canonical, err := llm.OfficialModelEndpoint(u.Provider, endpoint)
		if err != nil {
			add(path+".endpoint", "untrusted_endpoint", "Use the standard official HTTPS API endpoint")
			continue
		}
		key := old.Secret
		switch u.APIKeyAction {
		case "", "keep":
			if u.APIKey != "" {
				add(path+".api_key", "invalid_credential_action", "Use replace when supplying a new credential")
			}
		case "replace":
			if strings.TrimSpace(u.APIKey) == "" || strings.Contains(u.APIKey, "****") || len(u.APIKey) > 4096 || strings.ContainsAny(u.APIKey, "\x00\r\n") {
				add(path+".api_key", "invalid_credential", "Use a nonempty real API credential")
			} else {
				key = u.APIKey
			}
		default:
			add(path+".api_key_action", "invalid_credential_action", "Use keep or replace; revocation is a separate action")
		}
		if _, revoked := m.state.DeniedCredentials[secretIdentity(u.Provider, key, m.state.IdentitySalt)]; revoked {
			add(path+".api_key", "credential_revoked", "Supply a fresh credential; a revoked credential cannot be reused")
			continue
		}
		if key == "" {
			add(path+".api_key", "credential_required", "A provider API credential is required")
		}
		current, hasCurrent := m.state.Connections[u.Provider]
		if hasCurrent && current.Connection.Source == "environment" && (key != current.Secret || canonical != current.Connection.Endpoint) {
			add(path, "environment_override", "Clear the environment override before replacing this connection")
			continue
		}
		if exists && canonical != old.Connection.Endpoint && u.APIKeyAction != "replace" {
			add(path+".api_key", "credential_origin_changed", "Supply a credential explicitly for the new endpoint")
			continue
		}
		conn := Connection{Provider: u.Provider, Endpoint: canonical, Source: "encrypted_store", Enabled: true}
		if exists && old.Secret == key && old.Connection.Endpoint == canonical && old.Connection.Enabled {
			conn.Generation = old.Connection.Generation
			conn.Source = old.Connection.Source
		} else {
			conn.Generation = randomID()
		}
		connections[u.Provider] = secretConnection{Connection: conn, Secret: key}
	}
	assigned := map[string]bool{}
	for _, id := range settings.Policy.Roles() {
		if id != "" {
			assigned[id] = true
		}
	}
	for _, id := range settings.Policy.LegacyProfileHints {
		if id != "" {
			assigned[id] = true
		}
	}
	for _, p := range settings.Profiles {
		c, exists := connections[p.Connection]
		if !exists {
			if !assigned[p.ID] {
				continue
			}
			add("profiles."+p.ID+".connection", "credential_required", "The connection is not configured")
			continue
		}
		if _, blocked := m.state.Denied[c.Connection.Generation]; blocked || !c.Connection.Enabled {
			if !assigned[p.ID] {
				continue
			}
			add("profiles."+p.ID+".connection", "credential_revoked", "Create and test a fresh credential generation")
			continue
		}
		built, err := m.factory(p, c.handle())
		if (err != nil || built == nil) && assigned[p.ID] {
			add("profiles."+p.ID, "client_build_failed", "Could not construct this model client")
		}
	}
	return connections, errs
}

// Validate checks a draft locally without committing or contacting providers.
func (m *Manager) Validate(ctx context.Context, actor string, settings models.Settings, mutations []ConnectionMutation) []models.FieldError {
	if err := m.authorizeActor(ctx, actor); err != nil {
		return []models.FieldError{{Path: "authorization", Code: "admin_required", Message: "Administrator authorization is required"}}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	base := m.state.Connections
	if m.state.Stage != nil && m.state.Stage.Actor == actor && m.now().Before(m.state.Stage.ExpiresAt) {
		base = m.state.Stage.Connections
	}
	_, errs := m.prepareLocked(settings, mutations, base)
	if m.cipher == nil {
		errs = append(errs, models.FieldError{Path: "connections", Code: "secret_encryption_unavailable", Message: "Enable configuration encryption before changing models"})
	}
	return errs
}

// Stage saves a bounded candidate while preserving active credentials and policy.
func (m *Manager) Stage(ctx context.Context, actor string, expected uint64, settings models.Settings, mutations []ConnectionMutation) (StageView, error) {
	if err := m.authorizeActor(ctx, actor); err != nil {
		return StageView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createStageLocked(ctx, actor, expected, settings, mutations)
}

func (m *Manager) createStageLocked(ctx context.Context, actor string, expected uint64, settings models.Settings, mutations []ConnectionMutation) (StageView, error) {
	if err := m.checkRevisionLocked(expected); err != nil {
		return StageView{}, err
	}
	if m.cipher == nil {
		return StageView{}, failure("secret_encryption_unavailable", "Enable configuration encryption before changing models", 422)
	}
	base := m.state.Connections
	evidence := m.state.Evidence
	if current := m.state.Stage; current != nil && m.now().Before(current.ExpiresAt) {
		if current.Actor != actor {
			return StageView{}, failure("stage_busy", "Another administrator has a configuration saved for testing", 409)
		}
		for id := range m.state.Probes {
			record := m.state.Probes[id]
			if record.View.StageID == current.ID && (record.View.Status == "running" || record.View.Status == "cancel_requested") {
				return StageView{}, failure("stage_busy", "Wait for or cancel the running check before changing its configuration", 409)
			}
		}
		if current.BaseRevision != expected {
			return StageView{}, failure("stage_conflict", "The active configuration changed", 409)
		}
		base = current.Connections
		evidence = current.Evidence
	}
	connections, errs := m.prepareLocked(settings, mutations, base)
	if len(errs) > 0 {
		return StageView{}, &Error{Code: "invalid_settings", Message: "Review the highlighted model settings", FieldErrors: errs, HTTPStatus: 422}
	}
	candidate := cloneState(m.state)
	now := m.now().UTC()
	s := &stageState{ID: randomID(), Actor: actor, BaseRevision: expected, CreatedAt: now, ExpiresAt: now.Add(StageTTL), Settings: cloneSettings(settings), Connections: connections, Evidence: cloneEvidence(evidence)}
	public := make([]Connection, 0, len(connections))
	for _, provider := range sortedConnections(connections) {
		c := connections[provider].Connection
		c.Source = "" // ownership metadata does not change protocol identity
		public = append(public, c)
	}
	s.ConfigHash = hashJSON(struct {
		Settings    models.Settings
		Connections []Connection
	}{s.Settings, public})
	candidate.Stage = s
	if err := m.persistLocked(ctx, candidate, actor); err != nil {
		return StageView{}, err
	}
	m.state = candidate
	return m.stageViewLocked(s), nil
}
func (m *Manager) stageLocked(actor, id string) (*stageState, error) {
	s := m.state.Stage
	if s == nil || s.ID != id {
		return nil, failure("stage_conflict", "The saved test configuration is no longer current", 409)
	}
	if s.Actor != actor {
		return nil, failure("stage_owner_required", "This test configuration belongs to another administrator", 403)
	}
	if !m.now().Before(s.ExpiresAt) {
		return nil, failure("stage_expired", "Save a fresh test configuration", 410)
	}
	if s.BaseRevision != m.state.Revision {
		return nil, failure("stage_conflict", "The active configuration changed", 409)
	}
	return s, nil
}
func (m *Manager) eligibilityLocked(p models.Profile, c secretConnection, evidence map[string]Evidence) string {
	if p.Connection == "deepseek" && p.Thinking == "enabled" {
		return "experimental"
	}
	fp := profileFingerprint(p, c.Connection)
	if fp == m.state.LegacyProfiles[p.ID] && fp != "" && (p.Connection == "claude" || p.Connection == "openai" || p.Connection == "ollama") {
		return "legacy_preserved"
	}
	for _, kind := range []string{"text", "tools"} {
		e, ok := evidence[kind]
		if !ok || !e.Passed || e.Fingerprint != fp || e.FixtureVersion != FixtureVersion || e.CompatibilityVersion != models.CompatibilityVersion(p.Connection, p.Model) {
			return "experimental"
		}
	}
	return "production_eligible"
}
func (m *Manager) buildBindingsLocked(settings models.Settings, connections map[string]secretConnection, evidence map[string]map[string]Evidence, stageID string) (map[string]llm.ProfileBinding, error) {
	out := map[string]llm.ProfileBinding{}
	for _, p := range settings.Profiles {
		c, ok := connections[p.Connection]
		if !ok {
			out[p.ID] = llm.ProfileBinding{Profile: p, Eligibility: "experimental"}
			continue
		}
		var provider llm.Provider
		var err error
		if !c.Connection.Enabled {
			provider = suspendedProvider{}
		} else {
			provider, err = m.factory(p, c.handle())
		}
		if err != nil || provider == nil {
			assigned := false
			for _, id := range settings.Policy.Roles() {
				if id == p.ID {
					assigned = true
				}
			}
			for _, id := range settings.Policy.LegacyProfileHints {
				if id == p.ID {
					assigned = true
				}
			}
			if assigned {
				return nil, failure("client_build_failed", "Could not construct a model client", 422)
			}
			out[p.ID] = llm.ProfileBinding{Profile: p, Eligibility: "experimental"}
			continue
		}
		b := llm.ProfileBinding{Profile: p, Provider: &guardedProvider{manager: m, inner: provider, provider: p.Connection, generation: c.Connection.Generation, stageID: stageID}, Eligibility: m.eligibilityLocked(p, c, evidence[p.ID])}
		if d, known := models.Lookup(p.Connection, p.Model); known {
			e := evidence[p.ID]["vision"]
			b.Images = d.Images && e.Passed && e.Fingerprint == profileFingerprint(p, c.Connection) && validVisionFixtureVersion(e.FixtureVersion) && e.CompatibilityVersion == models.CompatibilityVersion(p.Connection, p.Model)
			b.ContextTokens = d.ContextTokens
		} else if p.Connection == "claude" {
			e := evidence[p.ID]["vision"]
			b.Images = e.Passed && e.Fingerprint == profileFingerprint(p, c.Connection) && validVisionFixtureVersion(e.FixtureVersion) && e.CompatibilityVersion == models.CompatibilityVersion(p.Connection, p.Model) || b.Eligibility == "legacy_preserved" && legacyClaudeImages(p.Model)
			b.ContextTokens = 200_000
		}
		out[p.ID] = b
	}
	return out, nil
}

// Activate verifies and commits a staged policy at the expected revision.
func (m *Manager) Activate(ctx context.Context, actor string, expected uint64, stageID string) (View, error) {
	if err := m.authorizeActor(ctx, actor); err != nil {
		return View{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevisionLocked(expected); err != nil {
		return View{}, err
	}
	s, err := m.stageLocked(actor, stageID)
	if err != nil {
		return View{}, err
	}
	for provider, current := range m.state.Connections {
		if current.Connection.Source == "environment" {
			next, ok := s.Connections[provider]
			if !ok || next.Secret != current.Secret || next.Connection.Endpoint != current.Connection.Endpoint {
				return View{}, failure("environment_override", "The environment owns this connection; clear its override first", 422)
			}
		}
	}
	if s.Settings.Policy.Everyday == "" {
		return View{}, failure("everyday_required", "Assign a verified everyday model before activation", 422)
	}
	if s.Settings.Policy.Classifier.Enabled {
		return View{}, failure("evaluation_required", "Classifier activation requires evaluation", 422)
	}
	b, err := m.buildBindingsLocked(s.Settings, s.Connections, s.Evidence, "")
	if err != nil {
		return View{}, err
	}
	for role, id := range s.Settings.Policy.Roles() {
		if id == "" {
			continue
		}
		binding := b[id]
		if binding.Eligibility != "production_eligible" && binding.Eligibility != "legacy_preserved" {
			return View{}, failure("verification_required", "Assigned profiles require successful text and tool checks; DeepSeek thinking needs its replay gate", 422)
		}
		if role == "vision" {
			e := s.Evidence[id]["vision"]
			c := s.Connections[binding.Profile.Connection]
			legacyVision := binding.Eligibility == "legacy_preserved" && binding.Profile.Connection == "claude"
			verifiedVision := e.Passed && e.Fingerprint == profileFingerprint(binding.Profile, c.Connection)
			if !binding.Images || !legacyVision && !verifiedVision {
				return View{}, failure("vision_verification_required", "The vision role requires a successful image check", 422)
			}
		}
	}
	for _, id := range s.Settings.Policy.LegacyProfileHints {
		if b[id].Eligibility != "production_eligible" && b[id].Eligibility != "legacy_preserved" {
			return View{}, failure("verification_required", "Custom route targets require successful text and tool checks", 422)
		}
	}
	// Construct the entire routing snapshot before committing any credential.
	preparedSnapshot, snapshotErr := llm.NewProfileSnapshot(expected+1, s.Settings, b)
	if snapshotErr != nil {
		return View{}, failure("invalid_settings", "The proposed routing policy cannot be activated", 422)
	}
	candidate := cloneState(m.state)
	if m.state.Settings.Policy.Everyday != "" {
		candidate.History = append(candidate.History, HistoryView{Revision: m.state.Revision, Settings: cloneSettings(m.state.Settings), CreatedAt: m.now().UTC()})
		if len(candidate.History) > 3 {
			candidate.History = candidate.History[len(candidate.History)-3:]
		}
	}
	candidate.Revision++
	candidate.Settings = cloneSettings(s.Settings)
	candidate.Connections = cloneConnections(s.Connections)
	referenced := map[string]bool{}
	for _, profile := range s.Settings.Profiles {
		referenced[profile.Connection] = true
	}
	for provider, c := range candidate.Connections {
		if referenced[provider] && c.Connection.Source != "environment" {
			c.Connection.Source = "encrypted_store"
			candidate.Connections[provider] = c
		}
	}
	candidate.Evidence = cloneEvidence(s.Evidence)
	candidate.Stage = nil
	for provider, old := range m.state.Connections {
		next, ok := candidate.Connections[provider]
		if !ok || next.Connection.Generation != old.Connection.Generation {
			candidate.Denied[old.Connection.Generation] = m.now().UTC()
			if old.Secret != "" {
				candidate.DeniedCredentials[secretIdentity(provider, old.Secret, candidate.IdentitySalt)] = m.now().UTC()
			}
		}
	}
	if err := m.persistLocked(ctx, candidate, actor); err != nil {
		return View{}, err
	}
	m.state = candidate
	m.bindings = b
	m.cancelDeniedLocked()
	if m.publisher != nil {
		if err := m.publisher(preparedSnapshot); err != nil {
			m.activationStatus = "failed"
			return m.snapshotLocked(), failure("activation_failed", "Settings were committed but routing publication failed", 503)
		}
	}
	m.activeRevision = m.state.Revision
	m.activationStatus = "applied"
	return m.snapshotLocked(), nil
}

// SetPublisher serializes routing publication with activation. The callback
// must only publish its supplied snapshot, and must not call Manager methods.
func (m *Manager) SetPublisher(publish func(*llm.ProfileSnapshot) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publisher = publish
	if publish == nil || m.state.Settings.Policy.Everyday == "" {
		return nil
	}
	snapshot, err := llm.NewProfileSnapshot(m.state.Revision, m.state.Settings, m.bindings)
	if err != nil {
		m.activationStatus = "failed"
		return failure("activation_failed", "Stored routing policy cannot be constructed", 503)
	}
	if err := publish(snapshot); err != nil {
		m.activationStatus = "failed"
		return failure("activation_failed", "Stored routing policy could not be published", 503)
	}
	m.activeRevision = m.state.Revision
	m.activationStatus = "applied"
	return nil
}

// Bindings returns isolated settings and immutable guarded profile clients.
func (m *Manager) Bindings() (settings models.Settings, bindings map[string]llm.ProfileBinding, revision uint64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]llm.ProfileBinding, len(m.bindings))
	for k := range m.bindings {
		out[k] = m.bindings[k]
	}
	return cloneSettings(m.state.Settings), out, m.state.Revision, nil
}

// Discard removes the actor-owned stage and cancels its outstanding checks.
func (m *Manager) Discard(ctx context.Context, actor string, expected uint64, stageID string) error {
	if err := m.authorizeActor(ctx, actor); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevisionLocked(expected); err != nil {
		return err
	}
	s, err := m.stageLocked(actor, stageID)
	if err != nil {
		return err
	}
	candidate := cloneState(m.state)
	candidate.Stage = nil
	if err := m.persistLocked(ctx, candidate, actor); err != nil {
		return err
	}
	m.state = candidate
	for id := range m.state.Probes {
		r := m.state.Probes[id]
		if r.View.StageID == s.ID {
			if cancel := m.probeCancel[id]; cancel != nil {
				cancel()
			}
		}
	}
	return nil
}

// Revoke durably denies a credential generation without requiring a replacement.
func (m *Manager) Revoke(ctx context.Context, actor string, expected uint64, provider, generation string) (View, error) {
	if err := m.authorizeActor(ctx, actor); err != nil {
		return View{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevisionLocked(expected); err != nil {
		return View{}, err
	}
	c, ok := m.state.Connections[provider]
	if !ok || c.Connection.Generation != generation {
		return View{}, failure("generation_conflict", "The connection generation changed", 409)
	}
	candidate := cloneState(m.state)
	candidate.Revision++
	candidate.Denied[generation] = m.now().UTC()
	if c.Secret != "" {
		candidate.DeniedCredentials[secretIdentity(provider, c.Secret, candidate.IdentitySalt)] = m.now().UTC()
	}
	c.Connection.Enabled = false
	candidate.Connections[provider] = c
	candidate.Stage = nil
	if err := m.persistLocked(ctx, candidate, actor); err != nil {
		return View{}, err
	}
	m.state = candidate
	m.cancelDeniedLocked()
	if m.publisher != nil && m.state.Settings.Policy.Everyday != "" {
		snapshot, buildErr := llm.NewProfileSnapshot(m.state.Revision, m.state.Settings, m.bindings)
		if buildErr != nil || m.publisher(snapshot) != nil {
			m.activationStatus = "failed"
			return m.snapshotLocked(), nil
		}
	}
	m.activeRevision = m.state.Revision
	m.activationStatus = "applied"
	return m.snapshotLocked(), nil
}
func (m *Manager) cancelDeniedLocked() {
	forbidden := map[string]bool{}
	for _, provider := range m.state.Settings.Policy.ForbiddenProviders {
		forbidden[provider] = true
	}
	for _, a := range m.admissions {
		if _, denied := m.state.Denied[a.generation]; denied || forbidden[a.provider] {
			a.cancel()
		}
	}
	for id := range m.state.Probes {
		r := m.state.Probes[id]
		if _, denied := m.state.Denied[r.Generation]; denied || forbidden[r.Provider] || r.View.StageID != "" && (m.state.Stage == nil || r.View.StageID != m.state.Stage.ID) {
			if cancel := m.probeCancel[id]; cancel != nil {
				cancel()
			}
		}
	}
}
func (m *Manager) cleanupLocked() bool {
	changed := false
	now := m.now()
	if m.state.Stage != nil && !now.Before(m.state.Stage.ExpiresAt) {
		m.state.Stage = nil
		changed = true
	}
	for id := range m.state.Probes {
		r := m.state.Probes[id]
		if !now.Before(r.ExpiresAt) {
			delete(m.state.Probes, id)
			changed = true
		}
	}
	return changed
}

func (m *Manager) admit(ctx context.Context, provider, generation, stageID string) (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, failure("runtime_stopped", "Model runtime is stopping", 503)
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if _, denied := m.state.Denied[generation]; denied {
		return nil, nil, failure("credential_revoked", "The model credential has been revoked", 403)
	}
	for _, p := range m.state.Settings.Policy.ForbiddenProviders {
		if p == provider {
			return nil, nil, failure("forbidden_provider", "This model provider is forbidden", 403)
		}
	}
	if stageID != "" {
		s := m.state.Stage
		if s == nil || s.ID != stageID || s.BaseRevision != m.state.Revision || !m.now().Before(s.ExpiresAt) || s.Connections[provider].Connection.Generation != generation {
			return nil, nil, failure("stage_conflict", "The test configuration is no longer current", 409)
		}
	} else {
		c, ok := m.state.Connections[provider]
		if !ok || !c.Connection.Enabled || c.Connection.Generation != generation {
			return nil, nil, failure("credential_revoked", "The model credential is no longer active", 403)
		}
	}
	admitted, cancel := context.WithCancel(ctx)
	id := randomID()
	m.admissions[id] = admission{provider, generation, cancel}
	m.activity.Add(1)
	release := func() { cancel(); m.mu.Lock(); delete(m.admissions, id); m.mu.Unlock(); m.activity.Done() }
	return admitted, release, nil
}

type guardedProvider struct {
	manager                       *Manager
	inner                         llm.Provider
	provider, generation, stageID string
}

// Complete checks and registers credential admission before invoking the adapter.
func (p *guardedProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	admitted, release, err := p.manager.admit(ctx, p.provider, p.generation, p.stageID)
	if err != nil {
		return llm.Response{}, err
	}
	defer release()
	return p.inner.Complete(admitted, req)
}

// CompleteStream guards streaming admission with the same revocation boundary.
func (p *guardedProvider) CompleteStream(ctx context.Context, req llm.Request, callback llm.StreamCallback) (llm.Response, error) {
	admitted, release, err := p.manager.admit(ctx, p.provider, p.generation, p.stageID)
	if err != nil {
		return llm.Response{}, err
	}
	defer release()
	if streaming, ok := p.inner.(llm.StreamingProvider); ok {
		return streaming.CompleteStream(admitted, req, callback)
	}
	response, err := p.inner.Complete(admitted, req)
	if err == nil && callback != nil {
		callback(response.Content)
	}
	return response, err
}

// BindLegacy guards one already-configured vendor adapter. Call before retry,
// fallback and cache wrappers, and bind Claude Haiku with provider="claude".
func (m *Manager) BindLegacy(provider string, inner llm.Provider) (llm.Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.state.Connections[provider]
	if !ok || inner == nil {
		return nil, failure("credential_required", "A seeded connection is required for legacy admission", 422)
	}
	return &guardedProvider{manager: m, inner: inner, provider: provider, generation: c.Connection.Generation}, nil
}

// Close stops local admissions and marks unfinished probes as interrupted. It
// does not promise cancellation of already accepted upstream work or billing.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	candidate := cloneState(m.state)
	changed := false
	for id := range candidate.Probes {
		r := candidate.Probes[id]
		if r.View.Status == "running" || r.View.Status == "cancel_requested" {
			r.View.Status = "interrupted_unknown"
			r.View.ErrorCode = "shutdown_interrupted"
			r.View.Result = nil
			candidate.Probes[id] = r
			changed = true
		}
	}
	var persistErr error
	if changed {
		persistErr = m.persistLocked(ctx, candidate, "shutdown")
	}
	m.closed = true
	for _, a := range m.admissions {
		a.cancel()
	}
	for _, cancel := range m.probeCancel {
		cancel()
	}
	if changed {
		m.state = candidate
	}
	return persistErr
}

type suspendedProvider struct{}

// Complete rejects invocations of a suspended connection.
func (suspendedProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, failure("credential_revoked", "The model connection is suspended", 403)
}

// History returns the bounded nonsecret policy history after fresh admin auth.
func (m *Manager) History(ctx context.Context, actor string) ([]HistoryView, error) {
	if err := m.authorizeActor(ctx, actor); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]HistoryView, len(m.state.History))
	for i := range m.state.History {
		entry := &m.state.History[i]
		out[i] = HistoryView{Revision: entry.Revision, Settings: cloneSettings(entry.Settings), CreatedAt: entry.CreatedAt}
	}
	return out, nil
}

// StageHistory prepares a restore with current credentials and normal evidence
// gates. It never restores a key, clears a deny, removes current provider bans,
// or replaces somebody's pending credential draft.
func (m *Manager) StageHistory(ctx context.Context, actor string, expected, target uint64) (StageView, error) {
	if err := m.authorizeActor(ctx, actor); err != nil {
		return StageView{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkRevisionLocked(expected); err != nil {
		return StageView{}, err
	}
	if m.state.Stage != nil && m.now().Before(m.state.Stage.ExpiresAt) {
		return StageView{}, failure("stage_busy", "Finish or discard the saved test configuration before preparing a restore", 409)
	}
	var settings models.Settings
	found := false
	for i := range m.state.History {
		entry := &m.state.History[i]
		if entry.Revision == target {
			settings = cloneSettings(entry.Settings)
			found = true
			break
		}
	}
	if !found {
		return StageView{}, failure("history_not_found", "This policy revision is no longer retained", 404)
	}
	forbidden := map[string]bool{}
	for _, provider := range settings.Policy.ForbiddenProviders {
		forbidden[provider] = true
	}
	for _, provider := range m.state.Settings.Policy.ForbiddenProviders {
		if !forbidden[provider] {
			settings.Policy.ForbiddenProviders = append(settings.Policy.ForbiddenProviders, provider)
			forbidden[provider] = true
		}
	}
	return m.createStageLocked(ctx, actor, expected, settings, nil)
}

// SyncEnvironmentOwnership updates only source metadata at startup. Removing
// an environment override preserves the encrypted credential and generation;
// SeedConnections separately verifies every still-effective environment key.
func (m *Manager) SyncEnvironmentOwnership(ctx context.Context, owners map[string]bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return failure("runtime_stopped", "Model runtime is stopping", 503)
	}
	candidate := cloneState(m.state)
	changed := false
	syncConnections := func(connections map[string]secretConnection) {
		for provider, c := range connections {
			source := normalizeSource(c.Connection.Source)
			if owners[provider] {
				source = "environment"
			} else if source == "environment" {
				source = "encrypted_store"
			}
			if source != c.Connection.Source {
				c.Connection.Source = source
				connections[provider] = c
				changed = true
			}
		}
	}
	syncConnections(candidate.Connections)
	if candidate.Stage != nil {
		syncConnections(candidate.Stage.Connections)
	}
	if !changed {
		return nil
	}
	if err := m.persistLocked(ctx, candidate, "startup"); err != nil {
		return err
	}
	m.state = candidate
	return nil
}

// Wait waits for admitted calls and probe finalization after Close has prevented
// new registrations. Call before shutting down usage observers or storage.
func (m *Manager) Wait(ctx context.Context) error {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if !closed {
		return failure("runtime_not_stopped", "Close the model runtime before waiting", 409)
	}
	done := make(chan struct{})
	go func() { m.activity.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LegacyConnection returns the sealed active handle for trusted Go bootstrap.
// Never expose this handle through a dashboard response or log it.
func (m *Manager) LegacyConnection(provider string) (Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.state.Connections[provider]
	if !ok {
		return Connection{}, failure("credential_required", "A seeded connection is required", 422)
	}
	return c.handle(), nil
}

// LegacySettings returns the immutable initial nonsecret migration baseline.
func (m *Manager) LegacySettings() (models.Settings, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneSettings(m.state.LegacyBaseline), m.state.LegacySeeded
}

func legacyClaudeImages(model string) bool {
	// The existing Anthropic adapter supports images on these configured families.
	return strings.HasPrefix(model, "claude-sonnet-") || strings.HasPrefix(model, "claude-opus-") || strings.HasPrefix(model, "claude-haiku-") || strings.HasPrefix(model, "claude-3-")
}

// SeedLegacyProfiles records an exact, one-time migration grant for existing
// Claude/OpenAI/Ollama profiles. This is trusted startup input, never an API action.
// It does not fabricate probe evidence or grant new vendor profiles.
func (m *Manager) SeedLegacyProfiles(ctx context.Context, settings models.Settings, changedProviders ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return failure("runtime_stopped", "Model runtime is stopping", 503)
	}
	if m.state.LegacySeeded {
		return nil
	}
	if m.state.Settings.Policy.Everyday != "" || m.state.Stage != nil {
		return failure("legacy_seed_conflict", "Legacy migration must precede managed configuration", 409)
	}
	if errs := settings.Validate(); len(errs) != 0 {
		return failure("invalid_legacy_settings", "Legacy settings cannot be migrated", 422)
	}
	if raw, marshalErr := json.Marshal(settings); marshalErr != nil || len(raw) > models.MaxSettingsBytes {
		return failure("invalid_legacy_settings", "Legacy settings exceed the limit", 422)
	}
	candidate := cloneState(m.state)
	candidate.LegacyProfiles = map[string]string{}
	changed := map[string]bool{}
	for _, provider := range changedProviders {
		changed[provider] = true
	}
	for _, profile := range settings.Profiles {
		if changed[profile.Connection] || profile.Connection != "claude" && profile.Connection != "openai" && profile.Connection != "ollama" {
			continue
		}
		c, ok := candidate.Connections[profile.Connection]
		if !ok || !c.Connection.Enabled || (normalizeSource(c.Connection.Source) != "legacy_effective" && normalizeSource(c.Connection.Source) != "environment") {
			continue
		}
		if _, denied := candidate.Denied[c.Connection.Generation]; denied {
			continue
		}
		if _, denied := candidate.DeniedCredentials[secretIdentity(profile.Connection, c.Secret, candidate.IdentitySalt)]; denied && c.Secret != "" {
			continue
		}
		candidate.LegacyProfiles[profile.ID] = profileFingerprint(profile, c.Connection)
	}
	candidate.LegacySeeded = true
	candidate.LegacyBaseline = cloneSettings(settings)
	if err := m.persistLocked(ctx, candidate, "legacy_migration"); err != nil {
		return err
	}
	m.state = candidate
	return nil
}

// Both fixtures use the same strict image-only nonce/color oracle. Prior v1
// successes remain valid; v2 only makes the raster label easier to read.
func validVisionFixtureVersion(version string) bool {
	return version == FixtureVersion || version == VisionFixtureVersion
}
