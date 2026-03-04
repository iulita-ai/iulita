package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// --- Directives (user-scoped) ---

func (s *Store) GetDirectiveByUser(ctx context.Context, userID string) (*domain.Directive, error) {
	if userID == "" {
		return nil, nil
	}
	d := new(domain.Directive)
	err := s.db.NewSelect().
		Model(d).
		Where("user_id = ?", userID).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting directive by user: %w", err)
	}
	return d, nil
}

// --- Facts (user-scoped) ---

func (s *Store) SearchFactsByUser(ctx context.Context, userID, query string, limit int) ([]domain.Fact, error) {
	if userID == "" {
		return nil, nil
	}
	var facts []domain.Fact
	fetchLimit := limit * 3
	if fetchLimit < 20 {
		fetchLimit = 20
	}
	err := s.db.NewSelect().
		Model(&facts).
		Where("user_id = ?", userID).
		Where("id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)", query).
		OrderExpr("access_count DESC, last_accessed_at DESC").
		Limit(fetchLimit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("searching facts by user: %w", err)
	}
	if s.halfLifeDays > 0 && len(facts) > 0 {
		if s.mmrLambda > 0 {
			sorted, scores := factDecayScores(facts, s.halfLifeDays)
			return applyMMRFacts(sorted, scores, s.mmrLambda, limit), nil
		}
		return rankFactsByDecay(facts, s.halfLifeDays, limit), nil
	}
	if len(facts) > limit {
		facts = facts[:limit]
	}
	return facts, nil
}

func (s *Store) GetRecentFactsByUser(ctx context.Context, userID string, limit int) ([]domain.Fact, error) {
	if userID == "" {
		return nil, nil
	}
	var facts []domain.Fact
	fetchLimit := limit * 3
	if fetchLimit < 30 {
		fetchLimit = 30
	}
	err := s.db.NewSelect().
		Model(&facts).
		Where("user_id = ?", userID).
		Order("last_accessed_at DESC").
		Limit(fetchLimit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting recent facts by user: %w", err)
	}
	if s.halfLifeDays > 0 && len(facts) > 0 {
		if s.mmrLambda > 0 {
			sorted, scores := factDecayScores(facts, s.halfLifeDays)
			return applyMMRFacts(sorted, scores, s.mmrLambda, limit), nil
		}
		return rankFactsByDecay(facts, s.halfLifeDays, limit), nil
	}
	if len(facts) > limit {
		facts = facts[:limit]
	}
	return facts, nil
}

func (s *Store) GetAllFactsByUser(ctx context.Context, userID string) ([]domain.Fact, error) {
	var facts []domain.Fact
	q := s.db.NewSelect().
		Model(&facts).
		Order("created_at ASC")
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	err := q.Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting all facts by user: %w", err)
	}
	return facts, nil
}

// --- Insights (user-scoped) ---

func (s *Store) GetRecentInsightsByUser(ctx context.Context, userID string, limit int) ([]domain.Insight, error) {
	if userID == "" {
		return nil, nil
	}
	var insights []domain.Insight
	fetchLimit := limit * 3
	if fetchLimit < 20 {
		fetchLimit = 20
	}
	err := s.db.NewSelect().
		Model(&insights).
		Where("user_id = ?", userID).
		Where("expires_at IS NULL OR expires_at > ?", time.Now()).
		OrderExpr("quality DESC, access_count DESC, created_at DESC").
		Limit(fetchLimit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting recent insights by user: %w", err)
	}
	if s.halfLifeDays > 0 && len(insights) > 0 {
		if s.mmrLambda > 0 {
			sorted, scores := insightDecayScores(insights, s.halfLifeDays)
			return applyMMRInsights(sorted, scores, s.mmrLambda, limit), nil
		}
		return rankInsightsByDecay(insights, s.halfLifeDays, limit), nil
	}
	if len(insights) > limit {
		insights = insights[:limit]
	}
	return insights, nil
}

func (s *Store) SearchInsightsByUser(ctx context.Context, userID, query string, limit int) ([]domain.Insight, error) {
	if userID == "" {
		return nil, nil
	}
	var insights []domain.Insight
	fetchLimit := limit * 3
	if fetchLimit < 20 {
		fetchLimit = 20
	}
	err := s.db.NewSelect().
		Model(&insights).
		Where("user_id = ?", userID).
		Where("id IN (SELECT rowid FROM insights_fts WHERE insights_fts MATCH ?)", query).
		Where("expires_at IS NULL OR expires_at > ?", time.Now()).
		OrderExpr("quality DESC, access_count DESC, created_at DESC").
		Limit(fetchLimit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("searching insights by user: %w", err)
	}
	if s.halfLifeDays > 0 && len(insights) > 0 {
		if s.mmrLambda > 0 {
			sorted, scores := insightDecayScores(insights, s.halfLifeDays)
			return applyMMRInsights(sorted, scores, s.mmrLambda, limit), nil
		}
		return rankInsightsByDecay(insights, s.halfLifeDays, limit), nil
	}
	if len(insights) > limit {
		insights = insights[:limit]
	}
	return insights, nil
}

func (s *Store) CountInsightsByUser(ctx context.Context, userID string) (int, error) {
	q := s.db.NewSelect().
		Model((*domain.Insight)(nil)).
		Where("expires_at IS NULL OR expires_at > ?", time.Now())
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	count, err := q.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting insights by user: %w", err)
	}
	return count, nil
}

// --- Tech Facts (user-scoped) ---

func (s *Store) GetTechFactsByUser(ctx context.Context, userID string) ([]domain.TechFact, error) {
	var facts []domain.TechFact
	q := s.db.NewSelect().Model(&facts)
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	err := q.Order("category ASC", "key ASC").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting tech facts by user: %w", err)
	}
	return facts, nil
}

func (s *Store) GetTechFactsByCategoryAndUser(ctx context.Context, userID, category string) ([]domain.TechFact, error) {
	if userID == "" {
		return nil, nil
	}
	var facts []domain.TechFact
	err := s.db.NewSelect().
		Model(&facts).
		Where("user_id = ?", userID).
		Where("category = ?", category).
		Order("key ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting tech facts by category and user: %w", err)
	}
	return facts, nil
}

func (s *Store) CountTechFactsByUser(ctx context.Context, userID string) (int, error) {
	q := s.db.NewSelect().Model((*domain.TechFact)(nil))
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	count, err := q.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting tech facts by user: %w", err)
	}
	return count, nil
}

// --- Hybrid vector search (user-scoped) ---

func (s *Store) SearchFactsHybridByUser(ctx context.Context, userID string, query string, queryVec []float32, limit int, vectorWeight float64) ([]domain.Fact, error) {
	if userID == "" {
		return nil, nil
	}
	ftsResults, err := s.SearchFactsByUser(ctx, userID, query, limit*2)
	if err != nil {
		return nil, err
	}

	if queryVec == nil || vectorWeight <= 0 {
		if len(ftsResults) > limit {
			return ftsResults[:limit], nil
		}
		return ftsResults, nil
	}

	var rows []struct {
		FactID    int64  `bun:"fact_id"`
		Embedding []byte `bun:"embedding"`
	}
	err = s.db.NewSelect().
		TableExpr("fact_vectors fv").
		Join("JOIN facts f ON f.id = fv.fact_id").
		Where("f.user_id = ?", userID).
		ColumnExpr("fv.fact_id, fv.embedding").
		Scan(ctx, &rows)
	if err != nil {
		if len(ftsResults) > limit {
			return ftsResults[:limit], nil
		}
		return ftsResults, nil
	}

	vecScores := make(map[int64]float64)
	for _, row := range rows {
		emb, err := decodeVector(row.Embedding)
		if err != nil {
			continue
		}
		vecScores[row.FactID] = cosineSimilarity(queryVec, emb)
	}

	ftsScores := make(map[int64]float64)
	for i, f := range ftsResults {
		ftsScores[f.ID] = 1.0 - float64(i)/float64(len(ftsResults)+1)
	}

	allIDs := make(map[int64]struct{})
	for id := range ftsScores {
		allIDs[id] = struct{}{}
	}
	for id := range vecScores {
		allIDs[id] = struct{}{}
	}

	type scored struct {
		id    int64
		score float64
	}
	var candidates []scored
	for id := range allIDs {
		fts := ftsScores[id]
		vec := vecScores[id]
		combined := (1-vectorWeight)*fts + vectorWeight*vec
		candidates = append(candidates, scored{id, combined})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
	}

	var facts []domain.Fact
	err = s.db.NewSelect().
		Model(&facts).
		Where("id IN (?)", ids).
		Scan(ctx)
	if err != nil {
		return ftsResults, nil
	}

	idOrder := make(map[int64]int)
	for i, c := range candidates {
		idOrder[c.id] = i
	}
	sort.Slice(facts, func(i, j int) bool {
		return idOrder[facts[i].ID] < idOrder[facts[j].ID]
	})

	return facts, nil
}

func (s *Store) SearchInsightsHybridByUser(ctx context.Context, userID string, query string, queryVec []float32, limit int, vectorWeight float64) ([]domain.Insight, error) {
	if userID == "" {
		return nil, nil
	}
	ftsResults, err := s.SearchInsightsByUser(ctx, userID, query, limit*2)
	if err != nil {
		return nil, err
	}

	if queryVec == nil || vectorWeight <= 0 {
		if len(ftsResults) > limit {
			return ftsResults[:limit], nil
		}
		return ftsResults, nil
	}

	var rows []struct {
		InsightID int64  `bun:"insight_id"`
		Embedding []byte `bun:"embedding"`
	}
	err = s.db.NewSelect().
		TableExpr("insight_vectors iv").
		Join("JOIN insights i ON i.id = iv.insight_id").
		Where("i.user_id = ?", userID).
		ColumnExpr("iv.insight_id, iv.embedding").
		Scan(ctx, &rows)
	if err != nil {
		if len(ftsResults) > limit {
			return ftsResults[:limit], nil
		}
		return ftsResults, nil
	}

	vecScores := make(map[int64]float64)
	for _, row := range rows {
		emb, err := decodeVector(row.Embedding)
		if err != nil {
			continue
		}
		vecScores[row.InsightID] = cosineSimilarity(queryVec, emb)
	}

	ftsScores := make(map[int64]float64)
	for i, ins := range ftsResults {
		ftsScores[ins.ID] = 1.0 - float64(i)/float64(len(ftsResults)+1)
	}

	allIDs := make(map[int64]struct{})
	for id := range ftsScores {
		allIDs[id] = struct{}{}
	}
	for id := range vecScores {
		allIDs[id] = struct{}{}
	}

	type scored struct {
		id    int64
		score float64
	}
	var candidates []scored
	for id := range allIDs {
		fts := ftsScores[id]
		vec := vecScores[id]
		combined := (1-vectorWeight)*fts + vectorWeight*vec
		candidates = append(candidates, scored{id, combined})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
	}

	var insights []domain.Insight
	err = s.db.NewSelect().
		Model(&insights).
		Where("id IN (?)", ids).
		Scan(ctx)
	if err != nil {
		return ftsResults, nil
	}

	idOrder := make(map[int64]int)
	for i, c := range candidates {
		idOrder[c.id] = i
	}
	sort.Slice(insights, func(i, j int) bool {
		return idOrder[insights[i].ID] < idOrder[insights[j].ID]
	})

	return insights, nil
}

// BackfillUserIDs sets user_id on facts, insights, tech_facts, and chat_messages
// that have an empty user_id, based on user_channels bindings.
// Matches on channel_id OR channel_user_id (Telegram stores chat_id as user_id in DMs).
func (s *Store) BackfillUserIDs(ctx context.Context) (int64, error) {
	// Build chat_id → user_id mapping from all channel bindings.
	var channels []struct {
		UserID        string `bun:"user_id"`
		ChannelID     string `bun:"channel_id"`
		ChannelUserID string `bun:"channel_user_id"`
	}
	err := s.db.NewSelect().TableExpr("user_channels").
		Column("user_id", "channel_id", "channel_user_id").
		Scan(ctx, &channels)
	if err != nil {
		return 0, fmt.Errorf("loading channel bindings: %w", err)
	}

	// Map chat_id values to user_id.
	chatToUser := make(map[string]string)
	for _, ch := range channels {
		if ch.ChannelID != "" {
			chatToUser[ch.ChannelID] = ch.UserID
		}
		if ch.ChannelUserID != "" {
			chatToUser[ch.ChannelUserID] = ch.UserID
		}
	}
	if len(chatToUser) == 0 {
		return 0, nil
	}

	// Tables with FTS triggers that conflict with UPDATE on non-content columns.
	ftsTriggered := map[string]string{
		"facts":    "facts_au",
		"insights": "insights_au",
	}
	tables := []string{"facts", "insights", "tech_facts", "chat_messages"}
	var total int64
	for _, table := range tables {
		// Temporarily drop FTS AFTER UPDATE trigger — it fires even for
		// non-content column changes and can cause SQL logic errors.
		triggerName := ftsTriggered[table]
		var triggerSQL string
		if triggerName != "" {
			row := s.db.QueryRowContext(ctx,
				`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, triggerName)
			if err := row.Scan(&triggerSQL); err == nil && triggerSQL != "" {
				s.db.ExecContext(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s`, triggerName))
			}
		}

		for chatID, userID := range chatToUser {
			res, err := s.db.ExecContext(ctx,
				fmt.Sprintf(`UPDATE %s SET user_id = ? WHERE chat_id = ? AND user_id = ''`, table),
				userID, chatID)
			if err != nil {
				// Recreate trigger before returning.
				if triggerSQL != "" {
					s.db.ExecContext(ctx, triggerSQL)
				}
				return total, fmt.Errorf("backfilling %s (chat_id=%s): %w", table, chatID, err)
			}
			n, _ := res.RowsAffected()
			total += n
		}

		// Recreate the FTS trigger.
		if triggerSQL != "" {
			s.db.ExecContext(ctx, triggerSQL)
		}
	}
	return total, nil
}
