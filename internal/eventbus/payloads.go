package eventbus

import "time"

// MessageReceivedPayload is published when a user message arrives.
type MessageReceivedPayload struct {
	ChatID   string
	UserID   string
	Text     string
	Language string
	Time     time.Time
}

// ResponseSentPayload is published after the assistant response is saved.
type ResponseSentPayload struct {
	ChatID   string
	Response string
	Time     time.Time
}

// SkillExecutedPayload is published after a skill/tool call completes.
type SkillExecutedPayload struct {
	ChatID     string
	UserID     string // iulita user UUID (empty for channels without a resolver)
	SkillName  string
	ToolCallID string
	Success    bool
	DurationMs int64
	Iteration  int    // main-loop turn index; -1 when not applicable (e.g. approval re-run)
	Origin     string // domain.SkillOriginMain | domain.SkillOriginSubagent
}

// LLMUsagePayload is published after each LLM completion.
type LLMUsagePayload struct {
	ChatID                   string
	UserID                   string
	Model                    string
	Provider                 string
	InputTokens              int64
	OutputTokens             int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
	Iteration                int
}

// TaskCompletedPayload is published when a background task finishes successfully.
type TaskCompletedPayload struct {
	TaskID   int64
	TaskType string
	ChatID   string
	Result   string
}

// TaskFailedPayload is published when a background task fails.
type TaskFailedPayload struct {
	TaskID   int64
	TaskType string
	ChatID   string
	Error    string
	Attempt  int
}

// InsightCreatedPayload is published when a new insight is generated.
type InsightCreatedPayload struct {
	ChatID    string
	InsightID int64
	Content   string
	Quality   int
}

// FactSavedPayload is published when a fact is saved to memory.
type FactSavedPayload struct {
	ChatID  string
	FactID  int64
	Content string
}

// FactDeletedPayload is published when a fact is deleted.
type FactDeletedPayload struct {
	ChatID string
	FactID int64
}

// ConfigChangedPayload is published when a config override is set or deleted.
type ConfigChangedPayload struct {
	Key string
}

// ImportProgressPayload streams progress of a Claude export import to the dashboard.
type ImportProgressPayload struct {
	JobID   string `json:"job_id"`
	UserID  string `json:"user_id"`
	Phase   string `json:"phase"` // "memories" | "conversations" | "embedding"
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Stored  int    `json:"stored,omitempty"`
	Skipped int    `json:"skipped,omitempty"`
}

// ImportDonePayload summarizes a completed Claude export import.
type ImportDonePayload struct {
	JobID           string  `json:"job_id"`
	UserID          string  `json:"user_id"`
	Conversations   int     `json:"conversations"`
	MessagesStored  int     `json:"messages_stored"`
	MessagesSkipped int     `json:"messages_skipped"`
	Facts           int     `json:"facts"`
	SkippedBinaries int     `json:"skipped_binaries"`
	ChunksEmbedded  int     `json:"chunks_embedded"`
	ParseErrors     int     `json:"parse_errors"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// ImportFailedPayload reports a failed import with partial progress (resumable).
type ImportFailedPayload struct {
	JobID             string `json:"job_id"`
	UserID            string `json:"user_id"`
	Error             string `json:"error"`
	ConversationsDone int    `json:"conversations_done"`
	MessagesStored    int    `json:"messages_stored"`
	Facts             int    `json:"facts"`
}

// CredentialChangedPayload is published when a credential is created, updated, deleted, or rotated.
type CredentialChangedPayload struct {
	Name string // credential name (dotted key)
}

// OrchestrationStartedPayload is published when a multi-agent orchestration begins.
type OrchestrationStartedPayload struct {
	ChatID     string
	AgentCount int
}

// OrchestrationDonePayload is published when all sub-agents in an orchestration finish.
type OrchestrationDonePayload struct {
	ChatID       string
	AgentCount   int
	SuccessCount int
	TotalTokens  int64
	DurationMs   int64
}
