package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/memory"
	"github.com/iulita-ai/iulita/internal/storage"
)

const TaskTypeInsightGenerate = "insight.generate"

type insightPayload struct {
	ChatID string `json:"chat_id"`
	UserID string `json:"user_id,omitempty"`
}

// InsightGenerateHandler generates insights for a single chat.
type InsightGenerateHandler struct {
	store    storage.Repository
	provider llm.Provider
	cfg      config.InsightsConfig
	sender   channel.MessageSender // optional, for delivery notifications
	logger   *zap.Logger
}

func NewInsightGenerateHandler(store storage.Repository, provider llm.Provider, cfg config.InsightsConfig, logger *zap.Logger) *InsightGenerateHandler {
	return &InsightGenerateHandler{store: store, provider: provider, cfg: cfg, logger: logger}
}

// SetSender configures a MessageSender for delivery notifications.
func (h *InsightGenerateHandler) SetSender(s channel.MessageSender) {
	h.sender = s
}

func (h *InsightGenerateHandler) Type() string { return TaskTypeInsightGenerate }

func (h *InsightGenerateHandler) Handle(ctx context.Context, payload string) (string, error) {
	var p insightPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("invalid payload: %w", err)
	}

	// Load facts — prefer user-scoped, fall back to chat-scoped.
	var facts []domain.Fact
	var err error
	if p.UserID != "" {
		facts, err = h.store.GetAllFactsByUser(ctx, p.UserID)
		if err != nil {
			return "", fmt.Errorf("loading user facts: %w", err)
		}
	}
	if len(facts) == 0 {
		facts, err = h.store.GetAllFacts(ctx, p.ChatID)
		if err != nil {
			return "", fmt.Errorf("loading facts: %w", err)
		}
	}

	minFacts := h.cfg.MinFacts
	if minFacts <= 0 {
		minFacts = 20
	}
	if len(facts) < minFacts {
		return `{"generated":0,"reason":"not enough facts"}`, nil
	}

	generated, err := h.generateForChat(ctx, p.ChatID, p.UserID, facts)
	if err != nil {
		return "", err
	}

	if generated > 0 && h.sender != nil {
		summary := fmt.Sprintf("Generated %d new insight(s) from your memory.", generated)
		if err := h.sender.SendMessage(ctx, p.ChatID, summary); err != nil {
			h.logger.Error("failed to deliver insight summary", zap.Error(err))
		}
	}

	result, _ := json.Marshal(map[string]int{"generated": generated})
	return string(result), nil
}

func (h *InsightGenerateHandler) maxPairs() int {
	if h.cfg.MaxPairs > 0 {
		return h.cfg.MaxPairs
	}
	return 6
}

func (h *InsightGenerateHandler) ttl() time.Duration {
	if h.cfg.TTL != "" {
		if d, err := time.ParseDuration(h.cfg.TTL); err == nil {
			return d
		}
	}
	return 720 * time.Hour
}

func (h *InsightGenerateHandler) generateForChat(ctx context.Context, chatID, userID string, facts []domain.Fact) (int, error) {
	texts := make([]string, len(facts))
	for i, f := range facts {
		texts[i] = f.Content
	}
	vectors := memory.BuildTFIDFVectors(texts)

	k := int(math.Sqrt(float64(len(facts)) / 3.0))
	if k < 2 {
		k = 2
	}

	clusters := memory.Kmeans(vectors, k, 20)
	if len(clusters) < 2 {
		return 0, nil
	}

	pairs := sampleCrossPairs(clusters, h.maxPairs())
	if len(pairs) == 0 {
		return 0, nil
	}

	coveredPairs := make(map[string]struct{})
	var existingInsights []domain.Insight
	if userID != "" {
		existingInsights, _ = h.store.GetRecentInsightsByUser(ctx, userID, 1000)
	}
	if len(existingInsights) == 0 {
		existingInsights, _ = h.store.GetRecentInsights(ctx, chatID, 1000)
	}
	for _, ins := range existingInsights {
		ids := strings.Split(ins.FactIDs, ",")
		coveredPairs[factPairKey(ids)] = struct{}{}
	}

	generated := 0
	for _, pair := range pairs {
		var pairIDs []string
		for _, idx := range pair.a {
			pairIDs = append(pairIDs, fmt.Sprintf("%d", facts[idx].ID))
		}
		for _, idx := range pair.b {
			pairIDs = append(pairIDs, fmt.Sprintf("%d", facts[idx].ID))
		}
		if _, covered := coveredPairs[factPairKey(pairIDs)]; covered {
			continue
		}

		if err := h.generateForPair(ctx, chatID, userID, facts, pair); err != nil {
			h.logger.Error("insight pair generation failed",
				zap.String("chat_id", chatID), zap.Error(err))
			continue
		}
		generated++
	}

	return generated, nil
}

func (h *InsightGenerateHandler) generateForPair(ctx context.Context, chatID, userID string, facts []domain.Fact, pair clusterPair) error {
	var prompt strings.Builder
	prompt.WriteString("You are a creative thinking assistant. Below are two clusters of facts from a user's memory. ")
	prompt.WriteString("Find a structural analogy, unexpected connection, or novel insight between them. ")
	prompt.WriteString("Respond with exactly one insight, 1-2 sentences. No preamble.\n\n")

	// Load user profile context — prefer user-scoped.
	var tfs []domain.TechFact
	if userID != "" {
		tfs, _ = h.store.GetTechFactsByUser(ctx, userID)
	}
	if len(tfs) == 0 {
		tfs, _ = h.store.GetTechFacts(ctx, chatID)
	}
	if len(tfs) > 0 {
		prompt.WriteString("User profile context:\n")
		for _, tf := range tfs {
			fmt.Fprintf(&prompt, "- %s/%s: %s\n", tf.Category, tf.Key, tf.Value)
		}
		prompt.WriteString("\n")
	}

	var factIDs []string
	prompt.WriteString("Cluster A:\n")
	for _, idx := range pair.a {
		fmt.Fprintf(&prompt, "- %s\n", facts[idx].Content)
		factIDs = append(factIDs, fmt.Sprintf("%d", facts[idx].ID))
	}
	prompt.WriteString("Cluster B:\n")
	for _, idx := range pair.b {
		fmt.Fprintf(&prompt, "- %s\n", facts[idx].Content)
		factIDs = append(factIDs, fmt.Sprintf("%d", facts[idx].ID))
	}

	resp, err := h.provider.Complete(ctx, llm.Request{
		SystemPrompt: "Generate a creative insight from the fact clusters below. Be concise and insightful.",
		Message:      prompt.String(),
		RouteHint:    llm.RouteHintCheap,
	})
	if err != nil {
		return fmt.Errorf("LLM insight generation: %w", err)
	}

	if resp.Content == "" {
		return nil
	}

	now := time.Now()
	expiresAt := now.Add(h.ttl())

	quality := h.scoreInsight(ctx, resp.Content)
	threshold := h.cfg.QualityThreshold
	if threshold > 0 && quality > 0 && quality < threshold {
		return nil
	}

	insight := &domain.Insight{
		ChatID:    chatID,
		UserID:    userID,
		Content:   resp.Content,
		FactIDs:   strings.Join(factIDs, ","),
		Quality:   quality,
		CreatedAt: now,
		ExpiresAt: &expiresAt,
	}

	return h.store.SaveInsight(ctx, insight)
}

func (h *InsightGenerateHandler) scoreInsight(ctx context.Context, content string) int {
	resp, err := h.provider.Complete(ctx, llm.Request{
		SystemPrompt: "Rate the following insight on a scale of 1-5 for novelty and usefulness. " +
			"Respond with ONLY a single digit (1-5), nothing else.",
		Message:   content,
		RouteHint: llm.RouteHintCheap,
	})
	if err != nil {
		return 0
	}
	score, err := strconv.Atoi(strings.TrimSpace(resp.Content))
	if err != nil || score < 1 || score > 5 {
		return 0
	}
	return score
}

// --- shared helpers (moved from memory package) ---

type clusterPair struct {
	a, b []int
}

func factPairKey(ids []string) string {
	sorted := make([]string, len(ids))
	copy(sorted, ids)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func sampleCrossPairs(clusters []memory.Cluster, maxPairs int) []clusterPair {
	if len(clusters) < 2 {
		return nil
	}

	type idxPair struct{ i, j int }
	var allPairs []idxPair
	for i := 0; i < len(clusters); i++ {
		for j := i + 1; j < len(clusters); j++ {
			if len(clusters[i].Members) > 0 && len(clusters[j].Members) > 0 {
				allPairs = append(allPairs, idxPair{i, j})
			}
		}
	}

	rand.Shuffle(len(allPairs), func(i, j int) {
		allPairs[i], allPairs[j] = allPairs[j], allPairs[i]
	})

	if len(allPairs) > maxPairs {
		allPairs = allPairs[:maxPairs]
	}

	result := make([]clusterPair, len(allPairs))
	for i, p := range allPairs {
		a := clusters[p.i].Members
		b := clusters[p.j].Members
		if len(a) > 3 {
			a = a[:3]
		}
		if len(b) > 3 {
			b = b[:3]
		}
		result[i] = clusterPair{a: a, b: b}
	}
	return result
}
