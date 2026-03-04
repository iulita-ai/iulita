package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

// Provider implements llm.Provider using the OpenAI-compatible chat API.
type Provider struct {
	apiKey     string
	model      string
	maxTokens  int
	baseURL    string
	httpClient *http.Client
}

// ListModels fetches available models from the /v1/models endpoint.
// Works with OpenAI and any compatible API (Together, Azure, etc).
func ListModels(baseURL, apiKey string, httpClient *http.Client) ([]string, error) {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models endpoint returned status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding models: %w", err)
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	sort.Strings(models)
	return models, nil
}

// New creates a new OpenAI-compatible provider.
func New(apiKey, model string, maxTokens int, baseURL string, httpClient *http.Client) *Provider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Provider{
		apiKey:     apiKey,
		model:      model,
		maxTokens:  maxTokens,
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

func (p *Provider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if len(req.Tools) > 0 {
		return llm.Response{}, fmt.Errorf("openai provider does not support tool use")
	}

	var msgs []chatMessage

	if sp := req.FullSystemPrompt(); sp != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: sp})
	}

	for _, m := range req.History {
		role := "user"
		if m.Role == domain.RoleAssistant {
			role = "assistant"
		}
		msgs = append(msgs, chatMessage{Role: role, Content: m.Content})
	}

	if req.Message != "" {
		msgs = append(msgs, chatMessage{Role: "user", Content: req.Message})
	}

	body, err := json.Marshal(chatRequest{
		Model:     p.model,
		Messages:  msgs,
		MaxTokens: p.maxTokens,
	})
	if err != nil {
		return llm.Response{}, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return llm.Response{}, fmt.Errorf("openai returned status %d", resp.StatusCode)
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return llm.Response{}, fmt.Errorf("decoding response: %w", err)
	}

	var content string
	if len(chatResp.Choices) > 0 {
		content = chatResp.Choices[0].Message.Content
	}

	return llm.Response{
		Content: content,
		Usage: llm.Usage{
			InputTokens:  chatResp.Usage.PromptTokens,
			OutputTokens: chatResp.Usage.CompletionTokens,
		},
	}, nil
}
