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

const StateKey = "models.runtime"
const StageTTL = 30 * time.Minute
const ProbeDeadline = 90 * time.Second
const FixtureVersion = "model-probe-v1"
const VisionFixtureVersion = "model-vision-probe-v2"
const maxStateBytes = 4 << 20
const maxProbeRecords = 128

type Repository interface {
	GetConfigOverride(context.Context, string) (*domain.ConfigOverride, error)
	SaveConfigOverride(context.Context, *domain.ConfigOverride) error
}
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

type Connection struct {
	Provider   string `json:"provider"`
	Endpoint   string `json:"endpoint"`
	Generation string `json:"generation"`
	Source     string `json:"source"`
	Enabled    bool   `json:"enabled"`
	APIKey     string `json:"-"`
}
type ConnectionMutation struct {
	Provider     string `json:"provider"`
	Endpoint     string `json:"endpoint,omitempty"`
	APIKeyAction string `json:"api_key_action"`    // keep or replace; clear uses revoke
	APIKey       string `json:"api_key,omitempty"` // write only; never returned
}
type ConnectionView struct {
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint"`
	Generation    string `json:"generation"`
	Source        string `json:"source"`
	CredentialSet bool   `json:"credential_set"`
	Availability  string `json:"availability"`
}
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
}
type ProfileView struct {
	ID          string     `json:"id"`
	Eligibility string     `json:"eligibility"`
	Fingerprint string     `json:"fingerprint"`
	Evidence    []Evidence `json:"evidence"`
}
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
type ProbeRequest struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	StageID          string `json:"stage_id,omitempty"`
	ProfileID        string `json:"profile_id"`
	Kind             string `json:"kind"`
	IdempotencyKey   string `json:"idempotency_key"`
}
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
