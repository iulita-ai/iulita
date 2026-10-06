package zai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
)

func TestGLMToolProtocolAndObjectArguments(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"model":"glm-5.3-20261001","choices":[{"message":{"reasoning_content":"private","tool_calls":[{"id":"call1","type":"function","function":{"name":"echo","arguments":{"value":"hi"}}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":8}}}`)
	}))
	defer srv.Close()
	p := New("key", "glm-5.3", 2048, srv.URL, srv.Client(), nil)
	res, err := p.Complete(context.Background(), llm.Request{Message: "hi", Tools: []llm.ToolDefinition{{Name: "echo"}}})
	if err != nil {
		t.Fatal(err)
	}
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["clear_thinking"] != true || body["reasoning_effort"] != "low" {
		t.Fatalf("thinking protocol: %v", body)
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("GLM only supports auto tool choice: %v", body)
	}
	if res.Provider != "zai" || res.Model != "glm-5.3-20261001" || res.RequestedModel != "glm-5.3" || res.FinishReason != "tool_calls" || !res.ModelVerified {
		t.Fatalf("identity: %+v", res)
	}
	if res.Usage.InputTokens != 12 || res.Usage.CacheReadInputTokens != 8 {
		t.Fatalf("cached input: %+v", res.Usage)
	}
	if len(res.ToolCalls) != 1 || string(res.ToolCalls[0].Input) != `{"value":"hi"}` {
		t.Fatalf("object arguments: %+v", res.ToolCalls)
	}
}

func TestGLMContinuationReplaysCurrentPrivateReasoning(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	p := New("key", "glm-5.3", 2048, srv.URL, srv.Client(), nil)
	_, err := p.Complete(context.Background(), llm.Request{Message: "hi", ToolExchanges: []llm.ToolExchange{{ReasoningContent: "opaque exact", ToolCalls: []llm.ToolCall{{ID: "c", Name: "echo", Input: json.RawMessage(`{}`)}}, Results: []llm.ToolResult{{ToolCallID: "c", Content: "ok"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	if msgs[1].(map[string]any)["reasoning_content"] != "opaque exact" {
		t.Fatalf("lost continuation: %v", body)
	}
}

func TestGLMUnsupportedConfigurationFailsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	for _, opts := range []Options{{Thinking: "disabled"}, {ReasoningEffort: "medium"}} {
		p := NewWithOptions("key", "glm-5.3", 2048, srv.URL, srv.Client(), nil, opts)
		if _, err := p.Complete(context.Background(), llm.Request{Message: "hi"}); err == nil {
			t.Fatal("invalid forced-thinking configuration accepted")
		}
	}
	p := New("key", "glm-5.3", 2048, srv.URL, srv.Client(), nil)
	if _, err := p.Complete(context.Background(), llm.Request{Images: []llm.ImageAttachment{{Data: []byte("image")}}}); err == nil {
		t.Fatal("text-only GLM accepted image")
	}
	p = New("key", "glm-5.3-flash", 2048, srv.URL, srv.Client(), nil)
	if _, err := p.Complete(context.Background(), llm.Request{Message: "hi", ForceTool: "echo", Tools: []llm.ToolDefinition{{Name: "echo"}}}); err == nil {
		t.Fatal("unhonored required tool accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached provider")
	}
}

func TestGLMStreamingObjectArgumentsAndPrivateReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: "+`{"model":"glm-served","choices":[{"delta":{"reasoning_content":"private","tool_calls":[{"index":0,"id":"c1","function":{"name":"echo","arguments":{"x":1}}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	var visible strings.Builder
	p := New("key", "glm-5.3", 2048, srv.URL, srv.Client(), nil)
	res, err := p.CompleteStream(context.Background(), llm.Request{Message: "hi"}, func(s string) { visible.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if visible.Len() != 0 || res.ReasoningContent != "private" || len(res.ToolCalls) != 1 || string(res.ToolCalls[0].Input) != `{"x":1}` || res.Model != "glm-served" {
		t.Fatalf("stream: %+v visible=%s", res, visible.String())
	}
}
