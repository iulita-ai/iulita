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
	SkillName  string
	ToolCallID string
	Success    bool
	DurationMs int64
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
