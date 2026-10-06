package deepseek

import (
	"context"
	"encoding/json"
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
func TestGLMContinuationWithoutReasoningPreservesItsAbsence(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Messages []map[string]any `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Fatal("invalid payload")
		}
		for _, m := range request.Messages {
			if m["role"] == "assistant" {
				if _, present := m["reasoning_content"]; present {
					t.Fatal("invented reasoning block")
				}
			}
		}
		io.WriteString(w, `{"model":"glm-5.3-flash","choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	p := NewWithOptions("synthetic", "glm-5.3-flash", 1024, server.URL, server.Client(), nil, Options{ProviderName: "zai", Thinking: "enabled", ReasoningEffort: "low"})
	response, err := p.Complete(context.Background(), llm.Request{ToolExchanges: []llm.ToolExchange{{ToolCalls: []llm.ToolCall{{ID: "id", Name: "probe", Input: []byte(`{}`)}}, Results: []llm.ToolResult{{ToolCallID: "id", Content: "ok"}}}}})
	if err != nil || calls != 1 || response.Content != "done" {
		t.Fatalf("valid no-reasoning tool continuation failed: %v calls=%d", err, calls)
	}
}

func TestPreflightRejectionIsKnownZeroUsageWithoutHTTP(t *testing.T) {
	for _, fixture := range []struct {
		name, model string
		options     Options
		request     llm.Request
	}{
		{"text-only image", "glm-5.3", Options{ProviderName: "zai", Thinking: "enabled", ReasoningEffort: "low"}, llm.Request{Images: []llm.ImageAttachment{{Data: []byte("not an image"), MediaType: "image/png"}}}},
		{"DeepSeek thinking continuation", "deepseek-flash", Options{Thinking: "enabled"}, llm.Request{ToolExchanges: []llm.ToolExchange{{ToolCalls: []llm.ToolCall{{ID: "call", Name: "echo", Input: []byte(`{}`)}}, Results: []llm.ToolResult{{ToolCallID: "call", Content: "ok"}}}}}},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", fixture.name, stream), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
				defer server.Close()
				inner := NewWithOptions("synthetic", fixture.model, 1024, server.URL, server.Client(), nil, fixture.options)
				var attempts []llm.Attempt
				observed := llm.NewObservingProvider(inner, llm.AttemptMetadata{RequestedModel: fixture.model}, func(_ context.Context, a llm.Attempt) { attempts = append(attempts, a) })
				var err error
				if stream {
					_, err = observed.CompleteStream(context.Background(), fixture.request, func(string) { t.Error("rejected request emitted content") })
				} else {
					_, err = observed.Complete(context.Background(), fixture.request)
				}
				if err == nil || calls != 0 || len(attempts) != 1 {
					t.Fatalf("preflight admission failed: err=%v calls=%d attempts=%d", err, calls, len(attempts))
				}
				a := attempts[0]
				if a.Status != "rejected" || !a.UsageAvailable || a.ChargeUnknown || a.Usage != (llm.Usage{}) {
					t.Fatalf("unsent request cost was not known zero: %+v", a)
				}
			})
		}
	}
}
