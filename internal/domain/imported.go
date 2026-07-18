package domain

import "time"

// ImportedConversation is an archived (read-only) Claude.ai conversation.
// The archive is intentionally isolated from the live chat_messages table so it
// never pollutes the assistant's active history or live retrieval.
type ImportedConversation struct {
	ID           int64     `bun:",pk,autoincrement" json:"id"`
	SourceUUID   string    `bun:",unique,notnull" json:"source_uuid"`      // conversations[].uuid — idempotency key
	UserID       string    `bun:",notnull,default:''" json:"user_id"`      // owning iulita user (admin)
	AccountUUID  string    `bun:",notnull,default:''" json:"account_uuid"` // conversations[].account.uuid
	Title        string    `bun:",notnull,default:''" json:"title"`        // conv.name (with fallback applied by mapper)
	Summary      string    `bun:",notnull,default:''" json:"summary"`      // conv.summary (may be empty)
	MessageCount int       `bun:",notnull,default:0" json:"message_count"`
	CreatedAt    time.Time `bun:",nullzero" json:"created_at"`
	UpdatedAt    time.Time `bun:",nullzero" json:"updated_at"`
	ImportedAt   time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"imported_at"`
}

// ImportedMessage is a single archived message. Content is immutable (append-only),
// so the FTS mirror needs only INSERT/DELETE triggers — no UPDATE trigger.
type ImportedMessage struct {
	ID                int64     `bun:",pk,autoincrement" json:"id"`
	SourceUUID        string    `bun:",unique,notnull" json:"source_uuid"`             // chat_messages[].uuid
	ConversationUUID  string    `bun:",notnull" json:"conversation_uuid"`              // → imported_conversations.source_uuid
	UserID            string    `bun:",notnull,default:''" json:"user_id"`             // owning iulita user (admin)
	Sender            string    `bun:",notnull" json:"sender"`                         // "human" | "assistant"
	Seq               int       `bun:",notnull,default:0" json:"seq"`                  // order within conversation (created_at asc)
	Content           string    `bun:",notnull" json:"content"`                        // reconstructed text + inlined attachment text
	ParentMessageUUID string    `bun:",notnull,default:''" json:"parent_message_uuid"` // raw parent — seam for branch reconstruction
	CreatedAt         time.Time `bun:",nullzero" json:"created_at"`
	ImportedAt        time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"imported_at"`
}

// ImportedFactKey is a sidecar dedup ledger for memories.json → facts. The facts
// table has no source_uuid column; we keep the idempotency key here to avoid
// touching the hot facts table (low blast radius).
type ImportedFactKey struct {
	SourceUUID string    `bun:",pk" json:"source_uuid"`
	FactID     int64     `bun:",notnull" json:"fact_id"`
	ImportedAt time.Time `bun:",nullzero,notnull,default:current_timestamp" json:"imported_at"`
}

// ImportRun is a durable summary of one import run. It is NOT garbage-collected by
// the scheduler (Task.Result is cleaned up by DeleteOldTasks), and it carries the
// last-progress fields needed to rehydrate a running job in the dashboard.
type ImportRun struct {
	ID              int64     `bun:",pk,autoincrement" json:"id"`
	JobID           string    `bun:",unique,notnull" json:"job_id"` // == task UniqueKey, for WS correlation
	UserID          string    `bun:",notnull,default:''" json:"user_id"`
	Status          string    `bun:",notnull" json:"status"` // running|done|failed|canceled
	SourceSHA       string    `bun:",notnull,default:''" json:"source_sha"`
	Conversations   int       `bun:",notnull,default:0" json:"conversations"`
	MessagesStored  int       `bun:",notnull,default:0" json:"messages_stored"`
	MessagesSkipped int       `bun:",notnull,default:0" json:"messages_skipped"`
	Facts           int       `bun:",notnull,default:0" json:"facts"`
	SkippedBinaries int       `bun:",notnull,default:0" json:"skipped_binaries"`
	ChunksEmbedded  int       `bun:",notnull,default:0" json:"chunks_embedded"`
	ParseErrors     int       `bun:",notnull,default:0" json:"parse_errors"`
	LastPhase       string    `bun:",notnull,default:''" json:"last_phase"` // for progress rehydrate
	LastDone        int       `bun:",notnull,default:0" json:"last_done"`
	LastTotal       int       `bun:",notnull,default:0" json:"last_total"`
	Error           string    `bun:",notnull,default:''" json:"error"` // scrubbed of payload
	StartedAt       time.Time `bun:",nullzero" json:"started_at"`
	FinishedAt      time.Time `bun:",nullzero" json:"finished_at"`
}
