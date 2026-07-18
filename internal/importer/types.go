// Package importer maps a Claude.ai data export (conversations.json, memories.json)
// into iulita domain objects. It is pure — no database, HTTP, or scheduler
// dependencies — so every mapper is unit-testable against small fixtures.
//
// Scope (locked): memories.json → facts (live memory) and conversations.json → an
// isolated read-only archive. projects/ and design_chats/ are intentionally out of
// scope; the decode types leave a seam for adding them later.
package importer

const (
	// ImportChatID is the synthetic chat scope for imported memory facts (Fact.ChatID
	// is NOT NULL). It keeps imported facts groupable/removable without colliding with
	// a real channel chat ID.
	ImportChatID = "claude-import"

	// ImportSourceType marks facts that originated from a Claude export, so exports
	// and other egress paths can exclude them.
	ImportSourceType = "claude_import"
)

// dumpConversation is one element of conversations.json.
type dumpConversation struct {
	UUID         string        `json:"uuid"`
	Name         string        `json:"name"`
	Summary      string        `json:"summary"`
	CreatedAt    string        `json:"created_at"`
	UpdatedAt    string        `json:"updated_at"`
	Account      dumpAccount   `json:"account"`
	ChatMessages []dumpMessage `json:"chat_messages"`
}

type dumpAccount struct {
	UUID string `json:"uuid"`
}

type dumpMessage struct {
	UUID              string             `json:"uuid"`
	Text              string             `json:"text"`
	Sender            string             `json:"sender"` // "human" | "assistant"
	CreatedAt         string             `json:"created_at"`
	UpdatedAt         string             `json:"updated_at"`
	ParentMessageUUID string             `json:"parent_message_uuid"`
	Content           []dumpContentBlock `json:"content"`
	Attachments       []dumpAttachment   `json:"attachments"`
	Files             []dumpFile         `json:"files"`
}

// dumpContentBlock: only type=="text" carries user-facing text. thinking / tool_use /
// tool_result / token_budget blocks are deliberately ignored (never reconstructed).
type dumpContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// dumpAttachment carries extracted text for text-like uploads (inlined into content).
type dumpAttachment struct {
	FileName         string `json:"file_name"`
	FileType         string `json:"file_type"`
	FileSize         int64  `json:"file_size"`
	ExtractedContent string `json:"extracted_content"`
}

// dumpFile references a binary upload. The export ships only the name/uuid, never the
// bytes, so these are logged and skipped.
type dumpFile struct {
	FileUUID string `json:"file_uuid"`
	FileName string `json:"file_name"`
}

// dumpMemories is one element of memories.json (the file is an array of one).
type dumpMemories struct {
	AccountUUID         string            `json:"account_uuid"`
	ConversationsMemory string            `json:"conversations_memory"`
	ProjectMemories     map[string]string `json:"project_memories"`
}
