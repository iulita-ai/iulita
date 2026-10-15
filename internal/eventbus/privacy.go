package eventbus

import "github.com/iulita-ai/iulita/internal/domain"

// GlobalEventAllowed is a second boundary after the legacy ingestion guard.
// Mobile events require an owner-scoped stream; even metadata IDs stay private.
// Content events accept only their typed envelopes, preventing map/string bypasses.
func GlobalEventAllowed(evt Event) bool {
	switch evt.Type {
	case MessageReceived:
		switch evt.Payload.(type) {
		case MessageReceivedPayload, *MessageReceivedPayload:
		default:
			return false
		}
	case ResponseSent:
		switch evt.Payload.(type) {
		case ResponseSentPayload, *ResponseSentPayload:
		default:
			return false
		}
	}
	switch p := evt.Payload.(type) {
	case MessageReceivedPayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *MessageReceivedPayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	case ResponseSentPayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *ResponseSentPayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	case SkillExecutedPayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *SkillExecutedPayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	case LLMUsagePayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *LLMUsagePayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	case TaskCompletedPayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *TaskCompletedPayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	case TaskFailedPayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *TaskFailedPayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	case InsightCreatedPayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *InsightCreatedPayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	case FactSavedPayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *FactSavedPayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	case FactDeletedPayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *FactDeletedPayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	case OrchestrationStartedPayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *OrchestrationStartedPayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	case OrchestrationDonePayload:
		return !domain.IsMobileChatID(p.ChatID)
	case *OrchestrationDonePayload:
		return p != nil && !domain.IsMobileChatID(p.ChatID)
	default:
		return true
	}
}
