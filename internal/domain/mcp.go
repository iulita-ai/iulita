package domain

import (
	"time"

	"github.com/uptrace/bun"
)

// MCPRecord is an encrypted service-owned document. Scope and CAS metadata are
// queryable; endpoints, catalogs, credentials and arguments never are plaintext.
type MCPRecord struct {
	bun.BaseModel `bun:"table:mcp_records"`
	ID            string `bun:"id,pk"`
	OwnerID       string `bun:"owner_id,notnull"`
	Kind          string `bun:"kind,notnull"`
	Revision      int64  `bun:"revision,notnull"`
	Payload       string `bun:"payload,notnull" json:"-"`
	ActorID       string `bun:"-" json:"-"`
	Action        string `bun:"-" json:"-"`
	State         string `bun:"-" json:"-"`
}

// MCPRecordEvent is safe append-only metadata, committed with the corresponding
// encrypted record update. Raw content and endpoints are never audit fields.
type MCPRecordEvent struct {
	bun.BaseModel `bun:"table:mcp_record_events"`
	ID            int64     `bun:"id,pk,autoincrement" json:"id"`
	OwnerID       string    `bun:"owner_id,notnull" json:"-"`
	ActorID       string    `bun:"actor_id,notnull" json:"actor_id"`
	RecordID      string    `bun:"record_id,notnull" json:"record_id"`
	Kind          string    `bun:"kind,notnull" json:"kind"`
	Action        string    `bun:"action,notnull,default:''" json:"action"`
	State         string    `bun:"state,notnull,default:''" json:"state"`
	Revision      int64     `bun:"revision,notnull" json:"revision"`
	CreatedAt     time.Time `bun:"created_at,notnull" json:"created_at"`
}
