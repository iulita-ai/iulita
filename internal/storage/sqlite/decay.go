package sqlite

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

const defaultHalfLifeDays = 30.0

// temporalDecayFactor returns a multiplier in [0, 1] based on age and half-life.
// score *= exp(-ln2 / halfLifeDays * ageDays)
func temporalDecayFactor(createdAt time.Time, halfLifeDays float64) float64 {
	if halfLifeDays <= 0 {
		halfLifeDays = defaultHalfLifeDays
	}
	ageDays := time.Since(createdAt).Hours() / 24.0
	if ageDays < 0 {
		ageDays = 0
	}
	return math.Exp(-math.Ln2 / halfLifeDays * ageDays)
}

// insightDecayScores returns insights sorted by decay score along with their normalized scores.
func insightDecayScores(insights []domain.Insight, halfLifeDays float64) ([]domain.Insight, []float64) {
	type scored struct {
		insight domain.Insight
		score   float64
	}
	items := make([]scored, len(insights))
	for i, ins := range insights {
		decay := temporalDecayFactor(ins.CreatedAt, halfLifeDays)
		accessBoost := 1.0 + math.Log1p(float64(ins.AccessCount))
		items[i] = scored{
			insight: ins,
			score:   float64(ins.Quality) * decay * accessBoost,
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].score > items[j].score
	})
	sorted := make([]domain.Insight, len(items))
	scores := make([]float64, len(items))
	maxScore := 1.0
	if len(items) > 0 && items[0].score > 0 {
		maxScore = items[0].score
	}
	for i, it := range items {
		sorted[i] = it.insight
		scores[i] = it.score / maxScore // normalize to [0,1]
	}
	return sorted, scores
}

// rankInsightsByDecay scores insights using quality * decay * (1 + log(1+accessCount))
// and returns the top `limit` results.
func rankInsightsByDecay(insights []domain.Insight, halfLifeDays float64, limit int) []domain.Insight {
	sorted, _ := insightDecayScores(insights, halfLifeDays)
	if limit > len(sorted) {
		limit = len(sorted)
	}
	return sorted[:limit]
}

// factDecayScores returns facts sorted by decay score along with their normalized scores.
func factDecayScores(facts []domain.Fact, halfLifeDays float64) ([]domain.Fact, []float64) {
	type scored struct {
		fact  domain.Fact
		score float64
	}
	items := make([]scored, len(facts))
	for i, f := range facts {
		decay := temporalDecayFactor(f.LastAccessedAt, halfLifeDays)
		accessBoost := 1.0 + math.Log1p(float64(f.AccessCount))
		items[i] = scored{
			fact:  f,
			score: decay * accessBoost,
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].score > items[j].score
	})
	sorted := make([]domain.Fact, len(items))
	scores := make([]float64, len(items))
	maxScore := 1.0
	if len(items) > 0 && items[0].score > 0 {
		maxScore = items[0].score
	}
	for i, it := range items {
		sorted[i] = it.fact
		scores[i] = it.score / maxScore // normalize to [0,1]
	}
	return sorted, scores
}

// rankFactsByDecay scores facts using decay * (1 + log(1+accessCount))
// and returns the top `limit` results.
func rankFactsByDecay(facts []domain.Fact, halfLifeDays float64, limit int) []domain.Fact {
	sorted, _ := factDecayScores(facts, halfLifeDays)
	if limit > len(sorted) {
		limit = len(sorted)
	}
	return sorted[:limit]
}

// tokenize splits text into lowercase word tokens for Jaccard similarity.
func tokenize(text string) map[string]struct{} {
	words := strings.Fields(strings.ToLower(text))
	tokens := make(map[string]struct{}, len(words))
	for _, w := range words {
		tokens[w] = struct{}{}
	}
	return tokens
}

// jaccardSimilarity computes Jaccard similarity between two token sets.
func jaccardSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for tok := range a {
		if _, ok := b[tok]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// applyMMRFacts reranks facts using Maximal Marginal Relevance to reduce near-duplicates.
// lambda controls relevance vs diversity tradeoff (1.0 = pure relevance, 0.0 = pure diversity).
func applyMMRFacts(facts []domain.Fact, scores []float64, lambda float64, limit int) []domain.Fact {
	if len(facts) == 0 || lambda <= 0 {
		return facts
	}
	if limit > len(facts) {
		limit = len(facts)
	}

	tokens := make([]map[string]struct{}, len(facts))
	for i, f := range facts {
		tokens[i] = tokenize(f.Content)
	}

	selected := make([]int, 0, limit)
	used := make([]bool, len(facts))

	// Greedily pick items maximizing MMR score.
	for len(selected) < limit {
		bestIdx := -1
		bestMMR := math.Inf(-1)

		for i := range facts {
			if used[i] {
				continue
			}
			// Max similarity to any already-selected item.
			maxSim := 0.0
			for _, si := range selected {
				sim := jaccardSimilarity(tokens[i], tokens[si])
				if sim > maxSim {
					maxSim = sim
				}
			}
			mmr := lambda*scores[i] - (1-lambda)*maxSim
			if mmr > bestMMR {
				bestMMR = mmr
				bestIdx = i
			}
		}

		if bestIdx < 0 {
			break
		}
		selected = append(selected, bestIdx)
		used[bestIdx] = true
	}

	result := make([]domain.Fact, len(selected))
	for i, idx := range selected {
		result[i] = facts[idx]
	}
	return result
}

// applyMMRInsights reranks insights using Maximal Marginal Relevance.
func applyMMRInsights(insights []domain.Insight, scores []float64, lambda float64, limit int) []domain.Insight {
	if len(insights) == 0 || lambda <= 0 {
		return insights
	}
	if limit > len(insights) {
		limit = len(insights)
	}

	tokens := make([]map[string]struct{}, len(insights))
	for i, ins := range insights {
		tokens[i] = tokenize(ins.Content)
	}

	selected := make([]int, 0, limit)
	used := make([]bool, len(insights))

	for len(selected) < limit {
		bestIdx := -1
		bestMMR := math.Inf(-1)

		for i := range insights {
			if used[i] {
				continue
			}
			maxSim := 0.0
			for _, si := range selected {
				sim := jaccardSimilarity(tokens[i], tokens[si])
				if sim > maxSim {
					maxSim = sim
				}
			}
			mmr := lambda*scores[i] - (1-lambda)*maxSim
			if mmr > bestMMR {
				bestMMR = mmr
				bestIdx = i
			}
		}

		if bestIdx < 0 {
			break
		}
		selected = append(selected, bestIdx)
		used[bestIdx] = true
	}

	result := make([]domain.Insight, len(selected))
	for i, idx := range selected {
		result[i] = insights[idx]
	}
	return result
}
