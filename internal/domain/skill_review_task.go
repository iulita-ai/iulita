package domain

// TaskTypeSkillReview is the scheduler task type for self-improvement reviews.
// Defined here (not in a producer/consumer package) so the assistant that
// enqueues it and the handler that runs it share one source of truth.
const TaskTypeSkillReview = "skill.review"

// SkillReviewPayload is the JSON contract for a skill.review task.
type SkillReviewPayload struct {
	ChatID        string `json:"chat_id"`
	UserID        string `json:"user_id,omitempty"`
	LastMessageID int64  `json:"last_message_id"`
	// ToolSummary is a compact, ordered list of the tool calls that made the
	// turn "hard" (name -> ok/error). Intermediate tool calls are not persisted
	// as chat messages, so this is the only record of the workflow available to
	// the reviewer.
	ToolSummary string `json:"tool_summary,omitempty"`
}
