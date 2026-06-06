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
	"github.com/iulita-ai/iulita/internal/skillmgr"
	"github.com/iulita-ai/iulita/internal/storage"
)

// lessonTTL is how long a workflow lesson stays retrievable before expiring.
const lessonTTL = 90 * 24 * time.Hour

// maxReviewMessages bounds how much transcript the reviewer reads.
const maxReviewMessages = 40

// reviewNoLesson is the sentinel the reviewer LLM returns when there's nothing
// worth recording.
const reviewNoLesson = "NONE"

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
func (h *SkillReviewHandler) Type() string { return domain.TaskTypeSkillReview }

// Handle reviews one completed turn's transcript and persists a reusable lesson.
func (h *SkillReviewHandler) Handle(ctx context.Context, payload string) (string, error) {
	if !h.cfg.Enabled {
		return `{"reviewed":0,"reason":"disabled"}`, nil
	}

	var p domain.SkillReviewPayload
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
	// Intermediate tool calls aren't persisted as chat messages, so fold the
	// gate's tool summary into the transcript — it's what made the turn "hard".
	if p.ToolSummary != "" {
		transcript += "\nTools used this turn:\n" + p.ToolSummary
	}

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

	proposed := 0
	if h.cfg.ProposeSkills {
		if h.maybeProposeSkill(ctx, p, transcript, lesson) {
			proposed = 1
		}
	}

	return fmt.Sprintf(`{"reviewed":1,"lesson_saved":1,"proposed":%d}`, proposed), nil
}

// proposedSkill is the JSON contract the reviewer LLM returns for a draft.
type proposedSkill struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Triggers    []string `json:"triggers"`
	Body        string   `json:"body"`
}

// maybeProposeSkill asks the LLM whether the turn generalizes into a reusable
// text-only skill, scans the draft, and persists it as an INERT proposal (never
// registered or injected). Returns true if a proposal row was written.
func (h *SkillReviewHandler) maybeProposeSkill(ctx context.Context, p domain.SkillReviewPayload, transcript, lesson string) bool {
	resp, err := h.provider.Complete(ctx, llm.Request{
		SystemPrompt: "You decide whether a conversation reveals a reusable, well-scoped PROCEDURE worth saving as a " +
			"lightweight text-only skill (instructions only, no code). Only propose one if it is genuinely reusable and " +
			"specific. Respond with EXACTLY " + reviewNoLesson + " if not. Otherwise respond with ONLY a JSON object: " +
			`{"slug":"kebab-case-id","name":"Short Name","description":"one line","triggers":["specific","keywords"],"body":"concise instructions"}. ` +
			"Triggers must be specific keywords (not generic words like 'help' or 'do'); at most 4. Body under 1500 characters.",
		Message:   "Lesson: " + lesson + "\n\nTranscript:\n" + transcript,
		RouteHint: llm.RouteHintCheap,
	})
	if err != nil {
		h.logger.Warn("skill proposal LLM call failed", zap.Error(err))
		return false
	}

	raw := strings.TrimSpace(resp.Content)
	if raw == "" || strings.EqualFold(raw, reviewNoLesson) {
		return false
	}
	raw = stripJSONFence(raw)

	var draft proposedSkill
	if err := json.Unmarshal([]byte(raw), &draft); err != nil {
		h.logger.Warn("skill proposal parse failed", zap.Error(err))
		return false
	}

	warnings, blocked := skillmgr.ScanAuthoredSkill(draft.Slug, draft.Name, draft.Body, draft.Triggers)
	if warnings == nil {
		warnings = []string{} // marshal to "[]", never "null" (it's a documented JSON array)
	}
	status := domain.SkillProposalPending
	if blocked {
		status = domain.SkillProposalRejected
	}
	warnJSON, mErr := json.Marshal(warnings)
	if mErr != nil {
		warnJSON = []byte("[]")
	}

	proposal := &domain.SkillProposal{
		ChatID:          p.ChatID,
		UserID:          p.UserID,
		Slug:            draft.Slug,
		Name:            draft.Name,
		Description:     draft.Description,
		Body:            strings.TrimSpace(draft.Body),
		Triggers:        strings.Join(draft.Triggers, ","),
		Warnings:        string(warnJSON),
		Status:          status,
		SourceMessageID: p.LastMessageID,
		CreatedAt:       time.Now(),
	}
	if err := h.store.SaveSkillProposal(ctx, proposal); err != nil {
		h.logger.Warn("saving skill proposal failed", zap.Error(err))
		return false
	}

	h.logger.Info("recorded skill proposal",
		zap.String("slug", draft.Slug),
		zap.String("status", status),
		zap.Int("warnings", len(warnings)))
	return true
}

// stripJSONFence removes a leading/trailing ```json ... ``` fence if present.
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
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
