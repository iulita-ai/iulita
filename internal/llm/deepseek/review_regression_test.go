package deepseek

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
)

func TestFailedGLMFinishNeverReturnsSuccessfulCompletion(t *testing.T) {
	for _, reason := range []string{"network_error", "model_context_window_exceeded", "sensitive"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", reason, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stream {
						fmt.Fprintf(w, "data: {\"model\":\"glm-5.3\",\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n", reason)
					} else {
						fmt.Fprintf(w, `{"model":"glm-5.3","choices":[{"message":{"content":"partial"},"finish_reason":%q}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`, reason)
					}
				}))
				defer server.Close()
				p := NewWithOptions("synthetic", "glm-5.3", 1024, server.URL, server.Client(), nil, Options{ProviderName: "zai", Thinking: "enabled", ReasoningEffort: "low"})
				var resp llm.Response
				var err error
				if stream {
					resp, err = p.CompleteStream(context.Background(), llm.Request{Message: "test"}, func(string) {})
				} else {
					resp, err = p.Complete(context.Background(), llm.Request{Message: "test"})
				}
				if err == nil || resp.Usage.InputTokens != 9 {
					t.Fatalf("failed finish marked successful or usage lost: %v %+v", err, resp)
				}
				if reason == "model_context_window_exceeded" && !llm.IsContextTooLarge(err) {
					t.Fatal("missing context sentinel")
				}
			})
		}
	}
}
func TestDuplicateToolIDsRejectBeforeExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"id":"same","type":"function","function":{"name":"first","arguments":"{}"}},{"id":"same","type":"function","function":{"name":"second","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer server.Close()
	p := NewWithOptions("synthetic", "deepseek-flash", 1024, server.URL, server.Client(), nil, Options{Thinking: "disabled"})
	if _, err := p.Complete(context.Background(), llm.Request{Message: "test"}); err == nil {
		t.Fatal("ambiguous tool IDs accepted")
	}
}
func TestUsageNeverHasNegativeTokenBuckets(t *testing.T) {
	for _, u := range []chatUsage{{PromptTokens: -1}, {PromptTokens: -10, PromptCacheHitTokens: 2}, {PromptTokens: 2, PromptCacheHitTokens: 9, CompletionTokens: -1}} {
		got := mapUsage(u)
		if got.InputTokens < 0 || got.CacheReadInputTokens < 0 || got.OutputTokens < 0 {
			t.Fatalf("negative usage %+v", got)
		}
	}
}
func TestGLMContinuationMissingReasoningRejectedLocally(t *testing.T) {
	p := NewWithOptions("synthetic", "glm-5.3", 1024, "https://invalid.test", nil, nil, Options{ProviderName: "zai", Thinking: "enabled"})
	_, err := p.Complete(context.Background(), llm.Request{ToolExchanges: []llm.ToolExchange{{ToolCalls: []llm.ToolCall{{ID: "id", Name: "probe", Input: []byte(`{}`)}}, Results: []llm.ToolResult{{ToolCallID: "id", Content: "ok"}}}}})
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unsafe replay reached network")
	}
}
