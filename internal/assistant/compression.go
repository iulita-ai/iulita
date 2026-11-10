package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/security"
	"go.uber.org/zap"
)

const (
	compressionThreshold = 0.8
	defaultSummaryPrefix = "[Summary of earlier conversation]\n"
	defaultContextWindow = 200000
)

type manualCompressionKey struct{}

func estimatedTextTokens(s string) int64 { return int64(len(s))/3 + int64((len(s)%3+2)/3) }
func forcedCompressionTokens(window int) int64 {
	if int64(window) > math.MaxInt64/2 {
		return math.MaxInt64
	}
	return int64(window) * 2
}
func estimatedHistoryTokens(history []domain.ChatMessage) int64 {
	var n int64
	for _, m := range history {
		n += estimatedTextTokens(m.Content) + 8
	}
	return n
}
func estimateRequestTokens(req llm.Request) int64 {
	n := estimatedHistoryTokens(req.History) + estimatedTextTokens(req.Message) + estimatedTextTokens(req.SystemPrompt) + estimatedTextTokens(req.StaticSystemPrompt) + 32
	tools, toolsErr := json.Marshal(req.Tools)
	if toolsErr != nil {
		// Invalid tool schemas cannot be safely admitted as an empty context.
		return math.MaxInt64
	}
	exchanges, exchangesErr := json.Marshal(req.ToolExchanges)
	if exchangesErr != nil {
		return math.MaxInt64
	}
	n += estimatedTextTokens(string(tools)) + estimatedTextTokens(string(exchanges))
	n += int64(len(req.Images)) * 2048
	for _, d := range req.Documents {
		n += estimatedTextTokens(string(d.Data))
	}
	return n
}
func compressionReport(ctx context.Context, before, after []domain.ChatMessage, window int) string {
	if strings.HasPrefix(i18n.LocaleFrom(ctx).String(), "ru") {
		return fmt.Sprintf("Контекст сжат. Оценка истории: ≈%d → ≈%d токенов; сообщений: %d → %d. Окно модели: %d токенов.", estimatedHistoryTokens(before), estimatedHistoryTokens(after), len(before), len(after), window)
	}
	return fmt.Sprintf("Context compacted. Estimated history: ≈%d → ≈%d tokens; messages: %d → %d. Model window: %d tokens.", estimatedHistoryTokens(before), estimatedHistoryTokens(after), len(before), len(after), window)
}
func compactNoop(ctx context.Context, history []domain.ChatMessage, window int) string {
	if strings.HasPrefix(i18n.LocaleFrom(ctx).String(), "ru") {
		return fmt.Sprintf("Сжатие не требуется. Оценка истории: ≈%d токенов; %d сообщений. Окно модели: %d токенов.", estimatedHistoryTokens(history), len(history), window)
	}
	return fmt.Sprintf("No compaction needed. Estimated history: ≈%d tokens; %d messages. Model window: %d tokens.", estimatedHistoryTokens(history), len(history), window)
}
func conversationContextRequest(key string, h []domain.ChatMessage) llm.Request {
	r := llm.Request{ChatID: key, Operation: "conversation"}
	if len(h) > 0 {
		r.UserID = h[0].UserID
	}
	return r
}
func llmCompressionRequest(key string, h []domain.ChatMessage) llm.Request {
	r := llm.Request{ChatID: key, Operation: "compression", RouteHint: llm.RouteHintCheap}
	if len(h) > 0 {
		r.UserID = h[0].UserID
	}
	return r
}
func (a *Assistant) compressIfNeeded(ctx context.Context, key string, history []domain.ChatMessage, inputTokens int64, routing ...llm.Request) ([]domain.ChatMessage, error) {
	if guardErr := a.guardConversation(ctx, key); guardErr != nil {
		return history, guardErr
	}
	window := a.compressionContextWindow(routing...)
	minimum := 4
	if strings.HasPrefix(key, "tgconv:") {
		minimum = 2
	}
	if window <= 0 || inputTokens < int64(float64(window)*compressionThreshold) || len(history) < minimum {
		return history, nil
	}
	split := len(history) / 2
	old, tail := history[:split], history[split:]
	var source strings.Builder
	for _, m := range old {
		fmt.Fprintf(&source, "%s: %s\n", m.Role, security.RedactSensitiveText(m.Content))
	}
	req := llmCompressionRequest(key, old)
	if len(routing) > 0 {
		req.RoutingSnapshot = routing[0].RoutingSnapshot
	}
	req.SystemPrompt = i18n.T(ctx, "AssistantCompressionPrompt")
	// The summarizer can be a smaller profile than the conversation model.
	summaryWindow := a.compressionContextWindow(req)
	if summaryWindow <= 0 {
		summaryWindow = defaultContextWindow
	}
	budget := 32768
	if summaryWindow < 32768+len(req.SystemPrompt) {
		budget = summaryWindow - len(req.SystemPrompt)
	}
	if budget < 64 {
		return history, fmt.Errorf("summarizer context budget is too small")
	}
	text, err := a.summarizeBounded(ctx, req, source.String(), budget)
	if err != nil {
		return history, err
	}
	replacer, ok := a.store.(interface {
		ReplaceMessagesWithSummary(context.Context, string, int64, *domain.ChatMessage) error
	})
	// Legacy mock/other stores without an atomic replacement cannot compact.
	if !ok {
		return history, nil
	}
	if strings.TrimSpace(text) == "" {
		return history, fmt.Errorf("summarizer returned an empty result")
	}
	prefix := i18n.T(ctx, "AssistantSummaryPrefix")
	if prefix == "AssistantSummaryPrefix" {
		prefix = defaultSummaryPrefix
	}
	summary := security.RedactSensitiveText(prefix + text)
	result := make([]domain.ChatMessage, 0, 1+len(tail))
	result = append(result, domain.ChatMessage{ID: old[len(old)-1].ID, ChatID: key, UserID: old[0].UserID, Role: domain.RoleAssistant, Content: summary, CreatedAt: old[0].CreatedAt})
	result = append(result, tail...)
	if estimatedHistoryTokens(result) >= estimatedHistoryTokens(history) {
		return history, fmt.Errorf("summary does not reduce conversation context")
	}
	// Refuse to delete unseen/stale rows. The conversation lock protects writers
	// in this process; atomic replacement still protects insertion failures.
	fresh, e := a.store.GetHistory(ctx, key, 0)
	if e != nil {
		return history, fmt.Errorf("verify context before compaction: %w", e)
	}
	var observed []domain.ChatMessage
	lastID := old[len(old)-1].ID
	for _, m := range fresh {
		if m.ID <= lastID {
			m.Content = security.RedactSensitiveText(m.Content)
			observed = append(observed, m)
		}
	}
	expected := append([]domain.ChatMessage(nil), old...)
	for i := range expected {
		expected[i].Content = security.RedactSensitiveText(expected[i].Content)
	}
	if !reflect.DeepEqual(observed, expected) {
		return history, fmt.Errorf("conversation changed during compaction; history preserved")
	}
	if guardErr := a.guardConversation(ctx, key); guardErr != nil {
		return history, guardErr
	}
	if err = replacer.ReplaceMessagesWithSummary(ctx, key, lastID, &result[0]); err != nil {
		return history, fmt.Errorf("replace conversation history with summary: %w", err)
	}
	if manual, ok := ctx.Value(manualCompressionKey{}).(bool); !ok || !manual {
		a.notifyStatus(ctx, key, channel.StatusEvent{Type: "context_compressed", Data: map[string]string{"text": compressionReport(ctx, history, result, window), "before_tokens": strconv.FormatInt(estimatedHistoryTokens(history), 10), "after_tokens": strconv.FormatInt(estimatedHistoryTokens(result), 10), "before_messages": strconv.Itoa(len(history)), "after_messages": strconv.Itoa(len(result)), "messages": strconv.Itoa(len(result)), "window": strconv.Itoa(window)}})
	}
	return result, nil
}
func splitSummaryInput(s string, budget int) ([]string, error) {
	var out []string
	for s != "" {
		n := budget
		if n > len(s) {
			n = len(s)
		}
		for n > 0 && !utf8.ValidString(s[:n]) {
			n--
		}
		if n == 0 {
			return nil, fmt.Errorf("invalid UTF-8 conversation")
		}
		out = append(out, s[:n])
		s = s[n:]
		if len(out) > 128 {
			return nil, fmt.Errorf("conversation exceeds bounded summarization work budget")
		}
	}
	return out, nil
}
func (a *Assistant) summarizeBounded(ctx context.Context, req llm.Request, source string, budget int) (string, error) {
	for round := 0; round < 8; round++ {
		chunks, err := splitSummaryInput(source, budget)
		if err != nil {
			return "", err
		}
		var summaries []string
		for _, chunk := range chunks {
			request := req
			request.Message = chunk
			if err := a.guardConversation(ctx, req.ChatID); err != nil {
				return "", err
			}
			redactRequest(&request)
			reply, e := a.provider.Complete(ctx, request)
			if err := a.guardConversation(ctx, req.ChatID); err != nil {
				return "", err
			}
			if e != nil {
				return "", fmt.Errorf("generate conversation summary: %w", e)
			}
			// A partial or tool-seeking response cannot replace durable history.
			// Empty finish reason is retained for adapters without metadata.
			if len(reply.ToolCalls) > 0 || (reply.FinishReason != "" && reply.FinishReason != "stop" && reply.FinishReason != "end_turn" && reply.FinishReason != "stop_sequence") {
				return "", fmt.Errorf("summarizer did not complete a plain-text response")
			}
			safe := security.RedactSensitiveText(strings.TrimSpace(reply.Content))
			if safe == "" {
				return "", fmt.Errorf("summarizer returned an empty result")
			}
			if len(safe) > budget {
				return "", fmt.Errorf("summary exceeds bounded output budget")
			}
			summaries = append(summaries, safe)
		}
		if len(chunks) == 1 {
			return summaries[0], nil
		}
		joined := strings.Join(summaries, "\n")
		if len(joined) >= len(source) {
			return "", fmt.Errorf("chunk summaries do not shrink context")
		}
		source = joined
	}
	return "", fmt.Errorf("summarization exceeded reduction rounds")
}
func (a *Assistant) forceCompressRequest(ctx context.Context, key string, req *llm.Request, history []domain.ChatMessage) []domain.ChatMessage {
	compressed, err := a.compressIfNeeded(ctx, key, history, forcedCompressionTokens(a.compressionContextWindow(*req)), *req)
	if err != nil {
		a.logger.Error("forced compression failed", zap.Error(err))
		return history
	}
	if len(compressed) > 1 {
		req.History = compressed[:len(compressed)-1]
	} else {
		req.History = nil
	}
	return compressed
}

// CompressNow manually compacts a conversation and returns the number of older messages summarized.
func (a *Assistant) CompressNow(ctx context.Context, key string) (int, error) {
	if err := domain.CheckLegacyChatID(key); err != nil {
		return 0, err
	}
	ctx, stop, lifecycleErr := a.beginConversation(ctx, key)
	if lifecycleErr != nil {
		return 0, lifecycleErr
	}
	defer stop()
	release, e := a.lockConversation(ctx, key)
	if e != nil {
		return 0, e
	}
	defer release()
	if err := a.guardConversation(ctx, key); err != nil {
		return 0, err
	}
	history, e := a.store.GetHistory(ctx, key, 0)
	if e != nil {
		return 0, e
	}
	if len(history) < 4 {
		return 0, fmt.Errorf("not enough messages to compress (%d, need at least 4)", len(history))
	}
	ctx = context.WithValue(ctx, manualCompressionKey{}, true)
	next, e := a.compressIfNeeded(ctx, key, history, forcedCompressionTokens(a.compressionContextWindow()))
	if e != nil {
		return 0, e
	}
	if estimatedHistoryTokens(next) >= estimatedHistoryTokens(history) {
		return 0, nil
	}
	return len(history) / 2, nil
}
func (a *Assistant) compressionContextWindow(request ...llm.Request) int {
	req := llm.Request{}
	if len(request) > 0 {
		req = request[0]
	}
	snapshot := req.RoutingSnapshot
	if snapshot == nil {
		if p, ok := a.provider.(llm.ProfileInvoker); ok {
			snapshot = p.AcquireProfileSnapshot()
		}
	}
	if window := snapshot.ContextWindow(req); window > 0 {
		return window
	}
	if a.contextWindow > 0 {
		return a.contextWindow
	}
	return defaultContextWindow
}

func (a *Assistant) fitTopicRequest(ctx context.Context, key string, history []domain.ChatMessage, req *llm.Request) ([]domain.ChatMessage, error) {
	window := a.compressionContextWindow(*req)
	for pass := 0; pass < 12 && estimateRequestTokens(*req) >= int64(float64(window)*compressionThreshold); pass++ {
		next, e := a.compressIfNeeded(ctx, key, history, forcedCompressionTokens(window), *req)
		if e != nil {
			return history, fmt.Errorf("topic context could not be compacted: %w", e)
		}
		if estimatedHistoryTokens(next) >= estimatedHistoryTokens(history) {
			break
		}
		history = next
		if len(history) > 1 {
			req.History = history[:len(history)-1]
		} else {
			req.History = nil
		}
	}
	if estimateRequestTokens(*req) >= int64(float64(window)*compressionThreshold) {
		return history, fmt.Errorf("topic context exceeds model window; input or tool overhead must be reduced")
	}
	return history, nil
}
