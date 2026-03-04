package domain

import "time"

// ConfigOverride represents a runtime configuration override stored in the database.
type ConfigOverride struct {
	Key       string    `bun:",pk" json:"key"`
	Value     string    `bun:",notnull" json:"value"`
	Encrypted bool      `bun:",notnull,default:false" json:"encrypted"`
	UpdatedAt time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"updated_at"`
	UpdatedBy string    `bun:",notnull,default:''" json:"updated_by"`
}
