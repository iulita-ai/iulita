package assistant

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/models"
	"golang.org/x/text/language"
)

type topicProvider struct {
	mu        sync.Mutex
	requests  []llm.Request
	summary   string
	finish    string
	toolCalls []llm.ToolCall
	failAt    int
	started   chan struct{}
	release   chan struct{}
}

func (p *topicProvider) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, r)
	n := len(p.requests)
	p.mu.Unlock()
	if r.Operation == "compression" {
		if p.started != nil {
			select {
			case p.started <- struct{}{}:
			default:
			}
			select {
			case <-p.release:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		if p.failAt == n {
			return llm.Response{}, errors.New("synthetic summary failure")
		}
		return llm.Response{Content: p.summary, FinishReason: p.finish, ToolCalls: p.toolCalls}, nil
	}
	return llm.Response{Content: "Answer", Usage: llm.Usage{InputTokens: 20}}, nil
}
func (p *topicProvider) captured() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.requests...)
}

type compressionNotifier struct {
	mu     sync.Mutex
	events []channel.StatusEvent
}

func (n *compressionNotifier) NotifyStatus(_ context.Context, _ string, e channel.StatusEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if e.Type == "context_compressed" {
		n.events = append(n.events, e)
	}
	return nil
}
func (n *compressionNotifier) all() []channel.StatusEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]channel.StatusEvent(nil), n.events...)
}
func topicMsg(key, text string) channel.IncomingMessage {
	return channel.IncomingMessage{ChatID: key, UserID: "42", ResolvedUserID: "owner", Text: text, ChannelType: "telegram", ProvenPrivateRoute: true}
}
func seedTopic(t *testing.T, a *Assistant, key string, count int, content string) {
	t.Helper()
	for i := 0; i < count; i++ {
		m := &domain.ChatMessage{ChatID: key, UserID: "owner", Role: domain.RoleUser, Content: fmt.Sprintf("%d %s", i, content), CreatedAt: time.Now()}
		if e := a.store.SaveMessage(context.Background(), m); e != nil {
			t.Fatal(e)
		}
	}
}

func TestProvenTopicLoadsAllHistoryAndIgnoresOtherChatUsage(t *testing.T) {
	p := &topicProvider{summary: "summary"}
	a := newTestAssistant(t, p)
	defer a.Shutdown()
	seedTopic(t, a, "tgconv:a", 65, "EARLY_TOPIC_A")
	seedTopic(t, a, "tgconv:b", 3, "PRIVATE_B")
	a.lastInputTokens.Store(99999999)
	if _, e := a.HandleMessage(context.Background(), topicMsg("tgconv:a", "continue")); e != nil {
		t.Fatal(e)
	}
	requests := p.captured()
	if len(requests) != 1 {
		t.Fatalf("other conversation token usage triggered compression: %d calls", len(requests))
	}
	r := requests[0]
	if len(r.History) != 65 || !strings.Contains(r.History[0].Content, "EARLY_TOPIC_A") {
		t.Fatalf("topic history truncated: %d", len(r.History))
	}
	for _, m := range r.History {
		if strings.Contains(m.Content, "PRIVATE_B") {
			t.Fatal("foreign topic context leaked")
		}
	}
}

func TestTopicTurnPersistsBothRolesAndReusesAssistantContext(t *testing.T) {
	p := &topicProvider{}
	a := newTestAssistant(t, p)
	defer a.Shutdown()
	ctx := context.Background()
	key := "tgconv:persisted-answer"
	if _, err := a.HandleMessage(ctx, topicMsg(key, "First topic question")); err != nil {
		t.Fatal(err)
	}
	history, err := a.store.GetHistory(ctx, key, 0)
	if err != nil || len(history) != 2 || history[0].Role != domain.RoleUser || history[1].Role != domain.RoleAssistant || history[0].UserID != "owner" || history[1].UserID != "owner" || history[1].Content != "Answer" {
		t.Fatalf("first turn not persisted completely: %+v, %v", history, err)
	}
	if _, err = a.HandleMessage(ctx, topicMsg(key, "Follow-up question")); err != nil {
		t.Fatal(err)
	}
	requests := p.captured()
	if len(requests) != 2 || len(requests[1].History) != 2 || requests[1].History[1].Role != "assistant" || requests[1].History[1].Content != "Answer" {
		t.Fatalf("next turn lost prior assistant context or replayed the model: %+v", requests)
	}
	history, err = a.store.GetHistory(ctx, key, 0)
	if err != nil || len(history) != 4 {
		t.Fatalf("two turns not persisted exactly once: %+v, %v", history, err)
	}
}
func TestCompactionReportsAfterCommitManualAndAutomatic(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			p := &topicProvider{summary: "Preserved decisions."}
			a := newTestAssistant(t, p)
			n := &compressionNotifier{}
			a.SetStatusNotifier(n)
			key := "tgconv:compact"
			seedTopic(t, a, key, 6, strings.Repeat("earlier details ", 100)+"4111111111111111")
			ctx := i18n.WithLocale(context.Background(), language.Russian)
			before, _ := a.store.GetHistory(ctx, key, 0)
			if manual {
				report, e := a.CompactContext(ctx, key)
				if e != nil || !strings.Contains(report, "Контекст сжат") || !strings.Contains(report, "≈") {
					t.Fatal(report, e)
				}
				if len(n.all()) != 0 {
					t.Fatal("manual report would be sent twice")
				}
			} else {
				if _, e := a.compressIfNeeded(ctx, key, before, 999999); e != nil {
					t.Fatal(e)
				}
				events := n.all()
				if len(events) != 1 || !strings.Contains(events[0].Data["text"], "Контекст сжат") {
					t.Fatal(events)
				}
			}
			after, _ := a.store.GetHistory(ctx, key, 0)
			if len(after) != 4 || estimatedHistoryTokens(after) >= estimatedHistoryTokens(before) {
				t.Fatal("compaction did not commit reduced context")
			}
			for _, r := range p.captured() {
				if strings.Contains(r.Message, "4111111111111111") {
					t.Fatal("old secret reached summarizer")
				}
				if len(r.Tools) > 0 || len(r.ToolExchanges) > 0 {
					t.Fatal("summarizer inherited tools")
				}
			}
		})
	}
}
func TestSingleOversizedOldMessageUsesBoundedChunks(t *testing.T) {
	p := &topicProvider{summary: "short summary"}
	a := newTestAssistant(t, p)
	a.contextWindow = 4096
	key := "tgconv:huge"
	seedTopic(t, a, key, 1, strings.Repeat("😀 long earlier detail ", 3000))
	seedTopic(t, a, key, 1, "current")
	report, e := a.CompactContext(context.Background(), key)
	if e != nil {
		t.Fatal(report, e)
	}
	calls := p.captured()
	if len(calls) < 2 {
		t.Fatal("oversized message was not chunked")
	}
	for _, r := range calls {
		if len(r.Message) > 4096 {
			t.Fatalf("summarizer request over budget: %d", len(r.Message))
		}
	}
	h, _ := a.store.GetHistory(context.Background(), key, 0)
	if len(h) != 2 || !strings.Contains(h[1].Content, "current") {
		t.Fatal("current message lost")
	}
}
func TestFailedOrExpandingSummaryPreservesCompleteHistory(t *testing.T) {
	for _, p := range []*topicProvider{{summary: ""}, {summary: strings.Repeat("expanding ", 10000)}, {summary: "short", failAt: 2}, {summary: "partial", finish: "max_tokens"}, {summary: "partial", finish: "length"}, {summary: "partial", finish: "content_filter"}, {summary: "tool", toolCalls: []llm.ToolCall{{ID: "unexpected"}}}} {
		a := newTestAssistant(t, p)
		a.contextWindow = 4096
		key := "tgconv:failure"
		seedTopic(t, a, key, 6, strings.Repeat("old detail ", 1000))
		before, _ := a.store.GetHistory(context.Background(), key, 0)
		n := &compressionNotifier{}
		a.SetStatusNotifier(n)
		if _, e := a.CompactContext(context.Background(), key); e == nil {
			t.Fatal("summary failure reported success")
		}
		after, _ := a.store.GetHistory(context.Background(), key, 0)
		if !reflect.DeepEqual(before, after) || len(n.all()) > 0 {
			t.Fatal("failed summary deleted history or emitted success")
		}
	}
}
func TestConversationLockSerializesClearAndCompactionWithoutBlockingOtherTopic(t *testing.T) {
	p := &topicProvider{summary: "short summary", started: make(chan struct{}, 1), release: make(chan struct{})}
	a := newTestAssistant(t, p)
	defer a.Shutdown()
	seedTopic(t, a, "tgconv:a", 6, strings.Repeat("earlier ", 100))
	compacted := make(chan error, 1)
	go func() { _, e := a.CompactContext(context.Background(), "tgconv:a"); compacted <- e }()
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("summary did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if e := a.ClearConversation(ctx, "tgconv:a"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("clear bypassed held conversation lock", e)
	}
	other := make(chan error, 1)
	go func() { _, e := a.HandleMessage(context.Background(), topicMsg("tgconv:b", "independent")); other <- e }()
	select {
	case e := <-other:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("other topic blocked by global lock")
	}
	cleared := make(chan error, 1)
	go func() { cleared <- a.ClearConversation(context.Background(), "tgconv:a") }()
	close(p.release)
	if e := <-compacted; e != nil {
		t.Fatal(e)
	}
	if e := <-cleared; e != nil {
		t.Fatal(e)
	}
	h, _ := a.store.GetHistory(context.Background(), "tgconv:a", 0)
	if len(h) != 0 {
		t.Fatal("pre-clear context resurrected")
	}
	b, _ := a.store.GetHistory(context.Background(), "tgconv:b", 0)
	if len(b) != 2 {
		t.Fatal("clear affected another topic")
	}
}

func TestAutomaticTopicPreflightCompactsFullHistoryAndReports(t *testing.T) {
	p := &topicProvider{summary: "EARLY_DECISION preserved"}
	a := newTestAssistant(t, p)
	defer a.Shutdown()
	a.contextWindow = 4000
	n := &compressionNotifier{}
	a.SetStatusNotifier(n)
	seedTopic(t, a, "tgconv:auto", 60, "EARLY_DECISION "+strings.Repeat("ordinary context details ", 40))
	if _, e := a.HandleMessage(context.Background(), topicMsg("tgconv:auto", "continue from earlier decisions")); e != nil {
		t.Fatal(e)
	}
	events := n.all()
	if len(events) == 0 {
		t.Fatal("automatic committed compaction was silent")
	}
	var conversation llm.Request
	for _, r := range p.captured() {
		if r.Operation != "compression" {
			conversation = r
		}
	}
	if !strings.Contains(fmt.Sprint(conversation.History), "EARLY_DECISION") {
		t.Fatal("earlier context was neither retained nor summarized")
	}
	for _, event := range events {
		if !strings.Contains(event.Data["text"], "≈") || event.Data["messages"] == "" || event.Data["window"] != "4000" {
			t.Fatal("missing explicit post-size report", event)
		}
	}
	if estimateRequestTokens(conversation) >= int64(4000*compressionThreshold) {
		t.Fatal("preflight sent overflowing context")
	}
}

func TestManualReportUsesConversationWindowAndNoopIncludesSize(t *testing.T) {
	p := &topicProvider{summary: "short summary"}
	a := newTestAssistant(t, p)
	settings := models.Settings{SchemaVersion: 1, Profiles: []models.Profile{
		{ID: "conversation", Name: "Conversation", Connection: "openai", Model: "fixture-conversation", MaxOutputTokens: 100, Thinking: "disabled"},
		{ID: "summary", Name: "Summary", Connection: "openai", Model: "fixture-summary", MaxOutputTokens: 100, Thinking: "disabled"},
	}, Policy: models.Policy{Everyday: "conversation", Background: "summary"}}
	bindings := map[string]llm.ProfileBinding{}
	for _, profile := range settings.Profiles {
		window := 8000
		if profile.ID == "summary" {
			window = 4000
		}
		bindings[profile.ID] = llm.ProfileBinding{Profile: profile, Provider: p, Eligibility: "production_eligible", ContextTokens: window}
	}
	snapshot, err := llm.NewProfileSnapshot(1, settings, bindings)
	if err != nil {
		t.Fatal(err)
	}
	// Route metadata is independently resolved for conversation and summarizer.
	everyday := snapshot.ContextWindow(llm.Request{Operation: "conversation"})
	cheap := snapshot.ContextWindow(llm.Request{RouteHint: llm.RouteHintCheap})
	if everyday == cheap {
		t.Fatal("fixture must use differing model windows")
	}
	a.provider = &topicProfileProvider{topicProvider: p, snapshot: snapshot}
	seedTopic(t, a, "tgconv:window", 6, strings.Repeat("prior detail ", 100))
	report, err := a.CompactContext(context.Background(), "tgconv:window")
	if err != nil || !strings.Contains(report, fmt.Sprintf("Model window: %d", everyday)) || !strings.Contains(report, "messages: 6 → 4") {
		t.Fatal(report, err)
	}
	empty, err := a.CompactContext(context.Background(), "tgconv:empty")
	if err != nil || !strings.Contains(empty, "≈0") || !strings.Contains(empty, fmt.Sprintf("Model window: %d", everyday)) {
		t.Fatal(empty, err)
	}
}

type topicProfileProvider struct {
	*topicProvider
	snapshot *llm.ProfileSnapshot
}

func (p *topicProfileProvider) AcquireProfileSnapshot() *llm.ProfileSnapshot { return p.snapshot }

func TestManualCompactionLargeWindowDoesNotOverflow(t *testing.T) {
	a := newTestAssistant(t, &topicProvider{summary: "short complete summary", finish: "stop"})
	a.contextWindow = math.MaxInt
	seedTopic(t, a, "tgconv:large-window", 6, strings.Repeat("earlier details ", 100))
	report, err := a.CompactContext(context.Background(), "tgconv:large-window")
	if err != nil || !strings.Contains(report, "Context compacted") {
		t.Fatal(report, err)
	}
}

func TestRequestEstimateRejectsMalformedToolJSON(t *testing.T) {
	cases := []struct {
		name string
		req  llm.Request
	}{
		{name: "tool schema", req: llm.Request{Tools: []llm.ToolDefinition{{Name: "invalid", InputSchema: []byte("{")}}}},
		{name: "tool arguments", req: llm.Request{ToolExchanges: []llm.ToolExchange{{ToolCalls: []llm.ToolCall{{ID: "invalid", Input: []byte("{")}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimateRequestTokens(tc.req); got != math.MaxInt64 {
				t.Fatalf("malformed tool context must fail admission conservatively, got %d", got)
			}
		})
	}
	valid := llm.Request{Tools: []llm.ToolDefinition{{Name: "valid", InputSchema: []byte(`{"type":"object"}`)}}}
	if got := estimateRequestTokens(valid); got <= 32 || got == math.MaxInt64 {
		t.Fatalf("valid tool context must remain countable, got %d", got)
	}
}
