package assistant

import (
	"strings"
	"sync"

	"golang.org/x/text/language"

	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
)

// approvalStore holds pending approvals keyed by chatID.
// Only one pending approval per chat at a time (simplest safe model).
type approvalStore struct {
	mu      sync.Mutex
	pending map[string]*pendingApproval
}

type pendingApproval struct {
	tc    llm.ToolCall
	level skill.ApprovalLevel
}

func newApprovalStore() *approvalStore {
	return &approvalStore{pending: make(map[string]*pendingApproval)}
}

func (s *approvalStore) set(chatID string, tc llm.ToolCall, level skill.ApprovalLevel) {
	s.mu.Lock()
	s.pending[chatID] = &pendingApproval{tc: tc, level: level}
	s.mu.Unlock()
}

func (s *approvalStore) take(chatID string) (*pendingApproval, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[chatID]
	if ok {
		delete(s.pending, chatID)
	}
	return p, ok
}

// isApprovalResponse checks if a user message is an affirmative or negative response.
// Uses locale-aware approval vocabulary from the i18n catalog.
func isApprovalResponse(text string, tag language.Tag) (approved bool, isDefined bool) {
	b := i18n.DefaultBundle()
	if b == nil {
		// Fallback to hardcoded words if i18n is not initialized.
		lower := strings.ToLower(strings.TrimSpace(text))
		switch lower {
		case "yes", "y", "да", "confirm", "ok", "approve", "ок":
			return true, true
		case "no", "n", "нет", "cancel", "deny", "reject", "отмена":
			return false, true
		}
		return false, false
	}

	if b.IsApprovalAffirmative(tag, text) {
		return true, true
	}
	if b.IsApprovalNegative(tag, text) {
		return false, true
	}
	return false, false
}
