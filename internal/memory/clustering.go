package memory

import (
	"math"
	"math/rand/v2"
	"strings"
)

// stopwords contains common English words to filter out during tokenization.
var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "been": {},
	"but": {}, "by": {}, "can": {}, "could": {}, "did": {}, "do": {}, "does": {}, "done": {},
	"for": {}, "from": {}, "get": {}, "got": {}, "had": {}, "has": {}, "have": {}, "he": {},
	"her": {}, "here": {}, "him": {}, "his": {}, "how": {}, "if": {}, "in": {}, "into": {},
	"is": {}, "it": {}, "its": {}, "just": {}, "know": {}, "let": {}, "like": {}, "make": {},
	"me": {}, "might": {}, "more": {}, "most": {}, "much": {}, "must": {}, "my": {}, "no": {},
	"not": {}, "now": {}, "of": {}, "on": {}, "one": {}, "only": {}, "or": {}, "other": {},
	"our": {}, "out": {}, "over": {}, "own": {}, "say": {}, "she": {}, "should": {}, "so": {},
	"some": {}, "such": {}, "than": {}, "that": {}, "the": {}, "their": {}, "them": {}, "then": {},
	"there": {}, "these": {}, "they": {}, "this": {}, "to": {}, "too": {}, "up": {}, "us": {},
	"very": {}, "was": {}, "we": {}, "were": {}, "what": {}, "when": {}, "which": {}, "who": {},
	"will": {}, "with": {}, "would": {}, "you": {}, "your": {},
}

// tfVector represents a term frequency (or TF-IDF) vector for a document.
type tfVector struct {
	terms map[string]float64
}

// tokenize splits text into lowercase words, removing punctuation and stopwords.
func tokenize(text string) []string {
	words := strings.Fields(strings.ToLower(text))
	result := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.Trim(w, ".,!?;:\"'()[]{}—–-")
		if len(w) > 1 {
			if _, stop := stopwords[w]; !stop {
				result = append(result, w)
			}
		}
	}
	return result
}

// addBigrams appends bigram tokens (e.g. "machine_learning") to the token list.
func addBigrams(tokens []string) []string {
	if len(tokens) < 2 {
		return tokens
	}
	result := make([]string, len(tokens), len(tokens)+len(tokens)-1)
	copy(result, tokens)
	for i := 0; i < len(tokens)-1; i++ {
		result = append(result, tokens[i]+"_"+tokens[i+1])
	}
	return result
}

// BuildTFIDFVectors computes TF-IDF vectors for a set of documents.
func BuildTFIDFVectors(texts []string) []tfVector {
	n := len(texts)
	if n == 0 {
		return nil
	}

	// Tokenize all documents (with bigrams).
	allTokens := make([][]string, n)
	for i, text := range texts {
		allTokens[i] = addBigrams(tokenize(text))
	}

	// Compute document frequency for each term.
	df := make(map[string]int)
	for _, tokens := range allTokens {
		seen := make(map[string]struct{})
		for _, t := range tokens {
			if _, ok := seen[t]; !ok {
				seen[t] = struct{}{}
				df[t]++
			}
		}
	}

	// Build TF-IDF vector per document.
	vectors := make([]tfVector, n)
	logN := math.Log(float64(n))
	for i, tokens := range allTokens {
		freqs := make(map[string]float64)
		for _, t := range tokens {
			freqs[t]++
		}
		total := float64(len(tokens))
		if total > 0 {
			for term, count := range freqs {
				tf := count / total
				idf := logN - math.Log(float64(1+df[term]))
				if idf < 0 {
					idf = 0
				}
				freqs[term] = tf * idf
			}
		}
		vectors[i] = tfVector{terms: freqs}
	}
	return vectors
}

// cosineDistance returns 1 - cosine_similarity between two TF vectors.
func cosineDistance(a, b tfVector) float64 {
	var dotProduct, normA, normB float64
	for term, va := range a.terms {
		if vb, ok := b.terms[term]; ok {
			dotProduct += va * vb
		}
		normA += va * va
	}
	for _, vb := range b.terms {
		normB += vb * vb
	}
	if normA == 0 || normB == 0 {
		return 1.0
	}
	similarity := dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
	return 1.0 - similarity
}

// Cluster represents a group of fact indices with a centroid.
type Cluster struct {
	centroid tfVector
	Members  []int
}

// Kmeans performs k-means clustering on TF vectors.
// Returns cluster assignments for each item.
func Kmeans(vectors []tfVector, k int, maxIter int) []Cluster {
	n := len(vectors)
	if k >= n {
		// Each item is its own cluster.
		clusters := make([]Cluster, n)
		for i := range n {
			clusters[i] = Cluster{centroid: vectors[i], Members: []int{i}}
		}
		return clusters
	}

	// k-means++ initialization.
	centroids := kmeansppInit(vectors, k)

	assignments := make([]int, n)
	for range maxIter {
		// Assign each vector to nearest centroid.
		changed := false
		for i, v := range vectors {
			bestCluster := 0
			bestDist := cosineDistance(v, centroids[0])
			for c := 1; c < k; c++ {
				d := cosineDistance(v, centroids[c])
				if d < bestDist {
					bestDist = d
					bestCluster = c
				}
			}
			if assignments[i] != bestCluster {
				assignments[i] = bestCluster
				changed = true
			}
		}

		if !changed {
			break
		}

		// Recompute centroids.
		centroids = computeCentroids(vectors, assignments, k)
	}

	// Build clusters.
	clusters := make([]Cluster, k)
	for i := range k {
		clusters[i] = Cluster{centroid: centroids[i]}
	}
	for i, c := range assignments {
		clusters[c].Members = append(clusters[c].Members, i)
	}

	// Remove empty clusters.
	result := make([]Cluster, 0, k)
	for _, c := range clusters {
		if len(c.Members) > 0 {
			result = append(result, c)
		}
	}
	return result
}

func kmeansppInit(vectors []tfVector, k int) []tfVector {
	n := len(vectors)
	centroids := make([]tfVector, 0, k)

	// Pick first centroid randomly.
	centroids = append(centroids, vectors[rand.IntN(n)])

	dists := make([]float64, n)
	for i := 1; i < k; i++ {
		// Compute distance to nearest centroid.
		total := 0.0
		for j, v := range vectors {
			minDist := math.MaxFloat64
			for _, c := range centroids {
				d := cosineDistance(v, c)
				if d < minDist {
					minDist = d
				}
			}
			dists[j] = minDist * minDist
			total += dists[j]
		}

		// Pick next centroid with probability proportional to distance².
		if total == 0 {
			centroids = append(centroids, vectors[rand.IntN(n)])
			continue
		}
		target := rand.Float64() * total
		cumulative := 0.0
		chosen := 0
		for j, d := range dists {
			cumulative += d
			if cumulative >= target {
				chosen = j
				break
			}
		}
		centroids = append(centroids, vectors[chosen])
	}
	return centroids
}

func computeCentroids(vectors []tfVector, assignments []int, k int) []tfVector {
	centroids := make([]tfVector, k)
	counts := make([]int, k)

	sums := make([]map[string]float64, k)
	for i := range k {
		sums[i] = make(map[string]float64)
	}

	for i, v := range vectors {
		c := assignments[i]
		counts[c]++
		for term, val := range v.terms {
			sums[c][term] += val
		}
	}

	for i := range k {
		terms := make(map[string]float64)
		if counts[i] > 0 {
			for term, val := range sums[i] {
				terms[term] = val / float64(counts[i])
			}
		}
		centroids[i] = tfVector{terms: terms}
	}
	return centroids
}
