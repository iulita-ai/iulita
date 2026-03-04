package ollama

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
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatResponse struct {
	Message chatMessage `json:"message"`
}

// Provider implements llm.Provider using Ollama's /api/chat endpoint.
type Provider struct {
	url        string
	model      string
	httpClient *http.Client
}

// ListModels fetches available models from Ollama's /api/tags endpoint.
func ListModels(ollamaURL string, httpClient *http.Client) ([]string, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding models: %w", err)
	}

	models := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, m.Name)
	}
	sort.Strings(models)
	return models, nil
}

// New creates a new Ollama provider.
func New(url, model string, httpClient *http.Client) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Provider{url: url, model: model, httpClient: httpClient}
}

func (p *Provider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if len(req.Tools) > 0 {
		return llm.Response{}, fmt.Errorf("ollama provider does not support tools")
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
		Model:    p.model,
		Messages: msgs,
		Stream:   false,
	})
	if err != nil {
		return llm.Response{}, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return llm.Response{}, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return llm.Response{}, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return llm.Response{}, fmt.Errorf("decoding response: %w", err)
	}

	return llm.Response{Content: chatResp.Message.Content}, nil
}
