package llm

import (
	"context"
	"encoding/json"

	"github.com/iulita-ai/iulita/internal/domain"
)

// ToolDefinition describes a tool that the LLM can invoke.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult carries the outcome of a tool execution back to the LLM.
type ToolResult struct {
	ToolCallID string
	Content    string
	IsError    bool
}

// ToolExchange records one round of tool use: the assistant's response
// (text + tool calls) and the corresponding execution results.
type ToolExchange struct {
	AssistantText string
	ToolCalls     []ToolCall
	Results       []ToolResult
}

// ImageAttachment holds raw image data to send to the LLM.
type ImageAttachment struct {
	Data      []byte
	MediaType string // e.g. "image/jpeg", "image/png"
}

// DocumentAttachment holds a file to send to the LLM (PDF, text, etc.).
type DocumentAttachment struct {
	Data     []byte
	MimeType string // e.g. "application/pdf", "text/plain"
	Filename string
}

// Request is the input to an LLM provider.
type Request struct {
	// StaticSystemPrompt contains the stable portion of the system prompt
	// (base instructions, skill system prompts) that is eligible for
	// provider-side caching. Claude uses cache_control: ephemeral on this
	// block. Other providers simply prepend it to SystemPrompt.
	StaticSystemPrompt string
	SystemPrompt       string
	History            []domain.ChatMessage
	Message            string
	Images             []ImageAttachment    // images attached to current message
	Documents          []DocumentAttachment // documents attached to current message
	Tools              []ToolDefinition     // available tools
	ToolExchanges      []ToolExchange       // accumulated tool use rounds in the current turn
	ThinkingBudget     int64                // extended thinking budget in tokens (0 = disabled)
	ForceTool          string               // if set, force the LLM to use this specific tool
	RouteHint          string               // optional: routing hint for provider selection
}

// Usage tracks token consumption for a single LLM call.
type Usage struct {
	InputTokens              int64
	OutputTokens             int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
}

// Response is the output from an LLM provider.
type Response struct {
	Content   string
	ToolCalls []ToolCall // non-empty when the LLM wants to use tools
	Usage     Usage
}

// FullSystemPrompt returns the combined system prompt for providers that don't
// support caching. It concatenates StaticSystemPrompt and SystemPrompt.
func (r Request) FullSystemPrompt() string {
	if r.StaticSystemPrompt == "" {
		return r.SystemPrompt
	}
	if r.SystemPrompt == "" {
		return r.StaticSystemPrompt
	}
	return r.StaticSystemPrompt + "\n\n" + r.SystemPrompt
}

// Provider abstracts an LLM backend.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// StreamCallback is called with incremental text chunks during streaming.
type StreamCallback func(chunk string)

// StreamingProvider extends Provider with streaming support.
type StreamingProvider interface {
	Provider
	CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error)
}
