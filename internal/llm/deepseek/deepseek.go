// Package deepseek implements the llm.Provider and llm.StreamingProvider
// interfaces for DeepSeek's OpenAI-compatible chat completions API.
//
// DeepSeek speaks the OpenAI wire format (/v1/chat/completions, /v1/models),
// so the JSON shapes here mirror OpenAI. Unlike the lightweight
// internal/llm/openai provider, this one fully supports tool-use and SSE
// streaming, matching the Claude provider's contract so the assistant's
// agentic loop works unchanged.
package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
)

const defaultBaseURL = "https://api.deepseek.com/v1"

// errBodyLimit bounds how much of a non-2xx response body we read into an
// error string, preventing a huge upstream body from leaking into logs.
const errBodyLimit = 8 << 10 // 8 KiB

// Provider implements llm.Provider/llm.StreamingProvider against DeepSeek.
type Provider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	logger     *zap.Logger

	mu        sync.RWMutex
	model     string
	maxTokens int
}

var (
	_ llm.Provider          = (*Provider)(nil)
	_ llm.StreamingProvider = (*Provider)(nil)
)

// New creates a DeepSeek provider. baseURL defaults to the public endpoint
// when empty. httpClient SHOULD be a configured (proxy/SSRF-aware,
// timeout-managed) client in production; nil falls back to a bare client
// (which still uses http.DefaultTransport, so it honors proxy env vars).
// logger may be nil (a no-op logger is used); it is set once at construction
// so no synchronization is needed on reads.
func New(apiKey, model string, maxTokens int, baseURL string, httpClient *http.Client, logger *zap.Logger) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Provider{
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: httpClient,
		logger:     logger,
		model:      model,
		maxTokens:  maxTokens,
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
func (p *Provider) getParams() (model string, maxTokens int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.model, p.maxTokens
}

func (p *Provider) endpoint() string { return p.baseURL + "/chat/completions" }

// --- Wire types (OpenAI shape) -------------------------------------------------

type chatMessage struct {
	Role    string  `json:"role"`
	Content *string `json:"content,omitempty"` // pointer: distinguish "" from absent
	// ReasoningContent must be replayed on assistant tool-call turns in thinking
	// mode, or DeepSeek rejects the request with a 400.
	ReasoningContent *string    `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON-encoded string, not an object
	} `json:"function"`
}

type toolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []chatMessage  `json:"messages"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Tools         []toolDef      `json:"tools,omitempty"`
	ToolChoice    any            `json:"tool_choice,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type chatUsage struct {
	PromptTokens          int64 `json:"prompt_tokens"`
	CompletionTokens      int64 `json:"completion_tokens"`
	PromptCacheHitTokens  int64 `json:"prompt_cache_hit_tokens"`  // parsed now, mapped in Phase 2
	PromptCacheMissTokens int64 `json:"prompt_cache_miss_tokens"` // parsed now, mapped in Phase 2
}

type respMessage struct {
	Content          *string    `json:"content"`
	ReasoningContent *string    `json:"reasoning_content"`
	ToolCalls        []toolCall `json:"tool_calls"`
}

type chatResponse struct {
	Choices []struct {
		Message respMessage `json:"message"`
	} `json:"choices"`
	Usage chatUsage `json:"usage"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// --- Complete ------------------------------------------------------------------

// Complete sends a non-streaming chat completion request to DeepSeek.
func (p *Provider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	model, maxTok := p.getParams()
	p.warnUnsupportedAttachments(req)

	body, err := json.Marshal(chatRequest{
		Model:      model,
		Messages:   buildMessages(req),
		MaxTokens:  maxTok,
		Tools:      buildToolDefs(req.Tools),
		ToolChoice: buildToolChoice(req, model),
		Stream:     false,
	})
	if err != nil {
		return llm.Response{}, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := p.newHTTPRequest(ctx, body)
	if err != nil {
		return llm.Response{}, err
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return llm.Response{}, fmt.Errorf("deepseek request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			p.logger.Debug("closing deepseek response body", zap.Error(cerr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return llm.Response{}, errorFromResponse("deepseek completion", resp)
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return llm.Response{}, fmt.Errorf("decoding response: %w", err)
	}

	var response llm.Response
	if len(cr.Choices) > 0 {
		msg := cr.Choices[0].Message
		if msg.Content != nil {
			response.Content = *msg.Content
		}
		if msg.ReasoningContent != nil {
			response.ReasoningContent = *msg.ReasoningContent
		}
		for _, tc := range msg.ToolCalls {
			response.ToolCalls = append(response.ToolCalls, llm.ToolCall{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: rawArgs(tc.Function.Arguments),
			})
		}
	}
	response.Usage = mapUsage(cr.Usage)
	response.Model = model
	response.Provider = "deepseek"
	return response, nil
}

// --- CompleteStream ------------------------------------------------------------

// CompleteStream sends a streaming chat completion request to DeepSeek,
// invoking callback for each text delta and reassembling fragmented tool calls.
func (p *Provider) CompleteStream(ctx context.Context, req llm.Request, callback llm.StreamCallback) (llm.Response, error) {
	model, maxTok := p.getParams()
	p.warnUnsupportedAttachments(req)

	body, err := json.Marshal(chatRequest{
		Model:         model,
		Messages:      buildMessages(req),
		MaxTokens:     maxTok,
		Tools:         buildToolDefs(req.Tools),
		ToolChoice:    buildToolChoice(req, model),
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true}, // emit a final usage chunk
	})
	if err != nil {
		return llm.Response{}, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := p.newHTTPRequest(ctx, body)
	if err != nil {
		return llm.Response{}, err
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return llm.Response{}, fmt.Errorf("deepseek stream request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			p.logger.Debug("closing deepseek response body", zap.Error(cerr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return llm.Response{}, errorFromResponse("deepseek stream", resp)
	}

	var response llm.Response
	var reasoning strings.Builder
	acc := newToolCallAccumulator()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // large tool-arg deltas
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		default:
		}

		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") { // blank or SSE comment (keep-alive)
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "[DONE]" {
			// A canceled context that coincides with [DONE] must still surface
			// as an error, not a partial success.
			if ctx.Err() != nil {
				return llm.Response{}, ctx.Err()
			}
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // tolerate partial/keepalive frames
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				callback(ch.Delta.Content)
				response.Content += ch.Delta.Content
			}
			// Reasoning (chain-of-thought) is captured but never streamed to the
			// user; it is threaded back via ToolExchange on tool-call turns.
			if ch.Delta.ReasoningContent != "" {
				reasoning.WriteString(ch.Delta.ReasoningContent)
			}
			for _, tc := range ch.Delta.ToolCalls {
				acc.add(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
			}
		}
		if chunk.Usage != nil {
			response.Usage = mapUsage(*chunk.Usage)
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{}, fmt.Errorf("deepseek stream: %w", err)
	}

	response.ToolCalls = acc.finalize()
	response.ReasoningContent = reasoning.String()
	response.Model = model
	response.Provider = "deepseek"
	return response, nil
}

func (p *Provider) newHTTPRequest(ctx context.Context, body []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	return httpReq, nil
}

func (p *Provider) warnUnsupportedAttachments(req llm.Request) {
	if n := len(req.Images) + len(req.Documents); n > 0 {
		p.logger.Warn("deepseek provider does not support image/document attachments; skipping",
			zap.Int("count", n))
	}
}

// --- Pure helpers (network-free, unit-tested) ----------------------------------

func buildMessages(req llm.Request) []chatMessage {
	msgs := make([]chatMessage, 0, len(req.History)+2+len(req.ToolExchanges)*2)

	if sp := req.FullSystemPrompt(); sp != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: strPtr(sp)})
	}

	for _, m := range req.History {
		if m.Content == "" { // skip empty content to avoid API errors
			continue
		}
		role := "user"
		if m.Role == domain.RoleAssistant {
			role = "assistant"
		}
		msgs = append(msgs, chatMessage{Role: role, Content: strPtr(m.Content)})
	}

	if req.Message != "" {
		msgs = append(msgs, chatMessage{Role: "user", Content: strPtr(req.Message)})
	}

	// Replay accumulated tool-use rounds: assistant(tool_calls) + tool results.
	for _, ex := range req.ToolExchanges {
		am := chatMessage{Role: "assistant"}
		if ex.AssistantText != "" {
			am.Content = strPtr(ex.AssistantText)
		}
		for _, tc := range ex.ToolCalls {
			var c toolCall
			c.ID = tc.ID
			c.Type = "function"
			c.Function.Name = tc.Name
			c.Function.Arguments = argsString(tc.Input)
			am.ToolCalls = append(am.ToolCalls, c)
		}
		// DeepSeek thinking-mode REQUIRES reasoning_content to be replayed on an
		// assistant turn that carries tool_calls (otherwise: 400 "must be passed
		// back"). Emit it (possibly empty) whenever there are tool calls.
		if len(am.ToolCalls) > 0 {
			am.ReasoningContent = strPtr(ex.ReasoningContent)
		}
		// A bare {"role":"assistant"} with neither content nor tool_calls is
		// rejected by the API; only append a turn that carries something.
		if am.Content != nil || len(am.ToolCalls) > 0 {
			msgs = append(msgs, am)
		}

		for _, tr := range ex.Results {
			// Emit content:"" explicitly (present, not absent) for empty results.
			msgs = append(msgs, chatMessage{
				Role:       "tool",
				ToolCallID: tr.ToolCallID,
				Content:    strPtr(tr.Content),
			})
		}
	}

	return msgs
}

func buildToolDefs(tools []llm.ToolDefinition) []toolDef {
	if len(tools) == 0 {
		return nil
	}
	out := make([]toolDef, 0, len(tools))
	for _, t := range tools {
		params := t.InputSchema
		if len(params) == 0 || string(params) == "null" {
			// DeepSeek/OpenAI reject "parameters": null with a 400.
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		} else {
			// DeepSeek strictly requires a top-level "type":"object" on every
			// function schema; some skills declare only "properties" (Claude is
			// lenient). Inject the type so a lax schema doesn't 400 the request.
			params = ensureObjectType(params)
		}
		var d toolDef
		d.Type = "function"
		d.Function.Name = t.Name
		d.Function.Description = t.Description
		d.Function.Parameters = params
		out = append(out, d)
	}
	return out
}

// ensureObjectType guarantees a function parameter schema has a top-level
// "type":"object". DeepSeek rejects schemas without it ("got type: null"),
// whereas Claude tolerates the omission. Returns the input unchanged if it
// already has a type or can't be parsed as an object.
func ensureObjectType(schema json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(schema, &m); err != nil {
		return schema // not a JSON object — leave as-is
	}
	if _, ok := m["type"]; ok {
		return schema
	}
	m["type"] = json.RawMessage(`"object"`)
	patched, err := json.Marshal(m)
	if err != nil {
		return schema
	}
	return patched
}

// isThinkingModel reports whether a DeepSeek model runs in "thinking" mode.
// V4 models (deepseek-v4-flash/-pro) and the reasoner are thinking models.
func isThinkingModel(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "v4") || strings.Contains(m, "reasoner") || strings.Contains(m, "think")
}

func buildToolChoice(req llm.Request, model string) any {
	if req.ForceTool == "" {
		return nil // defaults to "auto" server-side
	}
	// Thinking models reject a forced named tool_choice ("Thinking mode does not
	// support this tool_choice"). Fall back to auto — the tool is still offered,
	// the model just isn't forced to call it.
	if isThinkingModel(model) {
		return nil
	}
	return map[string]any{
		"type":     "function",
		"function": map[string]any{"name": req.ForceTool},
	}
}

// mapUsage maps DeepSeek usage to llm.Usage.
//
// DeepSeek guarantees prompt_tokens == prompt_cache_hit_tokens +
// prompt_cache_miss_tokens. We map miss → InputTokens (full rate) and hit →
// CacheReadInputTokens (discounted rate), preserving the invariant
// InputTokens + CacheReadInputTokens + CacheCreationInputTokens == prompt_tokens
// so the cost tracker bills exactly prompt_tokens worth of input. When the
// split isn't reported (older/compatible endpoints), all prompt tokens fall
// back to InputTokens at the full rate.
func mapUsage(u chatUsage) llm.Usage {
	out := llm.Usage{OutputTokens: u.CompletionTokens}
	if u.PromptCacheHitTokens > 0 || u.PromptCacheMissTokens > 0 {
		out.InputTokens = u.PromptCacheMissTokens
		out.CacheReadInputTokens = u.PromptCacheHitTokens
	} else {
		out.InputTokens = u.PromptTokens
	}
	return out
}

// toolCallAccumulator reassembles streamed tool calls that arrive fragmented
// across multiple SSE chunks, keyed by their index.
type toolCallAccumulator struct {
	order   []int
	byIndex map[int]*accumToolCall
}

type accumToolCall struct {
	id   string
	name string
	args strings.Builder
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIndex: make(map[int]*accumToolCall)}
}

func (a *toolCallAccumulator) add(index int, id, name, argsFragment string) {
	acc, ok := a.byIndex[index]
	if !ok {
		acc = &accumToolCall{}
		a.byIndex[index] = acc
		a.order = append(a.order, index)
	}
	if id != "" {
		acc.id = id
	}
	if name != "" {
		acc.name = name
	}
	if argsFragment != "" {
		acc.args.WriteString(argsFragment)
	}
}

func (a *toolCallAccumulator) finalize() []llm.ToolCall {
	if len(a.order) == 0 {
		return nil
	}
	sort.Ints(a.order)
	out := make([]llm.ToolCall, 0, len(a.order))
	for _, idx := range a.order {
		acc := a.byIndex[idx]
		out = append(out, llm.ToolCall{
			ID:    acc.id,
			Name:  acc.name,
			Input: rawArgs(acc.args.String()),
		})
	}
	return out
}

// --- Error handling ------------------------------------------------------------

// apiError carries the HTTP status so RetryProvider can retry transient codes.
type apiError struct {
	status int
	body   string // already bounded to <= errBodyLimit
}

func (e *apiError) Error() string {
	return fmt.Sprintf("deepseek returned status %d: %s", e.status, e.body)
}

// StatusCode satisfies llm.HTTPStatusError so 429/5xx responses are retried.
func (e *apiError) StatusCode() int { return e.status }

// errorFromResponse reads a bounded portion of a non-2xx body and returns either
// a wrapped llm.ErrContextTooLarge (so the agentic loop compresses and retries)
// or a typed, retryable apiError.
func errorFromResponse(prefix string, resp *http.Response) error {
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
	body := string(raw)
	if body == "" && readErr != nil {
		body = readErr.Error()
	}
	if isContextOverflowError(resp.StatusCode, body) {
		return fmt.Errorf("%s: %w", prefix, llm.ErrContextTooLarge)
	}
	return &apiError{status: resp.StatusCode, body: extractErrorMessage(body)}
}

// extractErrorMessage prefers the structured error.message; the raw body is
// already bounded to errBodyLimit by the caller.
func extractErrorMessage(body string) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return body
}

// isContextOverflowError detects a context-window-exceeded rejection. It prefers
// the structured error.code and uses tightly-scoped substring fallbacks. The
// bare token "too long" is intentionally NOT matched (misclassifies unrelated
// 400s and would falsely trigger compress-and-retry).
func isContextOverflowError(status int, body string) bool {
	if status != http.StatusBadRequest {
		return false
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &e) == nil && e.Error.Code == "context_length_exceeded" {
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "context length") ||
		strings.Contains(lower, "maximum context length") ||
		strings.Contains(lower, "context_length_exceeded")
}

// --- small helpers -------------------------------------------------------------

func strPtr(s string) *string { return &s }

// argsString renders tool-call input (a JSON object) as the JSON string the
// OpenAI wire format expects for function arguments.
func argsString(input json.RawMessage) string {
	if len(input) == 0 {
		return "{}"
	}
	return string(input)
}

// rawArgs converts a function-arguments JSON string back into a json.RawMessage,
// guarding against an empty string (which is not valid JSON).
func rawArgs(args string) json.RawMessage {
	if args == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(args)
}
