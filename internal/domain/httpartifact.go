package domain

import (
	"time"

	"github.com/uptrace/bun"
)

// HTTP artifact states.
const (
	HTTPArtifactPartial     = "partial"
	HTTPArtifactReady       = "ready"
	HTTPArtifactDeleting    = "deleting"
	HTTPArtifactUnavailable = "unavailable" // ready record without its file
)

// HTTPArtifact is the metadata row for one stored http_request download.
// The file bytes live under the artifact root (0700) with server-generated
// names; metadata never contains request URLs, credentials, or query
// strings (plan §9.2).
type HTTPArtifact struct {
	bun.BaseModel `bun:"table:http_artifacts"`

	ID              string     `bun:"id,pk" json:"id"` // server-generated opaque ID
	OwnerUserID     string     `bun:"owner_user_id,notnull" json:"owner_user_id"`
	ChatID          string     `bun:"chat_id,notnull,default:''" json:"chat_id"`
	StorageName     string     `bun:"storage_name,notnull" json:"-"` // basename inside the artifact root
	Size            int64      `bun:"size,notnull,default:0" json:"size"`
	SHA256          string     `bun:"sha256,notnull,default:''" json:"sha256"`
	MIME            string     `bun:"mime,notnull,default:''" json:"mime"`
	ContentEncoding string     `bun:"content_encoding,notnull,default:''" json:"content_encoding,omitempty"`
	Filename        string     `bun:"filename,notnull,default:'download'" json:"filename"`
	Sensitive       bool       `bun:"sensitive,notnull,default:true" json:"sensitive"`
	State           string     `bun:"state,notnull,default:'partial'" json:"state"`
	LeaseBoot       string     `bun:"lease_boot,notnull,default:''" json:"-"`
	LeaseExpiresAt  *time.Time `bun:"lease_expires_at" json:"-"`
	CreatedAt       time.Time  `bun:"created_at,nullzero,notnull,default:current_timestamp" json:"created_at"`
	ExpiresAt       time.Time  `bun:"expires_at,nullzero,notnull" json:"expires_at"`
}
