package domain

import "time"

// GoogleAccount stores OAuth2 tokens for a connected Google account.
type GoogleAccount struct {
	ID                    int64     `bun:",pk,autoincrement" json:"id"`
	UserID                string    `bun:",notnull" json:"user_id"`
	AccountEmail          string    `bun:",notnull" json:"account_email"`
	AccountAlias          string    `bun:",notnull,default:''" json:"account_alias"`
	IsDefault             bool      `bun:",notnull,default:false" json:"is_default"`
	EncryptedAccessToken  string    `bun:",notnull" json:"-"`
	EncryptedRefreshToken string    `bun:",notnull" json:"-"`
	TokenExpiry           time.Time `bun:",nullzero" json:"token_expiry"`
	Scopes                string    `bun:",notnull" json:"scopes"`
	CreatedAt             time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"created_at"`
	UpdatedAt             time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"updated_at"`
}
