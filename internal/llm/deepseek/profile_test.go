package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
)

func fixturePNG(t *testing.T) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 2, 2))
	im.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestProfileVisionInlineAndServedModel(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"model":"deepseek-v4.1-flash-20261001","choices":[{"message":{"content":"red"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":2}}`)
	}))
	defer srv.Close()
	p := NewWithOptions("key", "deepseek-flash", 8192, srv.URL, srv.Client(), nil, Options{Vision: true, Thinking: "disabled"})
	res, err := p.Complete(context.Background(), llm.Request{Images: []llm.ImageAttachment{{Data: fixturePNG(t), MediaType: "image/png"}}})
	if err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	parts := messages[0].(map[string]any)["content"].([]any)
	dataURL := parts[0].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("missing inline attachment: %v", body)
	}
	if body["thinking"].(map[string]any)["type"] != "disabled" {
		t.Fatalf("thinking: %v", body)
	}
	if res.Model != "deepseek-v4.1-flash-20261001" || res.RequestedModel != "deepseek-flash" || res.FinishReason != "stop" {
		t.Fatalf("identity: %+v", res)
	}
}

func TestProfileAttachmentsRejectBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	cases := []struct {
		name  string
		model string
		opts  Options
		req   llm.Request
	}{
		{"text only", "deepseek-v4-pro", Options{}, llm.Request{Images: []llm.ImageAttachment{{Data: fixturePNG(t)}}}},
		{"MIME mismatch", "deepseek-flash", Options{Vision: true}, llm.Request{Images: []llm.ImageAttachment{{Data: fixturePNG(t), MediaType: "image/jpeg"}}}},
		{"invalid image", "deepseek-flash", Options{Vision: true}, llm.Request{Images: []llm.ImageAttachment{{Data: []byte("not an image"), MediaType: "image/png"}}}},
		{"count limit", "deepseek-flash", Options{Vision: true, MaxImages: 1}, llm.Request{Images: []llm.ImageAttachment{{Data: fixturePNG(t)}, {Data: fixturePNG(t)}}}},
		{"byte limit", "deepseek-flash", Options{Vision: true, MaxImageBytes: 1}, llm.Request{Images: []llm.ImageAttachment{{Data: fixturePNG(t)}}}},
		{"pixel limit", "deepseek-flash", Options{Vision: true, MaxImagePixels: 1}, llm.Request{Images: []llm.ImageAttachment{{Data: fixturePNG(t)}}}},
		{"document", "deepseek-flash", Options{Vision: true}, llm.Request{Documents: []llm.DocumentAttachment{{Data: []byte("pdf")}}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := NewWithOptions("key", tt.model, 100, srv.URL, srv.Client(), nil, tt.opts)
			if _, err := p.Complete(context.Background(), tt.req); err == nil {
				t.Fatal("expected attachment rejection")
			}
			if _, err := p.CompleteStream(context.Background(), tt.req, func(string) {}); err == nil {
				t.Fatal("expected stream rejection")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("rejected attachments reached network: %d", calls.Load())
	}
}

func TestThinkingProfileRejectsIncompleteHistoryReplay(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	p := NewWithOptions("key", "deepseek-flash", 100, srv.URL, srv.Client(), nil, Options{Thinking: "enabled"})
	_, err := p.Complete(context.Background(), llm.Request{History: []domain.ChatMessage{{Role: domain.RoleAssistant, Content: "old reply"}}, Tools: []llm.ToolDefinition{{Name: "echo"}}})
	if err == nil || calls.Load() != 0 {
		t.Fatal("thinking history without reasoning must fail before network")
	}
}

func TestStreamRejectsTruncatedOrMalformedOutput(t *testing.T) {
	for _, data := range []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
		"data: {invalid JSON}\n\ndata: [DONE]\n\n",
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"echo\",\"arguments\":\"{bad\"}}]}}]}\n\ndata: [DONE]\n\n",
	} {
		t.Run(data, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, data) }))
			defer srv.Close()
			p := New("key", "deepseek-flash", 100, srv.URL, srv.Client(), nil)
			if _, err := p.CompleteStream(context.Background(), llm.Request{Message: "hello"}, func(string) {}); err == nil {
				t.Fatal("incomplete stream accepted")
			}
		})
	}
}

func TestProviderErrorsDoNotEchoUpstreamSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"secret-key prompt private https://example.test?token=secret-key"}}`)
	}))
	defer srv.Close()
	p := New("secret-key", "deepseek-flash", 100, srv.URL, srv.Client(), nil)
	_, err := p.Complete(context.Background(), llm.Request{Message: "private"})
	if err == nil || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestProfileVisionToolReplayOrderAndLimits(t *testing.T) {
	var messages []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		messages = body.Messages
		io.WriteString(w, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	req := llm.Request{SystemPrompt: "system", History: []domain.ChatMessage{{Role: domain.RoleUser, Content: "before"}}, Images: []llm.ImageAttachment{{Data: fixturePNG(t)}, {Data: fixturePNG(t)}}, ToolExchanges: []llm.ToolExchange{{ToolCalls: []llm.ToolCall{{ID: "c", Name: "echo", Input: json.RawMessage(`{}`)}}, Results: []llm.ToolResult{{ToolCallID: "c", Content: "ok"}}}}}
	p := NewWithOptions("key", "deepseek-flash", 100, srv.URL, srv.Client(), nil, Options{Vision: true, Thinking: "disabled"})
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 5 || messages[2]["role"] != "user" || len(messages[2]["content"].([]any)) != 2 || messages[3]["role"] != "assistant" || messages[4]["role"] != "tool" {
		t.Fatalf("bad image/tool order: %v", messages)
	}
	p = NewWithOptions("key", "deepseek-flash", 100, srv.URL, srv.Client(), nil, Options{Vision: true, Thinking: "disabled", MaxRequestBytes: 8})
	if _, err := p.Complete(context.Background(), req); err == nil {
		t.Fatal("serialized request limit ignored")
	}
}

func TestProfileIncompleteOutputRetainsUsage(t *testing.T) {
	for _, payload := range []string{
		`{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":12,"completion_tokens":20}}`,
		`{"choices":[{"message":{"reasoning_content":"private"},"finish_reason":"length"}],"usage":{"prompt_tokens":12,"completion_tokens":20}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
		p := NewWithOptions("key", "deepseek-flash", 100, srv.URL, srv.Client(), nil, Options{Thinking: "disabled"})
		res, err := p.Complete(context.Background(), llm.Request{Message: "hi"})
		srv.Close()
		if err == nil || res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 20 || res.FinishReason != "length" {
			t.Fatalf("truncated response must retain known usage: %+v %v", res, err)
		}
	}
}

func TestStreamFragmentedToolName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"ec","arguments":"{\"x\":"}}]}}]}`+"\n\ndata: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"ho","arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	p := New("key", "deepseek-flash", 100, srv.URL, srv.Client(), nil)
	res, err := p.CompleteStream(context.Background(), llm.Request{Message: "hi"}, nil)
	if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "echo" || string(res.ToolCalls[0].Input) != `{"x":1}` {
		t.Fatalf("fragmented name/args: %+v %v", res, err)
	}
}

func TestNonThinkingRequiredToolIsExplicit(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"id":"c","type":"function","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer srv.Close()
	p := NewWithOptions("key", "deepseek-flash", 100, srv.URL, srv.Client(), nil, Options{Thinking: "disabled"})
	_, err := p.Complete(context.Background(), llm.Request{Message: "hi", Tools: []llm.ToolDefinition{{Name: "echo"}}, ForceTool: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["function"].(map[string]any)["name"] != "echo" {
		t.Fatalf("required named tool missing: %v", body)
	}
}

func TestInvalidToolContinuationRejectedBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	p := NewWithOptions("key", "deepseek-flash", 100, srv.URL, srv.Client(), nil, Options{Thinking: "disabled"})
	for _, req := range []llm.Request{
		{ForceTool: "missing"},
		{ToolExchanges: []llm.ToolExchange{{ToolCalls: []llm.ToolCall{{ID: "c", Name: "echo", Input: json.RawMessage(`{}`)}}}}},
		{ToolExchanges: []llm.ToolExchange{{ToolCalls: []llm.ToolCall{{ID: "c", Name: "echo", Input: json.RawMessage(`{}`)}}, Results: []llm.ToolResult{{ToolCallID: "wrong", Content: "ok"}}}}},
	} {
		if _, err := p.Complete(context.Background(), req); err == nil {
			t.Fatal("malformed continuation accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("malformed continuation reached network")
	}
}

func TestLegacyNewFlashAndHotReloadUseNonThinking(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	for _, initial := range []string{"deepseek-flash", "deepseek-v4-flash"} {
		p := New("key", initial, 100, srv.URL, srv.Client(), nil)
		p.UpdateModel("deepseek-flash")
		if _, err := p.Complete(context.Background(), llm.Request{Message: "hi", History: []domain.ChatMessage{{Role: domain.RoleAssistant, Content: "old reply"}}, Tools: []llm.ToolDefinition{{Name: "echo"}}, ForceTool: "echo"}); err != nil {
			t.Fatal(err)
		}
		if body["thinking"].(map[string]any)["type"] != "disabled" {
			t.Fatalf("new alias opted into unverified thinking via %s: %v", initial, body)
		}
	}
}

func TestEmptyChoicesIsIncompleteWithUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"served","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`)
	}))
	defer srv.Close()
	p := New("key", "deepseek-flash", 100, srv.URL, srv.Client(), nil)
	res, err := p.Complete(context.Background(), llm.Request{Message: "hi"})
	if !errors.Is(err, llm.ErrIncompleteResponse) || res.Usage.InputTokens != 4 || !res.ModelVerified {
		t.Fatalf("empty choices must be incomplete with known usage: %+v %v", res, err)
	}
}

func TestCompletionDoesNotFollowCredentialRedirect(t *testing.T) {
	var calls atomic.Int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer downstream.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, downstream.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	original := upstream.Client()
	p := New("private-key", "deepseek-flash", 100, upstream.URL, original, nil)
	if _, err := p.Complete(context.Background(), llm.Request{Message: "hi"}); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("credential request followed redirect")
	}
	if original.CheckRedirect != nil {
		t.Fatal("caller-owned HTTP client was mutated")
	}
}
