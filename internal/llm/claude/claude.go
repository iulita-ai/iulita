package claude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
)

// Provider implements llm.Provider using the Anthropic Claude API.
type Provider struct {
	client    anthropic.Client
	model     string
	maxTokens int
	mu        sync.RWMutex
}

// New creates a new Claude provider.
// baseURL is optional — if non-empty, overrides the default Anthropic API endpoint.
func New(apiKey, model string, maxTokens int, baseURL string, httpClient *http.Client) *Provider {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	client := anthropic.NewClient(opts...)
	return &Provider{
		client:    client,
		model:     model,
		maxTokens: maxTokens,
	}
}

// UpdateModel changes the model at runtime (thread-safe).
func (p *Provider) UpdateModel(model string) {
	p.mu.Lock()
	p.model = model
	p.mu.Unlock()
}

// UpdateMaxTokens changes the max tokens at runtime (thread-safe).
func (p *Provider) UpdateMaxTokens(maxTokens int) {
	p.mu.Lock()
	p.maxTokens = maxTokens
	p.mu.Unlock()
}

// getParams returns the current model and maxTokens (thread-safe read).
func (p *Provider) getParams() (string, int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.model, p.maxTokens
}

func (p *Provider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	messages := make([]anthropic.MessageParam, 0, len(req.History)+2+len(req.ToolExchanges)*2)

	// Conversation history (skip messages with empty content to avoid API errors).
	for _, msg := range req.History {
		if msg.Content == "" {
			continue
		}
		switch msg.Role {
		case domain.RoleUser:
			messages = append(messages, anthropic.NewUserMessage(
				anthropic.NewTextBlock(msg.Content),
			))
		case domain.RoleAssistant:
			messages = append(messages, anthropic.NewAssistantMessage(
				anthropic.NewTextBlock(msg.Content),
			))
		}
	}

	// Current user message (images first, then documents, then text).
	var userBlocks []anthropic.ContentBlockParamUnion
	for _, img := range req.Images {
		encoded := base64.StdEncoding.EncodeToString(img.Data)
		userBlocks = append(userBlocks, anthropic.NewImageBlockBase64(img.MediaType, encoded))
	}
	for _, doc := range req.Documents {
		if doc.MimeType == "application/pdf" {
			encoded := base64.StdEncoding.EncodeToString(doc.Data)
			userBlocks = append(userBlocks, anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
				Data: encoded,
			}))
		} else {
			// Text-based documents (text/plain, text/csv, text/markdown, text/html).
			userBlocks = append(userBlocks, anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{
				Data: string(doc.Data),
			}))
		}
	}
	if req.Message != "" {
		userBlocks = append(userBlocks, anthropic.NewTextBlock(req.Message))
	}
	if len(userBlocks) == 0 {
		userBlocks = append(userBlocks, anthropic.NewTextBlock(""))
	}
	messages = append(messages, anthropic.NewUserMessage(userBlocks...))

	// Append tool exchange rounds (assistant tool_use + user tool_result pairs).
	for _, exchange := range req.ToolExchanges {
		// Assistant message: optional text + tool_use blocks.
		var assistantBlocks []anthropic.ContentBlockParamUnion
		if exchange.AssistantText != "" {
			assistantBlocks = append(assistantBlocks, anthropic.ContentBlockParamUnion{
				OfText: &anthropic.TextBlockParam{Text: exchange.AssistantText},
			})
		}
		for _, tc := range exchange.ToolCalls {
			input, err := rawToAny(tc.Input)
			if err != nil {
				return llm.Response{}, fmt.Errorf("tool call %s input: %w", tc.Name, err)
			}
			assistantBlocks = append(assistantBlocks, anthropic.ContentBlockParamUnion{
				OfToolUse: &anthropic.ToolUseBlockParam{
					ID:    tc.ID,
					Name:  tc.Name,
					Input: input,
				},
			})
		}
		messages = append(messages, anthropic.NewAssistantMessage(assistantBlocks...))

		// User message: tool_result blocks.
		var resultBlocks []anthropic.ContentBlockParamUnion
		for _, tr := range exchange.Results {
			resultBlocks = append(resultBlocks, anthropic.NewToolResultBlock(
				tr.ToolCallID, tr.Content, tr.IsError,
			))
		}
		messages = append(messages, anthropic.NewUserMessage(resultBlocks...))
	}

	model, maxTok := p.getParams()
	maxTokens := int64(maxTok)
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model(model),
		Messages: messages,
	}

	if req.ThinkingBudget > 0 {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(req.ThinkingBudget)
		maxTokens += req.ThinkingBudget
	}
	params.MaxTokens = maxTokens

	if sysBlocks := buildSystemBlocks(req); len(sysBlocks) > 0 {
		params.System = sysBlocks
	}

	// Add tool definitions.
	if len(req.Tools) > 0 {
		tools := make([]anthropic.ToolUnionParam, 0, len(req.Tools))
		for _, t := range req.Tools {
			var schema struct {
				Properties any      `json:"properties"`
				Required   []string `json:"required"`
			}
			if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
				return llm.Response{}, fmt.Errorf("invalid input schema for tool %s: %w", t.Name, err)
			}

			tools = append(tools, anthropic.ToolUnionParam{
				OfTool: &anthropic.ToolParam{
					Name:        t.Name,
					Description: anthropic.String(t.Description),
					InputSchema: anthropic.ToolInputSchemaParam{
						Properties: schema.Properties,
						Required:   schema.Required,
					},
				},
			})
		}
		params.Tools = tools

		// Force a specific tool if requested.
		// Thinking must be disabled when forcing tool use (API constraint).
		if req.ForceTool != "" {
			params.ToolChoice = anthropic.ToolChoiceParamOfTool(req.ForceTool)
			params.Thinking = anthropic.ThinkingConfigParamUnion{
				OfDisabled: &anthropic.ThinkingConfigDisabledParam{},
			}
		}
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		if isContextOverflowError(err) {
			return llm.Response{}, fmt.Errorf("claude completion: %w", llm.ErrContextTooLarge)
		}
		return llm.Response{}, fmt.Errorf("claude completion: %w", err)
	}

	var response llm.Response
	for _, block := range resp.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.TextBlock:
			response.Content += variant.Text
		case anthropic.ToolUseBlock:
			response.ToolCalls = append(response.ToolCalls, llm.ToolCall{
				ID:    variant.ID,
				Name:  variant.Name,
				Input: variant.Input,
			})
		}
	}

	response.Usage = llm.Usage{
		InputTokens:              resp.Usage.InputTokens,
		OutputTokens:             resp.Usage.OutputTokens,
		CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
	}
	p.mu.RLock()
	response.Model = p.model
	p.mu.RUnlock()
	response.Provider = "claude"

	return response, nil
}

func (p *Provider) CompleteStream(ctx context.Context, req llm.Request, callback llm.StreamCallback) (llm.Response, error) {
	messages := make([]anthropic.MessageParam, 0, len(req.History)+2+len(req.ToolExchanges)*2)

	for _, msg := range req.History {
		if msg.Content == "" {
			continue
		}
		switch msg.Role {
		case domain.RoleUser:
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)))
		case domain.RoleAssistant:
			messages = append(messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(msg.Content)))
		}
	}

	var userBlocks []anthropic.ContentBlockParamUnion
	for _, img := range req.Images {
		encoded := base64.StdEncoding.EncodeToString(img.Data)
		userBlocks = append(userBlocks, anthropic.NewImageBlockBase64(img.MediaType, encoded))
	}
	for _, doc := range req.Documents {
		if doc.MimeType == "application/pdf" {
			encoded := base64.StdEncoding.EncodeToString(doc.Data)
			userBlocks = append(userBlocks, anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{Data: encoded}))
		} else {
			userBlocks = append(userBlocks, anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{Data: string(doc.Data)}))
		}
	}
	if req.Message != "" {
		userBlocks = append(userBlocks, anthropic.NewTextBlock(req.Message))
	}
	if len(userBlocks) == 0 {
		userBlocks = append(userBlocks, anthropic.NewTextBlock(""))
	}
	messages = append(messages, anthropic.NewUserMessage(userBlocks...))

	for _, exchange := range req.ToolExchanges {
		var assistantBlocks []anthropic.ContentBlockParamUnion
		if exchange.AssistantText != "" {
			assistantBlocks = append(assistantBlocks, anthropic.ContentBlockParamUnion{
				OfText: &anthropic.TextBlockParam{Text: exchange.AssistantText},
			})
		}
		for _, tc := range exchange.ToolCalls {
			input, err := rawToAny(tc.Input)
			if err != nil {
				return llm.Response{}, fmt.Errorf("tool call %s input: %w", tc.Name, err)
			}
			assistantBlocks = append(assistantBlocks, anthropic.ContentBlockParamUnion{
				OfToolUse: &anthropic.ToolUseBlockParam{ID: tc.ID, Name: tc.Name, Input: input},
			})
		}
		messages = append(messages, anthropic.NewAssistantMessage(assistantBlocks...))

		var resultBlocks []anthropic.ContentBlockParamUnion
		for _, tr := range exchange.Results {
			resultBlocks = append(resultBlocks, anthropic.NewToolResultBlock(tr.ToolCallID, tr.Content, tr.IsError))
		}
		messages = append(messages, anthropic.NewUserMessage(resultBlocks...))
	}

	model, maxTok := p.getParams()
	maxTokens := int64(maxTok)
	params := anthropic.MessageNewParams{
		Model:    anthropic.Model(model),
		Messages: messages,
	}
	if req.ThinkingBudget > 0 {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(req.ThinkingBudget)
		maxTokens += req.ThinkingBudget
	}
	params.MaxTokens = maxTokens

	if sysBlocks := buildSystemBlocks(req); len(sysBlocks) > 0 {
		params.System = sysBlocks
	}

	stream := p.client.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	var response llm.Response
	for stream.Next() {
		evt := stream.Current()
		switch variant := evt.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			if variant.Delta.Type == "text_delta" {
				callback(variant.Delta.Text)
				response.Content += variant.Delta.Text
			}
		case anthropic.MessageDeltaEvent:
			response.Usage.OutputTokens = variant.Usage.OutputTokens
		case anthropic.MessageStartEvent:
			response.Usage.InputTokens = variant.Message.Usage.InputTokens
			response.Usage.CacheReadInputTokens = variant.Message.Usage.CacheReadInputTokens
			response.Usage.CacheCreationInputTokens = variant.Message.Usage.CacheCreationInputTokens
		}
	}

	if err := stream.Err(); err != nil {
		if isContextOverflowError(err) {
			return llm.Response{}, fmt.Errorf("claude stream: %w", llm.ErrContextTooLarge)
		}
		return llm.Response{}, fmt.Errorf("claude stream: %w", err)
	}

	p.mu.RLock()
	response.Model = p.model
	p.mu.RUnlock()
	response.Provider = "claude"

	return response, nil
}

// buildSystemBlocks constructs the system prompt blocks for the Anthropic API.
// When StaticSystemPrompt is set, it becomes a separate block with cache_control
// so Anthropic can cache the expensive static portion across requests.
func buildSystemBlocks(req llm.Request) []anthropic.TextBlockParam {
	if req.StaticSystemPrompt != "" {
		blocks := []anthropic.TextBlockParam{
			{
				Text:         req.StaticSystemPrompt,
				CacheControl: anthropic.NewCacheControlEphemeralParam(),
			},
		}
		if req.SystemPrompt != "" {
			blocks = append(blocks, anthropic.TextBlockParam{Text: req.SystemPrompt})
		}
		return blocks
	}
	if req.SystemPrompt != "" {
		return []anthropic.TextBlockParam{{Text: req.SystemPrompt}}
	}
	return nil
}

// isContextOverflowError detects when the Anthropic API rejects a request
// because the prompt exceeds the model's context window.
func isContextOverflowError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "context_length_exceeded") ||
		strings.Contains(msg, "maximum context length")
}

// rawToAny converts json.RawMessage to any for the SDK's ToolUseBlockParam.Input field.
func rawToAny(raw json.RawMessage) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("unmarshal tool input: %w", err)
	}
	return v, nil
}
