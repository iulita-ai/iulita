package domain

import "time"

// SlackAccount stores the OWNER's Slack personal (xoxp-) OAuth user token used
// for reading/searching everything the owner can see (search:read etc.).
//
// Single-owner: at most one row exists in total. UNIQUE(user_id) enforces one row
// per user at the DB level; the connect handler additionally rejects a second
// user connecting, so the table holds a single canonical account (resolve it via
// GetAnySlackAccount).
//
// This is distinct from the per-channel bot token (xoxb-/xapp-) used by
// internal/channel/slack for conversation and posting — that lives in the
// channel instance config, not here.
//
// RefreshToken and TokenExpiry are optional: Slack user tokens do not expire
// unless the workspace has token rotation enabled, so a zero TokenExpiry means
// "non-expiring, never refresh".
type SlackAccount struct {
	ID                    int64     `bun:",pk,autoincrement" json:"id"`
	UserID                string    `bun:",notnull,unique" json:"user_id"` // owner iulita user id (UNIQUE — single account)
	SlackUserID           string    `bun:",notnull" json:"slack_user_id"`  // authed_user.id ("U...")
	TeamID                string    `bun:",notnull" json:"team_id"`        // team.id ("T...")
	TeamName              string    `bun:",notnull,default:''" json:"team_name"`
	EncryptedAccessToken  string    `bun:",notnull" json:"-"`             // authed_user.access_token ("xoxp-...")
	EncryptedRefreshToken string    `bun:",notnull,default:''" json:"-"`  // present only when token rotation is on
	TokenExpiry           time.Time `bun:",nullzero" json:"token_expiry"` // zero = non-expiring (rotation off)
	Scopes                string    `bun:",notnull" json:"scopes"`        // JSON array of granted scopes
	CreatedAt             time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"created_at"`
	UpdatedAt             time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"updated_at"`
}
