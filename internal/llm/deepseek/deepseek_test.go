package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
)

// --- pure helpers --------------------------------------------------------------

func TestBuildToolDefs_VerbatimAndEmptySchema(t *testing.T) {
	tools := []llm.ToolDefinition{
		{Name: "with_schema", Description: "d", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)},
		{Name: "empty_schema", InputSchema: nil},
		{Name: "null_schema", InputSchema: json.RawMessage(`null`)},
	}
	defs := buildToolDefs(tools)
	if len(defs) != 3 {
		t.Fatalf("expected 3 defs, got %d", len(defs))
	}
	if defs[0].Type != "function" || defs[0].Function.Name != "with_schema" {
		t.Errorf("def[0] = %+v", defs[0])
	}
	if string(defs[0].Function.Parameters) != `{"type":"object","properties":{"x":{"type":"string"}}}` {
		t.Errorf("schema not passed verbatim: %s", defs[0].Function.Parameters)
	}
	want := `{"type":"object","properties":{}}`
	if string(defs[1].Function.Parameters) != want {
		t.Errorf("empty schema = %s, want %s", defs[1].Function.Parameters, want)
	}
	if string(defs[2].Function.Parameters) != want {
		t.Errorf("null schema = %s, want %s", defs[2].Function.Parameters, want)
	}
}

func TestBuildToolDefs_Nil(t *testing.T) {
	if buildToolDefs(nil) != nil {
		t.Error("expected nil for no tools")
	}
}

func TestBuildMessages_OrderSystemHistoryMessage(t *testing.T) {
	req := llm.Request{
		StaticSystemPrompt: "static",
		SystemPrompt:       "dynamic",
		History: []domain.ChatMessage{
			{Role: domain.RoleUser, Content: "hi"},
			{Role: domain.RoleAssistant, Content: ""}, // skipped
			{Role: domain.RoleAssistant, Content: "hello"},
		},
		Message: "now",
	}
	msgs := buildMessages(req)
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	want := []string{"system", "user", "assistant", "user"}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	if msgs[0].Content == nil || *msgs[0].Content != "static\n\ndynamic" {
		t.Errorf("system content = %v", msgs[0].Content)
	}
}

func TestBuildMessages_ToolExchangeReplay(t *testing.T) {
	req := llm.Request{
		Message: "do it",
		ToolExchanges: []llm.ToolExchange{
			{
				AssistantText: "calling",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "search", Input: json.RawMessage(`{"q":"go"}`)},
				},
				Results: []llm.ToolResult{
					{ToolCallID: "call_1", Content: "result text"},
					{ToolCallID: "call_2", Content: ""}, // empty content must round-trip as ""
				},
			},
		},
	}
	msgs := buildMessages(req)
	// user("do it"), assistant(tool_calls), tool(result), tool(empty)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 msgs, got %d: %+v", len(msgs), msgs)
	}
	asst := msgs[1]
	if asst.Role != "assistant" || asst.Content == nil || *asst.Content != "calling" {
		t.Errorf("assistant msg = %+v", asst)
	}
	if len(asst.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(asst.ToolCalls))
	}
	tc := asst.ToolCalls[0]
	if tc.ID != "call_1" || tc.Type != "function" || tc.Function.Name != "search" {
		t.Errorf("tool call = %+v", tc)
	}
	if tc.Function.Arguments != `{"q":"go"}` {
		t.Errorf("arguments = %q, want JSON string", tc.Function.Arguments)
	}
	tool1, tool2 := msgs[2], msgs[3]
	if tool1.Role != "tool" || tool1.ToolCallID != "call_1" || tool1.Content == nil || *tool1.Content != "result text" {
		t.Errorf("tool result 1 = %+v", tool1)
	}
	if tool2.Content == nil || *tool2.Content != "" {
		t.Errorf("empty tool result should serialize content as \"\", got %v", tool2.Content)
	}
}

func TestBuildMessages_AssistantOnlyToolCallsOmitsContent(t *testing.T) {
	req := llm.Request{
		ToolExchanges: []llm.ToolExchange{
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "t", Input: json.RawMessage(`{}`)}}},
		},
	}
	msgs := buildMessages(req)
	b, err := json.Marshal(msgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"content"`) {
		t.Errorf("assistant message with only tool_calls must omit content: %s", b)
	}
}

func TestBuildMessages_EmptyExchangeNotEmitted(t *testing.T) {
	// A ToolExchange with no assistant text and no tool calls must not produce
	// a bare {"role":"assistant"} message (API rejects it).
	req := llm.Request{
		Message:       "hi",
		ToolExchanges: []llm.ToolExchange{{Results: []llm.ToolResult{{ToolCallID: "x", Content: "r"}}}},
	}
	msgs := buildMessages(req)
	for _, m := range msgs {
		if m.Role == "assistant" && m.Content == nil && len(m.ToolCalls) == 0 {
			t.Fatalf("emitted empty assistant turn: %+v", msgs)
		}
	}
}

func TestBuildToolChoice(t *testing.T) {
	if buildToolChoice(llm.Request{}) != nil {
		t.Error("expected nil tool choice by default")
	}
	tc := buildToolChoice(llm.Request{ForceTool: "weather"})
	b, _ := json.Marshal(tc)
	want := `{"function":{"name":"weather"},"type":"function"}`
	if string(b) != want {
		t.Errorf("forced tool choice = %s, want %s", b, want)
	}
}

func TestToolCallAccumulator_FragmentedByIndex(t *testing.T) {
	acc := newToolCallAccumulator()
	// Two tool calls interleaved, args fragmented.
	acc.add(0, "call_a", "search", `{"q":`)
	acc.add(1, "call_b", "weather", `{"city`)
	acc.add(0, "", "", `"go"}`)
	acc.add(1, "", "", `":"NYC"}`)
	calls := acc.finalize()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].ID != "call_a" || calls[0].Name != "search" || string(calls[0].Input) != `{"q":"go"}` {
		t.Errorf("call[0] = %+v", calls[0])
	}
	if calls[1].ID != "call_b" || calls[1].Name != "weather" || string(calls[1].Input) != `{"city":"NYC"}` {
		t.Errorf("call[1] = %+v", calls[1])
	}
}

func TestToolCallAccumulator_Empty(t *testing.T) {
	if newToolCallAccumulator().finalize() != nil {
		t.Error("expected nil for no tool calls")
	}
}

func TestMapUsage_Phase1Fallback(t *testing.T) {
	u := mapUsage(chatUsage{PromptTokens: 100, CompletionTokens: 40, PromptCacheHitTokens: 80, PromptCacheMissTokens: 20})
	if u.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (all prompt tokens at input rate in P1)", u.InputTokens)
	}
	if u.OutputTokens != 40 {
		t.Errorf("OutputTokens = %d, want 40", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 0 || u.CacheCreationInputTokens != 0 {
		t.Errorf("cache fields must be zero in P1, got read=%d create=%d", u.CacheReadInputTokens, u.CacheCreationInputTokens)
	}
}

func TestAPIError_StatusCodeAndInterface(t *testing.T) {
	var err error = &apiError{status: 429, body: "rate limited"}
	var hse llm.HTTPStatusError
	if !errors.As(err, &hse) {
		t.Fatal("apiError must satisfy llm.HTTPStatusError")
	}
	if hse.StatusCode() != 429 {
		t.Errorf("StatusCode = %d, want 429", hse.StatusCode())
	}
}

func TestErrorFromResponse_BoundsBody(t *testing.T) {
	huge := strings.Repeat("x", 100*1024)
	resp := &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(huge))}
	err := errorFromResponse("deepseek completion", resp)
	if len(err.Error()) > errBodyLimit+64 {
		t.Errorf("error string not bounded: len=%d", len(err.Error()))
	}
	var hse llm.HTTPStatusError
	if !errors.As(err, &hse) || hse.StatusCode() != 500 {
		t.Errorf("expected retryable 500 apiError, got %v", err)
	}
}

func TestErrorFromResponse_PrefersStructuredMessage(t *testing.T) {
	body := `{"error":{"message":"invalid model","code":"invalid_request_error"}}`
	resp := &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body))}
	err := errorFromResponse("deepseek completion", resp)
	if !strings.Contains(err.Error(), "invalid model") {
		t.Errorf("error should surface structured message: %v", err)
	}
}

func TestIsContextOverflowError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"structured code", 400, `{"error":{"code":"context_length_exceeded"}}`, true},
		{"substring context length", 400, `{"error":{"message":"This model's maximum context length is 65536 tokens"}}`, true},
		{"unrelated too long", 400, `{"error":{"message":"field 'name' is too long"}}`, false},
		{"non-400", 500, `{"error":{"code":"context_length_exceeded"}}`, false},
		{"plain 400", 400, `{"error":{"message":"bad request"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isContextOverflowError(tt.status, tt.body); got != tt.want {
				t.Errorf("isContextOverflowError(%d, %q) = %v, want %v", tt.status, tt.body, got, tt.want)
			}
		})
	}
}

// --- HTTP integration ----------------------------------------------------------

func TestComplete_RequestAndResponse(t *testing.T) {
	var gotBody []byte
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"choices":[{"message":{"content":"hi there","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"go\"}"}}
			]}}],
			"usage":{"prompt_tokens":12,"completion_tokens":5,"prompt_cache_hit_tokens":8,"prompt_cache_miss_tokens":4}
		}`)
	}))
	defer srv.Close()

	p := New("secret-key", "deepseek-v4-flash", 1024, srv.URL, srv.Client(), nil)
	resp, err := p.Complete(context.Background(), llm.Request{Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if resp.Content != "hi there" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.Provider != "deepseek" || resp.Model != "deepseek-v4-flash" {
		t.Errorf("provider/model = %q/%q", resp.Provider, resp.Model)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "search" || string(resp.ToolCalls[0].Input) != `{"q":"go"}` {
		t.Errorf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	// Non-stream request must not contain stream fields (checked separately so
	// neither assertion subsumes the other).
	if strings.Contains(string(gotBody), `"stream":`) {
		t.Errorf("Complete request must omit stream key: %s", gotBody)
	}
	if strings.Contains(string(gotBody), `"stream_options"`) {
		t.Errorf("Complete request must omit stream_options key: %s", gotBody)
	}
	if !strings.Contains(string(gotBody), `"model":"deepseek-v4-flash"`) {
		t.Errorf("request missing model: %s", gotBody)
	}
}

func TestCompleteStream_SSE(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		lines := []string{
			": keep-alive\n\n",
			`data: {"choices":[{"delta":{"content":"Hel"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"lo"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"search","arguments":"{\"q\":"}}]}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go\"}"}}]}}]}` + "\n\n",
			`data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, l := range lines {
			io.WriteString(w, l)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()

	p := New("k", "deepseek-v4-flash", 1024, srv.URL, srv.Client(), nil)
	var chunks []string
	resp, err := p.CompleteStream(context.Background(), llm.Request{Message: "hi"}, func(c string) {
		chunks = append(chunks, c)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(chunks, "") != "Hello" || resp.Content != "Hello" {
		t.Errorf("streamed content = %q (chunks %v)", resp.Content, chunks)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_x" || string(resp.ToolCalls[0].Input) != `{"q":"go"}` {
		t.Errorf("reassembled tool calls = %+v", resp.ToolCalls)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if !strings.Contains(string(gotBody), `"include_usage":true`) {
		t.Errorf("stream request must set stream_options.include_usage: %s", gotBody)
	}
}

func TestComplete_RetryOn429(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()

	p := New("k", "deepseek-v4-flash", 1024, srv.URL, srv.Client(), nil)
	retry := llm.NewRetryProvider(p, llm.RetryConfig{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond})
	resp, err := retry.Complete(context.Background(), llm.Request{Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", calls)
	}
}

func TestComplete_ContextOverflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":"context_length_exceeded","message":"too large"}}`)
	}))
	defer srv.Close()

	p := New("k", "deepseek-v4-flash", 1024, srv.URL, srv.Client(), nil)
	_, err := p.Complete(context.Background(), llm.Request{Message: "hi"})
	if !errors.Is(err, llm.ErrContextTooLarge) {
		t.Errorf("expected ErrContextTooLarge, got %v", err)
	}
}

func TestCompleteStream_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"part"}}]}`+"\n\n")
		if fl != nil {
			fl.Flush()
		}
		<-r.Context().Done() // block until client cancels
	}))
	defer srv.Close()

	p := New("k", "deepseek-v4-flash", 1024, srv.URL, srv.Client(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := p.CompleteStream(ctx, llm.Request{Message: "hi"}, func(string) {})
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestProvider_ConcurrentUpdateAndComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()

	p := New("k", "deepseek-v4-flash", 1024, srv.URL, srv.Client(), nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(n int) { defer wg.Done(); p.UpdateModel(fmt.Sprintf("m-%d", n)); p.UpdateMaxTokens(100 + n) }(i)
		go func() { defer wg.Done(); _, _ = p.Complete(context.Background(), llm.Request{Message: "hi"}) }()
	}
	wg.Wait()
}

func ExampleProvider() {
	p := New("key", "deepseek-v4-flash", 4096, "", nil, nil)
	fmt.Println(p.endpoint())
	// Output: https://api.deepseek.com/v1/chat/completions
}
