package domain

import (
	"time"

	"github.com/uptrace/bun"
)

// Skill proposal lifecycle states.
const (
	// SkillProposalPending — passed the security scan, awaiting human review.
	SkillProposalPending = "pending"
	// SkillProposalRejected — failed the security scan; never installable.
	SkillProposalRejected = "rejected"
	// SkillProposalDiscarded — a human dismissed it.
	SkillProposalDiscarded = "discarded"
	// SkillProposalInstalled — a human approved and installed it (future step).
	SkillProposalInstalled = "installed"
)

// SkillProposal is a self-authored, NOT-yet-executable skill draft produced by
// the self-improvement review loop. It is inert data: the proposed body is never
// injected into a prompt and the proposal is never registered as a skill until a
// human explicitly approves it. This keeps self-authored content off the live
// prompt/force-trigger path.
type SkillProposal struct {
	bun.BaseModel `bun:"table:skill_proposals"`

	ID          int64  `bun:"id,pk,autoincrement" json:"id"`
	ChatID      string `bun:"chat_id,notnull" json:"chat_id"`
	UserID      string `bun:"user_id,notnull,default:''" json:"user_id"`
	Slug        string `bun:"slug,notnull" json:"slug"`
	Name        string `bun:"name,notnull" json:"name"`
	Description string `bun:"description,notnull,default:''" json:"description"`
	Body        string `bun:"body,notnull,default:''" json:"body"`         // proposed SKILL.md instruction (inert)
	Triggers    string `bun:"triggers,notnull,default:''" json:"triggers"` // comma-separated proposed triggers
	Warnings    string `bun:"warnings,notnull,default:''" json:"warnings"` // JSON array of security-scan warnings
	Status      string `bun:"status,notnull,default:'pending'" json:"status"`

	SourceMessageID int64     `bun:"source_message_id,notnull,default:0" json:"source_message_id"` // provenance: turn boundary
	CreatedAt       time.Time `bun:"created_at,notnull,default:current_timestamp" json:"created_at"`
}
