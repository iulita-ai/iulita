package domain

import (
	"time"

	"github.com/uptrace/bun"
)

// HTTP standing-grant scopes.
const (
	HTTPStandingScopeOrigin = "origin" // mutations to one exact scheme+host+port
	HTTPStandingScopeGlobal = "global" // mutations to any origin
)

// HTTPStandingGrant is a user-issued standing approval for mutating
// http_request calls ("always allow this origin" / "always allow"). Large
// downloads over the consent threshold are NEVER covered — they always ask.
// Grants expire (origin 30d, global 7d by default) and are revocable via the
// dashboard admin API.
type HTTPStandingGrant struct {
	bun.BaseModel `bun:"table:http_standing_grants"`

	ID          int64      `bun:"id,pk,autoincrement" json:"id"`
	OwnerUserID string     `bun:"owner_user_id,notnull" json:"owner_user_id"`
	Scope       string     `bun:"scope,notnull" json:"scope"` // origin | global
	Scheme      string     `bun:"scheme,notnull,default:''" json:"scheme,omitempty"`
	Host        string     `bun:"host,notnull,default:''" json:"host,omitempty"`
	Port        string     `bun:"port,notnull,default:''" json:"port,omitempty"`
	Reason      string     `bun:"reason,notnull,default:'mutation'" json:"reason"`
	ExpiresAt   time.Time  `bun:"expires_at,notnull" json:"expires_at"`
	CreatedAt   time.Time  `bun:"created_at,nullzero,notnull,default:current_timestamp" json:"created_at"`
	CreatedBy   string     `bun:"created_by,notnull,default:''" json:"created_by"`
	RevokedAt   *time.Time `bun:"revoked_at" json:"revoked_at,omitempty"`
}
