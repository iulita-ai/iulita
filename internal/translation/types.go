// Package translation implements the bounded Z.ai Translation Agent operation.
// It reuses the managed Z.ai connection, never a separate credential source.
package translation

import (
	"context"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/modelruntime"
)

// AgentID identifies the specialized translation service, not its internal model.
const AgentID = "general_translation"

// Runtime admits operations using the current managed credential generation.
type Runtime interface {
	WithOperationConnection(context.Context, string, string, func(context.Context, modelruntime.Connection, uint64) error) error
	OperationAvailability(string) string
}

// Request contains source text, vendor language codes and trusted caller identity.
type Request struct {
	Text, SourceLanguage, TargetLanguage, UserID, ChatID string
}

// Response contains a complete accepted translation and reported vendor usage.
type Response struct {
	Text           string
	Usage          llm.Usage
	UsageAvailable bool
	TotalCalls     int64
}

// Error contains only stable local codes and safe messages, never upstream text.
type Error struct {
	Code        string
	UserMessage string
	unsent      bool
	cause       error
}

func (e *Error) Error() string { return "translation: " + e.Code }

// ErrorCode exposes a stable category for localized presentation and metrics.
func (e *Error) ErrorCode() string { return e.Code }
func (e *Error) Unwrap() error     { return e.cause }

// NoCharge identifies local rejections that never submitted an upstream request.
func (e *Error) NoCharge() bool { return e.unsent }
func fail(code string) *Error   { return &Error{Code: code} }
