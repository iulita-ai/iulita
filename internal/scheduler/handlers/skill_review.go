package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/storage"
)

// TaskTypeSkillReview must match the literal used by the assistant's complexity
// gate (assistant.taskTypeSkillReview).
const TaskTypeSkillReview = "skill.review"

// lessonTTL is how long a workflow lesson stays retrievable before expiring.
const lessonTTL = 90 * 24 * time.Hour

// maxReviewMessages bounds how much transcript the reviewer reads.
const maxReviewMessages = 40

// reviewNoLesson is the sentinel the reviewer LLM returns when there's nothing
// worth recording.
const reviewNoLesson = "NONE"

type skillReviewPayload struct {
	ChatID        string `json:"chat_id"`
	UserID        string `json:"user_id,omitempty"`
	LastMessageID int64  `json:"last_message_id"`
}

// SkillReviewHandler reflects on a completed "hard" turn and records a reusable
// lesson as an insight. This is the first slice of the self-improvement loop:
// it does NOT author executable skills (that has a separate security surface).
type SkillReviewHandler struct {
	store    storage.Repository
	provider llm.Provider
	cfg      config.SelfImproveConfig
	logger   *zap.Logger
}

// NewSkillReviewHandler constructs the skill.review task handler.
func NewSkillReviewHandler(store storage.Repository, provider llm.Provider, cfg config.SelfImproveConfig, logger *zap.Logger) *SkillReviewHandler {
	return &SkillReviewHandler{store: store, provider: provider, cfg: cfg, logger: logger}
}

// Type returns the task type this handler serves.
func (h *SkillReviewHandler) Type() string { return TaskTypeSkillReview }

// Handle reviews one completed turn's transcript and persists a reusable lesson.
func (h *SkillReviewHandler) Handle(ctx context.Context, payload string) (string, error) {
	if !h.cfg.Enabled {
		return `{"reviewed":0,"reason":"disabled"}`, nil
	}

	var p skillReviewPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("invalid payload: %w", err)
	}

	// Load the turn (and some preceding context) up to and including the boundary.
	history, err := h.store.GetHistoryBefore(ctx, p.ChatID, p.LastMessageID+1, maxReviewMessages)
	if err != nil {
		return "", fmt.Errorf("loading history: %w", err)
	}
	if len(history) < 2 {
		return `{"reviewed":0,"reason":"insufficient history"}`, nil
	}

	transcript := renderTranscript(history)

	resp, err := h.provider.Complete(ctx, llm.Request{
		SystemPrompt: "You review an assistant's completed conversation that required many tool calls. " +
			"Extract ONE concise, reusable lesson (1-2 sentences) that would let the assistant handle " +
			"a similar request faster or more reliably next time — a heuristic, a better tool order, or a " +
			"pitfall to avoid. Be specific and actionable. If there is no generalizable lesson, reply with " +
			"exactly " + reviewNoLesson + " and nothing else.",
		Message:   transcript,
		RouteHint: llm.RouteHintCheap,
	})
	if err != nil {
		return "", fmt.Errorf("LLM review: %w", err)
	}

	lesson := strings.TrimSpace(resp.Content)
	if lesson == "" || strings.EqualFold(lesson, reviewNoLesson) {
		return `{"reviewed":1,"lesson_saved":0}`, nil
	}

	now := time.Now()
	expiresAt := now.Add(lessonTTL)
	insight := &domain.Insight{
		ChatID:    p.ChatID,
		UserID:    p.UserID,
		Content:   "Workflow lesson: " + lesson,
		FactIDs:   "",
		Quality:   3,
		CreatedAt: now,
		ExpiresAt: &expiresAt,
	}
	if err := h.store.SaveInsight(ctx, insight); err != nil {
		return "", fmt.Errorf("saving lesson: %w", err)
	}

	h.logger.Info("recorded workflow lesson",
		zap.String("chat_id", p.ChatID),
		zap.Int64("insight_id", insight.ID))

	return `{"reviewed":1,"lesson_saved":1}`, nil
}

// renderTranscript turns chat messages into a compact role-tagged transcript.
func renderTranscript(msgs []domain.ChatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", strings.ToUpper(string(m.Role)), content)
	}
	return b.String()
}
