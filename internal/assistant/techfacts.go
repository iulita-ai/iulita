package assistant

import (
	"context"
	"fmt"
	"time"
	"unicode"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

// TechFactAnalyzer performs lightweight per-message analysis to extract
// technical facts (language, message style) without LLM calls.
type TechFactAnalyzer struct {
	store  storage.Repository
	logger *zap.Logger
}

// NewTechFactAnalyzer creates a new analyzer.
func NewTechFactAnalyzer(store storage.Repository, logger *zap.Logger) *TechFactAnalyzer {
	return &TechFactAnalyzer{store: store, logger: logger}
}

// AnalyzeMessage runs lightweight heuristics on the message text.
// Designed to be called in a goroutine (fire-and-forget).
func (a *TechFactAnalyzer) AnalyzeMessage(ctx context.Context, chatID, userID, text string) {
	if text == "" {
		return
	}

	a.detectLanguage(ctx, chatID, userID, text)
	a.detectMessageLength(ctx, chatID, userID, text)
}

func (a *TechFactAnalyzer) detectLanguage(ctx context.Context, chatID, userID, text string) {
	var cyrillic, latin int
	for _, r := range text {
		if unicode.Is(unicode.Cyrillic, r) {
			cyrillic++
		} else if unicode.Is(unicode.Latin, r) {
			latin++
		}
	}

	total := cyrillic + latin
	if total < 5 {
		return // not enough letter data
	}

	// Upsert each detected language separately.
	// update_count accumulates how many messages used this language,
	// confidence stores the ratio in this message (averaged over time via UpsertTechFact).
	if cyrillic > 0 {
		ratio := float64(cyrillic) / float64(total)
		if err := a.upsert(ctx, &domain.TechFact{
			ChatID:     chatID,
			UserID:     userID,
			Category:   "language",
			Key:        "Russian",
			Value:      fmt.Sprintf("%d%%", int(ratio*100)),
			Confidence: ratio,
		}); err != nil {
			a.logger.Debug("failed to upsert language tech fact", zap.Error(err))
		}
	}

	if latin > 0 {
		ratio := float64(latin) / float64(total)
		if err := a.upsert(ctx, &domain.TechFact{
			ChatID:     chatID,
			UserID:     userID,
			Category:   "language",
			Key:        "English",
			Value:      fmt.Sprintf("%d%%", int(ratio*100)),
			Confidence: ratio,
		}); err != nil {
			a.logger.Debug("failed to upsert language tech fact", zap.Error(err))
		}
	}
}

func (a *TechFactAnalyzer) detectMessageLength(ctx context.Context, chatID, userID, text string) {
	var style string
	n := len([]rune(text))
	switch {
	case n < 50:
		style = "short"
	case n < 200:
		style = "medium"
	default:
		style = "long"
	}

	if err := a.upsert(ctx, &domain.TechFact{
		ChatID:     chatID,
		UserID:     userID,
		Category:   "style",
		Key:        "message_length",
		Value:      style,
		Confidence: 1.0,
	}); err != nil {
		a.logger.Debug("failed to upsert message length tech fact", zap.Error(err))
	}
}

// Each derived write obeys the active turn's deletion lifetime as well as a
// fresh durable tombstone. SQLite provides the final transaction fence.
func (a *TechFactAnalyzer) upsert(ctx context.Context, fact *domain.TechFact) error {
	if err := conversationContextError(ctx); err != nil {
		return err
	}
	if store, ok := a.store.(conversationAvailability); ok {
		check, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		unavailable, err := store.ConversationUnavailable(check, fact.ChatID)
		if err != nil {
			return err
		}
		if unavailable {
			return domain.ErrConversationDeleted
		}
	}
	return a.store.UpsertTechFact(ctx, fact)
}
