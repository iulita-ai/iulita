package llm_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/llm/ollama"
	"github.com/iulita-ai/iulita/internal/llm/openai"
)

func TestRequestCompletionProofFromActualWire(t *testing.T) {
	for _, tc := range []struct{ name, body, reason string }{
		{"openai_stop", `{"choices":[{"message":{"content":"fixture"},"finish_reason":"stop"}]}`, "stop"},
		{"openai_truncated", `{"choices":[{"message":{"content":"fixture"},"finish_reason":"length"}]}`, "length"},
		{"openai_unknown", `{"choices":[{"message":{"content":"fixture"}}]}`, ""},
		{"ollama_stop", `{"message":{"content":"fixture"},"done":true,"done_reason":"stop"}`, "stop"},
		{"ollama_complete_legacy", `{"message":{"content":"fixture"},"done":true}`, "stop"},
		{"ollama_truncated", `{"message":{"content":"fixture"},"done":true,"done_reason":"length"}`, "length"},
		{"ollama_partial", `{"message":{"content":"fixture"},"done":false,"done_reason":"stop"}`, "incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			var provider llm.Provider = ollama.New(server.URL, "model", server.Client())
			if tc.name[:6] == "openai" {
				provider = openai.New("fixture", "model", 1000, server.URL, server.Client())
			}
			response, err := provider.Complete(context.Background(), llm.Request{Message: "fixture", MaxOutputTokens: 1000, SingleAttempt: true})
			if err != nil || response.Content != "fixture" || response.FinishReason != tc.reason {
				t.Fatalf("completion proof=%q want=%q err=%v", response.FinishReason, tc.reason, err)
			}
		})
	}
}
