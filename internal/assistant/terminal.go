package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
)

type approvedToolKey struct{}

func (a *Assistant) isTerminalTool(name string) bool {
	s, ok := a.registry.Registered(name)
	if !ok {
		return false
	}
	terminal, ok := s.(skill.TerminalDeclarer)
	return ok && terminal.TerminalResult()
}
func (a *Assistant) hasTerminalCall(calls []llm.ToolCall) bool {
	for _, call := range calls {
		if a.isTerminalTool(call.Name) {
			return true
		}
	}
	return false
}

// finishResponse has exactly one persistence/event path. Immutable native
// operations bypass both mutating hooks and the self-improvement transcript.
func (a *Assistant) finishResponse(ctx context.Context, chatID, userID, response string, immutable bool, iterations int, exchanges []llm.ToolExchange) (string, error) {
	if !immutable {
		a.runPostHooks(ctx, &response)
	}
	// An upstream deadline can expire just before finalization. Persist already
	// completed work with a bounded context, without resubmitting the operation.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	id := a.saveAssistantResponse(saveCtx, chatID, response)
	if immutable && id == 0 {
		return "", fmt.Errorf("could not save completed operation result")
	}
	if immutable {
		channel.MarkVerbatimResponse(ctx)
	}
	if !immutable {
		a.maybeEnqueueSkillReview(saveCtx, chatID, userID, iterations, id, summarizeToolExchanges(exchanges))
	}
	if hasToolCall(exchanges, "remember") {
		a.notifyStatus(saveCtx, chatID, channel.StatusEvent{Type: "skip_bookmark"})
	}
	if a.bus != nil {
		a.bus.Publish(saveCtx, eventbus.Event{Type: eventbus.ResponseSent, Payload: eventbus.ResponseSentPayload{ChatID: chatID, Response: response, Time: time.Now()}})
	}
	return response, nil
}

func (a *Assistant) unavailableToolMessage(ctx context.Context, name string) string {
	if a.isTerminalTool(name) {
		return i18n.T(ctx, "TranslationUnavailable")
	}
	return fmt.Sprintf("error: unknown skill %q", name)
}
func terminalErrorCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	return "provider_unavailable"
}
func translationErrorMessage(ctx context.Context, err error) string {
	key := "TranslationFailed"
	switch terminalErrorCode(err) {
	case "invalid_input", "unsupported_language", "same_language":
		key = "TranslationInvalidInput"
	case "text_too_long":
		key = "TranslationTooLong"
	case "busy", "rate_limited":
		key = "TranslationBusy"
	case "auth_failed", "permission_denied":
		key = "TranslationAccessDenied"
	case "timeout", "cancelled":
		key = "TranslationTimeout"
	case "incomplete_result", "protocol_error":
		key = "TranslationIncomplete"
	case "not_configured", "disabled", "forbidden", "credential_revoked", "runtime_unavailable":
		key = "TranslationUnavailable"
	}
	return i18n.T(ctx, key)
}

// explicitTranslationRequest only suppresses unrelated substring force triggers
// in quoted source text. Tool choice stays semantic; this never forces a paid
// translation. Availability is intentionally irrelevant (revocation must not
// turn the quoted source into an action).
func (a *Assistant) explicitTranslationRequest(text string) bool {
	if _, ok := a.registry.Registered("translate"); !ok {
		return false
	}
	text = strings.ToLower(strings.TrimSpace(text))
	for _, polite := range []string{"please ", "пожалуйста, ", "пожалуйста ", "s’il vous plaît, ", "s'il vous plaît, ", "por favor, ", "por favor ", "נא ", "בבקשה ", "请"} {
		text = strings.TrimPrefix(text, polite)
	}
	for _, question := range []string{"can you translate", "could you translate", "would you translate", "можешь перевести", "можете перевести"} {
		if strings.HasPrefix(text, question) {
			rest := strings.TrimPrefix(text, question)
			if rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, ":") {
				return true
			}
		}
	}
	for _, prefix := range []string{"translate", "переведи", "переведите", "traduis", "traduisez", "traduce", "traduzca", "翻译", "תרגם", "תרגמי", "תרגמו"} {
		if strings.HasPrefix(text, prefix) {
			rest := strings.TrimPrefix(text, prefix)
			if rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, ":") || prefix == "翻译" {
				return true
			}
		}
	}
	return false
}
