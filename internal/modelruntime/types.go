// Package modelruntime coordinates the encrypted model configuration lifecycle.
// Its repository is single-writer: one Manager per SQLite-backed process. All
// credential, policy, deny and probe-marker changes commit as one encrypted row.
package modelruntime

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/models"
)

// StateKey identifies the encrypted runtime configuration row.
const StateKey = "models.runtime"

// StageTTL bounds staged credentials and probe idempotency markers.
const StageTTL = 30 * time.Minute

// ProbeDeadline bounds each asynchronous provider check.
const ProbeDeadline = 90 * time.Second

// FixtureVersion identifies the stable text and tool probe contract.
const FixtureVersion = "model-probe-v1"

// VisionFixtureVersion identifies the enlarged image probe raster.
const VisionFixtureVersion = "model-vision-probe-v2"

// ClassifierFixtureVersion binds quality evidence to the prompt and fixtures.
const ClassifierFixtureVersion = llm.ClassifierPromptVersion + "-eval-v1"
const maxStateBytes = 4 << 20
const maxProbeRecords = 128

// Repository persists the single encrypted runtime state row.
type Repository interface {
	GetConfigOverride(context.Context, string) (*domain.ConfigOverride, error)
	SaveConfigOverride(context.Context, *domain.ConfigOverride) error
}

// Cipher encrypts and decrypts runtime state at rest.
type Cipher interface {
	Encrypt(string) (string, error)
	Decrypt(string) (string, error)
}

// Factory creates a single-attempt adapter. Retry wrappers belong outside the
// guarded binding so every attempt goes through independent generation admission.
type Factory func(models.Profile, Connection) (llm.Provider, error)

// Authorize rechecks admin and password-change state. Nil fails closed for every
// user-facing operation. Actors are server-authenticated IDs, not body fields.
type Authorize func(context.Context, string) error

// Connection binds a provider origin to one credential generation.
type Connection struct {
	Provider   string `json:"provider"`
	Endpoint   string `json:"endpoint"`
	Generation string `json:"generation"`
	Source     string `json:"source"`
	Enabled    bool   `json:"enabled"`
	APIKey     string `json:"-"`
}

// ConnectionMutation supplies a write-only staged credential change.
type ConnectionMutation struct {
	Provider     string `json:"provider"`
	Endpoint     string `json:"endpoint,omitempty"`
	APIKeyAction string `json:"api_key_action"`    // keep or replace; clear uses revoke
	APIKey       string `json:"api_key,omitempty"` // write only; never returned
}

// ConnectionView exposes connection metadata without its secret.
type ConnectionView struct {
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint"`
	Generation    string `json:"generation"`
	Source        string `json:"source"`
	CredentialSet bool   `json:"credential_set"`
	Availability  string `json:"availability"`
}

// Evidence records a bounded, server-owned synthetic check result.
type Evidence struct {
	Kind                 string    `json:"kind"`
	Passed               bool      `json:"passed"`
	CheckedAt            time.Time `json:"checked_at"`
	RequestedModel       string    `json:"requested_model"`
	ServedModel          string    `json:"served_model"`
	Fingerprint          string    `json:"fingerprint"`
	FixtureVersion       string    `json:"fixture_version"`
	CatalogVersion       string    `json:"catalog_version"`
	CompatibilityVersion string    `json:"compatibility_version"`
	Usage                llm.Usage `json:"usage"`
	ErrorCode            string    `json:"error_code,omitempty"`
	ClassifierTimeoutMS  int       `json:"classifier_timeout_ms,omitempty"`
	Cases                int       `json:"cases,omitempty"`
	Correct              int       `json:"correct,omitempty"`
	ComplexCases         int       `json:"complex_cases,omitempty"`
	ComplexCorrect       int       `json:"complex_correct,omitempty"`
	MaxLatencyMS         int64     `json:"max_latency_ms,omitempty"`
}

// ProfileView exposes eligibility and evidence for one profile.
type ProfileView struct {
	ID          string     `json:"id"`
	Eligibility string     `json:"eligibility"`
	Fingerprint string     `json:"fingerprint"`
	Evidence    []Evidence `json:"evidence"`
}

// StageView exposes nonsecret details of a saved candidate.
type StageView struct {
	ID           string           `json:"id"`
	BaseRevision uint64           `json:"base_revision"`
	ConfigHash   string           `json:"config_hash"`
	CreatedAt    time.Time        `json:"created_at"`
	ExpiresAt    time.Time        `json:"expires_at"`
	Settings     models.Settings  `json:"settings"`
	Connections  []ConnectionView `json:"connections"`
	Profiles     []ProfileView    `json:"effective_profiles"`
}

// HistoryView stores only prior nonsecret policies. Credentials are always
// taken from the current encrypted state when preparing a restore stage.
type HistoryView struct {
	Revision  uint64          `json:"revision"`
	Settings  models.Settings `json:"settings"`
	CreatedAt time.Time       `json:"created_at"`
}

// View reports desired and active model configuration and health.
type View struct {
	Revision            uint64           `json:"revision"`
	ActiveRevision      uint64           `json:"active_revision"`
	Settings            models.Settings  `json:"settings"`
	Connections         []ConnectionView `json:"connections"`
	Profiles            []ProfileView    `json:"effective_profiles"`
	Stage               *StageView       `json:"stage,omitempty"`
	Health              string           `json:"health"`
	AffectedRoles       []string         `json:"affected_roles"`
	ActivationStatus    string           `json:"activation_status"`
	EncryptionAvailable bool             `json:"encryption_available"`
}

// ProbeRequest identifies an explicit, idempotent synthetic check.
type ProbeRequest struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	StageID          string `json:"stage_id,omitempty"`
	ProfileID        string `json:"profile_id"`
	Kind             string `json:"kind"`
	IdempotencyKey   string `json:"idempotency_key"`
}

// ProbeView reports the lifecycle of an asynchronous check.
type ProbeView struct {
	ID        string    `json:"id"`
	ProfileID string    `json:"profile_id"`
	StageID   string    `json:"stage_id,omitempty"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	Deadline  time.Time `json:"deadline"`
	Result    *Evidence `json:"result,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
}

// Error carries a sanitized error category and optional field diagnostics.
type Error struct {
	Code        string              `json:"code"`
	Message     string              `json:"message"`
	FieldErrors []models.FieldError `json:"field_errors,omitempty"`
	HTTPStatus  int                 `json:"-"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
func failure(code, message string, status int) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: status}
}
