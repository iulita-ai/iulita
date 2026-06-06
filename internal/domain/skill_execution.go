package domain

import (
	"time"

	"github.com/uptrace/bun"
)

// Skill execution origins.
const (
	// SkillOriginMain marks a skill executed from the primary agentic loop.
	SkillOriginMain = "main"
	// SkillOriginSubagent marks a skill executed by a sub-agent runner.
	SkillOriginSubagent = "subagent"
)

// SkillExecution is a per-call outcome ledger entry for a skill/tool invocation.
// Unlike the generic audit_log, this is a typed, queryable telemetry record used
// to reason about how skills perform (success rates, latency, usage frequency).
type SkillExecution struct {
	bun.BaseModel `bun:"table:skill_executions"`

	ID         int64     `bun:"id,pk,autoincrement"`
	ChatID     string    `bun:"chat_id,notnull"`
	UserID     string    `bun:"user_id,notnull,default:''"` // iulita user UUID
	SkillName  string    `bun:"skill_name,notnull"`
	ToolCallID string    `bun:"tool_call_id,notnull,default:''"`
	Success    bool      `bun:"success"`
	DurationMs int64     `bun:"duration_ms"`
	Iteration  int       `bun:"iteration,notnull,default:0"`   // main-loop turn index (-1 when N/A)
	Origin     string    `bun:"origin,notnull,default:'main'"` // "main" | "subagent"
	CreatedAt  time.Time `bun:"created_at,notnull,default:current_timestamp"`
}
