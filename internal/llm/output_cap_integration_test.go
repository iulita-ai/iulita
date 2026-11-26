package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/llm/claude"
	"github.com/iulita-ai/iulita/internal/llm/deepseek"
	"github.com/iulita-ai/iulita/internal/llm/ollama"
	"github.com/iulita-ai/iulita/internal/llm/openai"
	"github.com/iulita-ai/iulita/internal/llm/zai"
	"go.uber.org/zap"
)

func TestRequestOutputCapAtActualAdapterHTTPBoundary(t *testing.T) {
	for _, name := range []string{"claude", "openai", "deepseek", "zai", "ollama"} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			seen := make(chan map[string]any, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				seen <- body
				if r.URL.Path == "/unexpected-replay" {
					http.Error(w, "synthetic failure", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Location", "/unexpected-replay")
				w.WriteHeader(http.StatusTemporaryRedirect)
			}))
			defer server.Close()
			var provider llm.Provider
			switch name {
			case "claude":
				provider = claude.New("fixture", "claude-sonnet-4-6", 4096, server.URL, server.Client())
			case "openai":
				provider = openai.New("fixture", "model", 4096, server.URL, server.Client())
			case "deepseek":
				provider = deepseek.New("fixture", "deepseek-chat", 4096, server.URL, server.Client(), zap.NewNop())
			case "zai":
				provider = zai.New("fixture", "glm-5.3", 4096, server.URL, server.Client(), zap.NewNop())
			case "ollama":
				provider = ollama.New(server.URL, "model", server.Client())
			}
			_, err := provider.Complete(context.Background(), llm.Request{Message: "private fixture", MaxOutputTokens: 127, SingleAttempt: true})
			if err == nil || calls.Load() != 1 {
				t.Fatalf("wanted one failed HTTP attempt: %d %v", calls.Load(), err)
			}
			body := <-seen
			value := body["max_tokens"]
			if name == "ollama" {
				value = body["options"].(map[string]any)["num_predict"]
			}
			if value != float64(127) {
				t.Fatalf("upstream output cap=%v", value)
			}
			if _, err := provider.Complete(context.Background(), llm.Request{Message: "private fixture", MaxOutputTokens: -1, SingleAttempt: true}); err == nil || calls.Load() != 1 {
				t.Fatalf("invalid cap reached HTTP: %d %v", calls.Load(), err)
			}
			if _, err := provider.Complete(context.Background(), llm.Request{Message: "private fixture", MaxOutputTokens: 127, SingleAttempt: true, BeforeRequest: func(context.Context) error { return context.Canceled }}); err == nil || calls.Load() != 1 {
				t.Fatalf("revoked authority reached HTTP: %d %v", calls.Load(), err)
			}
		})
	}
}

func TestRequestOutputCapIncludesClaudeThinkingAndStreaming(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MaxTokens != 1500 {
			t.Errorf("stream output cap: %d %v", body.MaxTokens, err)
		}
		http.Error(w, "synthetic failure", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	p := claude.New("fixture", "claude-sonnet-4-6", 4096, server.URL, server.Client())
	req := llm.Request{Message: "fixture", MaxOutputTokens: 1500, ThinkingBudget: 1024, SingleAttempt: true}
	if _, err := p.CompleteStream(context.Background(), req, nil); err == nil || calls.Load() != 1 {
		t.Fatalf("stream attempt: %d %v", calls.Load(), err)
	}
	req.MaxOutputTokens = 1000
	if _, err := p.Complete(context.Background(), req); err == nil || calls.Load() != 1 {
		t.Fatalf("oversized thinking reached HTTP: %d %v", calls.Load(), err)
	}
	if _, err := p.CompleteStream(context.Background(), req, nil); err == nil || calls.Load() != 1 {
		t.Fatalf("oversized streaming thinking reached HTTP: %d %v", calls.Load(), err)
	}
}
