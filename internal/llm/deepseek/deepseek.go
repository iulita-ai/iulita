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
	"encoding/base64"
	"encoding/json"
	"fmt"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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
const errBodyLimit = 8 << 10       // 8 KiB
const responseBodyLimit = 32 << 20 // bounds even a misbehaving compatible gateway

// Provider implements llm.Provider/llm.StreamingProvider against DeepSeek.
type Provider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	logger     *zap.Logger

	mu        sync.RWMutex
	model     string
	maxTokens int
	options   Options
}

var (
	_ llm.Provider          = (*Provider)(nil)
	_ llm.StreamingProvider = (*Provider)(nil)
)

// Options fixes a model profile's protocol and attachment contract. Zero limits
// select conservative deployment defaults. New keeps the legacy protocol;
// profile callers must specify thinking explicitly.
type Options struct {
	ProviderName       string
	Vision             bool
	Thinking           string // enabled, disabled, or empty for legacy behavior
	ReasoningEffort    string // low, high, max (Z.ai); high, max (DeepSeek)
	MaxImages          int
	MaxImageBytes      int
	MaxTotalImageBytes int
	MaxImagePixels     int
	MaxRequestBytes    int
}

// New creates a DeepSeek provider. baseURL defaults to the public endpoint
// when empty. httpClient SHOULD be a configured (proxy/SSRF-aware,
// timeout-managed) client in production; nil falls back to a bare client
// (which still uses http.DefaultTransport, so it honors proxy env vars).
// logger may be nil (a no-op logger is used); it is set once at construction
// so no synchronization is needed on reads.
func New(apiKey, model string, maxTokens int, baseURL string, httpClient *http.Client, logger *zap.Logger) *Provider {
	options := Options{}
	if model == "deepseek-flash" || model == "deepseek-v4-flash" || model == "deepseek-v4-flash-vision-exp" || model == "deepseek-v4-pro" {
		options.Thinking = "disabled"
	}
	return NewWithOptions(apiKey, model, maxTokens, baseURL, httpClient, logger, options)
}

// NewWithOptions creates an immutable profile adapter. The legacy update methods
// remain for old callers; new profile registries create a new client on changes.
func NewWithOptions(apiKey, model string, maxTokens int, baseURL string, httpClient *http.Client, logger *zap.Logger, options Options) *Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	// Keep the caller's client and transport immutable. Completion credentials
	// must never follow redirects, including same-host or subdomain redirects.
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	httpClient = &clientCopy
	if logger == nil {
		logger = zap.NewNop()
	}
	if options.ProviderName == "" {
		options.ProviderName = "deepseek"
	}
	if options.MaxImages <= 0 {
		options.MaxImages = 4
	}
	if options.MaxImageBytes <= 0 {
		options.MaxImageBytes = 8 << 20
	}
	if options.MaxTotalImageBytes <= 0 {
		options.MaxTotalImageBytes = 16 << 20
	}
	if options.MaxImagePixels <= 0 {
		options.MaxImagePixels = 40_000_000
	}
	if options.MaxRequestBytes <= 0 {
		options.MaxRequestBytes = 32 << 20
	}
	return &Provider{
		options:    options,
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
	Role    string        `json:"role"`
	Content *string       `json:"content,omitempty"` // pointer: distinguish "" from absent
	Parts   []contentPart `json:"-"`
	// ReasoningContent must be replayed on assistant tool-call turns in thinking
	// mode, or DeepSeek rejects the request with a 400.
	ReasoningContent *string    `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

func (m chatMessage) MarshalJSON() ([]byte, error) {
	type plain chatMessage
	if len(m.Parts) == 0 {
		return json.Marshal(plain(m))
	}
	return json.Marshal(struct {
		Role    string        `json:"role"`
		Content []contentPart `json:"content"`
	}{Role: m.Role, Content: m.Parts})
}

// arguments accepts the string shape used by DeepSeek and the object shape
// returned by compatible GLM endpoints. Marshal always uses a JSON string.
type arguments string

func (a *arguments) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*a = arguments(value)
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return fmt.Errorf("invalid tool arguments")
	}
	*a = arguments(data)
	return nil
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string    `json:"name"`
		Arguments arguments `json:"arguments"` // JSON string on requests; object or string on responses
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
	Thinking      *struct {
		Type          string `json:"type"`
		ClearThinking *bool  `json:"clear_thinking,omitempty"`
	} `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type chatUsage struct {
	PromptTokens          int64 `json:"prompt_tokens"`
	CompletionTokens      int64 `json:"completion_tokens"`
	PromptCacheHitTokens  int64 `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int64 `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails   struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type respMessage struct {
	Content          *string    `json:"content"`
	ReasoningContent *string    `json:"reasoning_content"`
	ToolCalls        []toolCall `json:"tool_calls"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      respMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

type streamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string    `json:"name"`
					Arguments arguments `json:"arguments"`
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
	body, err := p.requestBody(req, model, maxTok, false)
	if err != nil {
		return llm.Response{}, &llm.UnsentRequestError{Cause: err}
	}

	httpReq, err := p.newHTTPRequest(ctx, body)
	if err != nil {
		return llm.Response{}, &llm.UnsentRequestError{Cause: err}
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return llm.Response{}, safeTransportError(ctx, p.options.ProviderName, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			p.logger.Debug("closing model response body failed")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return llm.Response{}, errorFromResponse(p.options.ProviderName+" completion", resp)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, responseBodyLimit+1))
	if err != nil {
		return llm.Response{}, safeTransportError(ctx, p.options.ProviderName, err)
	}
	if len(raw) > responseBodyLimit {
		return llm.Response{}, fmt.Errorf("%s response exceeds limit", p.options.ProviderName)
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return llm.Response{}, fmt.Errorf("%s invalid completion response", p.options.ProviderName)
	}

	var response llm.Response
	if len(cr.Choices) > 0 {
		msg := cr.Choices[0].Message
		response.FinishReason = cr.Choices[0].FinishReason
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
				Input: rawArgs(string(tc.Function.Arguments)),
			})
		}
	}
	if cr.Usage != nil {
		response.Usage = mapUsage(*cr.Usage)
		response.UsageReported = true
	}
	response.RequestedModel = model
	response.Model = model
	if cr.Model != "" {
		response.Model = cr.Model
		response.ModelVerified = true
	}
	response.Provider = p.options.ProviderName
	if err := validateResponse(response); err != nil {
		return response, err
	}
	return response, nil
}

// --- CompleteStream ------------------------------------------------------------

// CompleteStream sends a streaming chat completion request to DeepSeek,
// invoking callback for each text delta and reassembling fragmented tool calls.
func (p *Provider) CompleteStream(ctx context.Context, req llm.Request, callback llm.StreamCallback) (response llm.Response, retErr error) {
	model, maxTok := p.getParams()
	body, err := p.requestBody(req, model, maxTok, true)
	if err != nil {
		return llm.Response{}, &llm.UnsentRequestError{Cause: err}
	}

	httpReq, err := p.newHTTPRequest(ctx, body)
	if err != nil {
		return llm.Response{}, &llm.UnsentRequestError{Cause: err}
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return llm.Response{}, safeTransportError(ctx, p.options.ProviderName, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			p.logger.Debug("closing model response body failed")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return llm.Response{}, errorFromResponse(p.options.ProviderName+" stream", resp)
	}

	response = llm.Response{RequestedModel: model, Model: model, Provider: p.options.ProviderName}
	var visible strings.Builder
	// Preserve visible partial output even on cancellation or malformed SSE,
	// without quadratic concatenation as small token deltas arrive.
	defer func() { response.Content = visible.String() }()
	done := false
	var reasoning strings.Builder
	acc := newToolCallAccumulator()

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, responseBodyLimit+1))
	receivedBytes := 0
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // large tool-arg deltas
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return response, ctx.Err()
		default:
		}

		line := scanner.Text()
		receivedBytes += len(line) + 1
		if receivedBytes > responseBodyLimit {
			return response, fmt.Errorf("%s stream exceeds limit", p.options.ProviderName)
		}
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
				return response, ctx.Err()
			}
			done = true
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return response, fmt.Errorf("%s invalid stream frame", p.options.ProviderName)
		}
		if chunk.Model != "" {
			if response.ModelVerified && response.Model != chunk.Model {
				return response, fmt.Errorf("%s stream model identity changed", p.options.ProviderName)
			}
			response.Model = chunk.Model
			response.ModelVerified = true
		}
		for _, ch := range chunk.Choices {
			if ch.FinishReason != "" {
				response.FinishReason = ch.FinishReason
			}
			if ch.Delta.Content != "" {
				if callback != nil {
					callback(ch.Delta.Content)
				}
				visible.WriteString(ch.Delta.Content)
			}
			// Reasoning (chain-of-thought) is captured but never streamed to the
			// user; it is threaded back via ToolExchange on tool-call turns.
			if ch.Delta.ReasoningContent != "" {
				reasoning.WriteString(ch.Delta.ReasoningContent)
			}
			for _, tc := range ch.Delta.ToolCalls {
				acc.add(tc.Index, tc.ID, tc.Function.Name, string(tc.Function.Arguments))
			}
		}
		if chunk.Usage != nil {
			response.Usage = mapUsage(*chunk.Usage)
			response.UsageReported = true
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return response, ctx.Err()
		}
		return response, fmt.Errorf("%s stream interrupted", p.options.ProviderName)
	}

	response.Content = visible.String()
	response.ToolCalls = acc.finalize()
	response.ReasoningContent = reasoning.String()
	response.RequestedModel = model
	if response.Model == "" {
		response.Model = model
	}
	response.Provider = p.options.ProviderName
	if !done {
		return response, fmt.Errorf("%s stream did not finish: %w", p.options.ProviderName, llm.ErrIncompleteResponse)
	}
	if err := validateResponse(response); err != nil {
		return response, err
	}
	return response, nil
}

func (p *Provider) newHTTPRequest(ctx context.Context, body []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s invalid completion endpoint", p.options.ProviderName)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	return httpReq, nil
}

// requestBody validates capability, real image bytes and protocol before any
// network admission. Attachments are never silently removed.
func (p *Provider) requestBody(req llm.Request, model string, maxTok int, stream bool) ([]byte, error) {
	o := p.options
	// Legacy scalar model hot reload also must not opt the new Flash alias into
	// thinking, whose durable-history replay has not passed the live gate.
	if o.ProviderName == "deepseek" && (model == "deepseek-flash" || model == "deepseek-v4-flash" || model == "deepseek-v4-flash-vision-exp" || model == "deepseek-v4-pro") && o.Thinking == "" {
		o.Thinking = "disabled"
	}
	if len(req.Documents) > 0 {
		return nil, fmt.Errorf("%s documents are unsupported", o.ProviderName)
	}
	if len(req.Images) > 0 && (!o.Vision || model == "deepseek-v4-pro" || model == "glm-5.3") {
		return nil, fmt.Errorf("%s model does not support images", o.ProviderName)
	}
	if len(req.Images) > o.MaxImages {
		return nil, fmt.Errorf("image count exceeds limit")
	}
	if o.Thinking != "" && o.Thinking != "enabled" && o.Thinking != "disabled" {
		return nil, fmt.Errorf("invalid thinking mode")
	}
	if req.ForceTool != "" && !req.RequireToolOutcome && (o.ProviderName == "zai" || o.Thinking == "enabled" || (o.Thinking == "" && isThinkingModel(model))) {
		return nil, fmt.Errorf("%s model does not support required named tool choice", o.ProviderName)
	}
	if o.ReasoningEffort != "" && o.ReasoningEffort != "high" && o.ReasoningEffort != "max" && !(o.ProviderName == "zai" && o.ReasoningEffort == "low") {
		return nil, fmt.Errorf("unsupported reasoning effort")
	}
	if o.ProviderName == "zai" && o.Thinking == "disabled" {
		return nil, fmt.Errorf("GLM 5.3 requires thinking")
	}
	if o.ProviderName == "deepseek" && o.Thinking == "enabled" && (len(req.Tools) > 0 || len(req.ToolExchanges) > 0) {
		for _, h := range req.History {
			if h.Role == domain.RoleAssistant && h.Content != "" {
				return nil, fmt.Errorf("thinking history requires verified replay strategy")
			}
		}
		for _, ex := range req.ToolExchanges {
			if len(ex.ToolCalls) > 0 && ex.ReasoningContent == "" {
				return nil, fmt.Errorf("thinking tool continuation is missing reasoning")
			}
		}
	}
	// GLM can return tool calls without a reasoning block, even with thinking
	// enabled. Preserve any supplied block exactly; preserve absence otherwise.
	// DeepSeek's mandatory thinking replay contract remains guarded above.
	if err := validateToolTranscript(req); err != nil {
		return nil, err
	}
	msgs := buildMessages(req)
	// Preserve exact reasoning only from the current tool loop, never invent an
	// empty block to make a thinking request appear valid.
	for i := range msgs {
		if msgs[i].ReasoningContent != nil && (*msgs[i].ReasoningContent == "" || o.Thinking == "disabled") {
			msgs[i].ReasoningContent = nil
		}
	}
	if len(req.Images) > 0 {
		parts := make([]contentPart, 0, len(req.Images)+1)
		if req.Message != "" {
			parts = append(parts, contentPart{Type: "text", Text: req.Message})
		}
		total := 0
		for _, attachment := range req.Images {
			n := len(attachment.Data)
			if n == 0 || n > o.MaxImageBytes || total > o.MaxTotalImageBytes-n {
				return nil, fmt.Errorf("image bytes exceed limit")
			}
			total += n
			mime := http.DetectContentType(attachment.Data)
			if mime != "image/png" && mime != "image/jpeg" && mime != "image/gif" && mime != "image/webp" {
				return nil, fmt.Errorf("unsupported image format")
			}
			if attachment.MediaType != "" && attachment.MediaType != mime {
				return nil, fmt.Errorf("image MIME does not match actual bytes")
			}
			cfg, _, err := image.DecodeConfig(bytes.NewReader(attachment.Data))
			if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
				return nil, fmt.Errorf("invalid image header")
			}
			if int64(cfg.Width) > int64(o.MaxImagePixels)/int64(cfg.Height) {
				return nil, fmt.Errorf("image dimensions exceed limit")
			}
			part := contentPart{Type: "image_url"}
			part.ImageURL = &struct {
				URL string `json:"url"`
			}{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(attachment.Data)}
			parts = append(parts, part)
		}
		current := 0
		// Find the current user turn before the tool replay. buildMessages may omit
		// empty history, so locate by counting the emitted prefix instead.
		if req.FullSystemPrompt() != "" {
			current++
		}
		for _, h := range req.History {
			if h.Content != "" {
				current++
			}
		}
		msg := chatMessage{Role: "user", Parts: parts}
		if req.Message != "" {
			msgs[current] = msg
		} else {
			msgs = append(msgs, chatMessage{})
			copy(msgs[current+1:], msgs[current:])
			msgs[current] = msg
		}
	}
	choice := buildToolChoice(req, model)
	if o.ProviderName == "zai" || o.Thinking == "enabled" {
		choice = nil
	} else if o.Thinking == "disabled" && req.ForceTool != "" {
		choice = map[string]any{"type": "function", "function": map[string]any{"name": req.ForceTool}}
	}
	cr := chatRequest{Model: model, Messages: msgs, MaxTokens: maxTok, Tools: buildToolDefs(req.Tools), ToolChoice: choice, Stream: stream, ReasoningEffort: o.ReasoningEffort}
	if stream {
		cr.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if o.Thinking != "" || o.ProviderName == "zai" {
		cr.Thinking = &struct {
			Type          string `json:"type"`
			ClearThinking *bool  `json:"clear_thinking,omitempty"`
		}{Type: o.Thinking}
		if o.ProviderName == "zai" {
			yes := true
			cr.Thinking.Type = "enabled"
			cr.Thinking.ClearThinking = &yes
		}
	}
	body, err := json.Marshal(cr)
	if err != nil {
		return nil, fmt.Errorf("invalid completion request")
	}
	if len(body) > o.MaxRequestBytes {
		return nil, fmt.Errorf("serialized request exceeds limit")
	}
	return body, nil
}

// A malformed continuation is rejected locally so no ambiguous tool result is
// attributed to a different operation by a compatible endpoint.
func validateToolTranscript(req llm.Request) error {
	if req.ForceTool != "" {
		found := false
		for _, t := range req.Tools {
			if t.Name == req.ForceTool {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("required tool is not available")
		}
	}
	seen := make(map[string]bool)
	for _, ex := range req.ToolExchanges {
		pending := make(map[string]bool)
		for _, tc := range ex.ToolCalls {
			var args map[string]json.RawMessage
			if tc.ID == "" || tc.Name == "" || seen[tc.ID] || json.Unmarshal(rawArgs(string(tc.Input)), &args) != nil || args == nil {
				return fmt.Errorf("invalid tool continuation")
			}
			seen[tc.ID] = true
			pending[tc.ID] = true
		}
		for _, result := range ex.Results {
			if !pending[result.ToolCallID] {
				return fmt.Errorf("invalid tool result reference")
			}
			delete(pending, result.ToolCallID)
		}
		if len(pending) > 0 {
			return fmt.Errorf("tool continuation is missing results")
		}
	}
	return nil
}

func validateResponse(response llm.Response) error {
	switch response.FinishReason {
	case "network_error":
		return fmt.Errorf("%s output interrupted: %w", response.Provider, llm.ErrIncompleteResponse)
	case "model_context_window_exceeded":
		return fmt.Errorf("%s context exceeded: %w", response.Provider, llm.ErrContextTooLarge)
	case "sensitive":
		return fmt.Errorf("%s output was filtered", response.Provider)
	}
	if response.FinishReason == "length" {
		return fmt.Errorf("%s output reached length limit: %w", response.Provider, llm.ErrIncompleteResponse)
	}
	if response.FinishReason == "content_filter" {
		return fmt.Errorf("%s output was filtered", response.Provider)
	}
	if strings.TrimSpace(response.Content) == "" && len(response.ToolCalls) == 0 {
		return fmt.Errorf("%s completion has no visible output: %w", response.Provider, llm.ErrIncompleteResponse)
	}
	seen := make(map[string]bool, len(response.ToolCalls))
	for _, tc := range response.ToolCalls {
		var input map[string]json.RawMessage
		if tc.ID == "" || seen[tc.ID] || tc.Name == "" || json.Unmarshal(tc.Input, &input) != nil || input == nil {
			return fmt.Errorf("%s invalid tool call", response.Provider)
		}
		seen[tc.ID] = true
	}
	return nil
}

func safeTransportError(ctx context.Context, provider string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// URL errors may contain credential-bearing query/userinfo or proxy errors.
	// Expose only a typed timeout; never echo the upstream transport string.
	if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
		return fmt.Errorf("%s request timed out: %w", provider, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s request failed", provider)
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
			c.Function.Arguments = arguments(argsString(tc.Input))
			am.ToolCalls = append(am.ToolCalls, c)
		}
		// Replay reasoning from this tool round unchanged. Missing values are
		// rejected by explicit thinking profiles before the HTTP request.
		if len(am.ToolCalls) > 0 && ex.ReasoningContent != "" {
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
	total := u.PromptTokens
	if total < 0 {
		total = 0
	}
	hit := u.PromptCacheHitTokens
	if hit == 0 {
		hit = u.PromptTokensDetails.CachedTokens
	}
	if hit < 0 {
		hit = 0
	}
	if hit > total {
		hit = total
	}
	input := total - hit
	if input < 0 {
		input = 0
	}
	output := u.CompletionTokens
	if output < 0 {
		output = 0
	}
	return llm.Usage{InputTokens: input, CacheReadInputTokens: hit, OutputTokens: output}
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
		acc.name += name
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
	status    int
	body      string // fixed safe message, never provider response text
	code      string // server-selected safe category
	permanent bool
}

func (e *apiError) Error() string {
	return fmt.Sprintf("model API returned status %d: %s", e.status, e.body)
}

// StatusCode satisfies llm.HTTPStatusError so 429/5xx responses are retried.
func (e *apiError) StatusCode() int        { return e.status }
func (e *apiError) Permanent() bool        { return e.permanent }
func (e *apiError) ModelErrorCode() string { return e.code }

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
	message := "provider request rejected"
	switch resp.StatusCode {
	case 400:
		message = "invalid model or request"
	case 401, 403:
		message = "authentication or permission denied"
	case 429:
		message = "rate limit exceeded"
	case 500, 502, 503, 504:
		message = "provider unavailable"
	}
	result := &apiError{status: resp.StatusCode, body: message}
	// Z.ai also uses HTTP 429 for balance, product and quota errors. Preserve
	// only allowlisted categories; never expose upstream messages or raw codes.
	if strings.HasPrefix(prefix, "zai ") {
		var envelope struct {
			Error struct {
				Code json.RawMessage `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			var code string
			if json.Unmarshal(envelope.Error.Code, &code) != nil {
				code = string(envelope.Error.Code)
			}
			switch code {
			case "1113":
				result.code, result.body = "insufficient_balance", "insufficient API balance or resource package"
			case "1000", "1001", "1003", "1005":
				result.code, result.body = "authentication_failed", "provider authentication failed"
			case "1220", "1311":
				result.code, result.body = "model_access_denied", "model access is not included"
			case "1315":
				result.code, result.body = "credential_product_mismatch", "credential belongs to another API product"
			case "1308", "1309", "1310", "1313", "1314", "1316", "1317", "1318", "1319", "1320", "1321":
				result.code, result.body = "quota_exhausted", "provider account quota or plan is unavailable"
			}
			result.permanent = result.code != ""
		}
	}
	return result
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
