# Memory and Insights

## Philosophy

Most AI assistants either forget everything between sessions or hallucinate "memories" from training data. Iulita takes a fundamentally different approach:

- **Explicit storage only** — facts are stored only when you ask ("remember that...")
- **Verified data** — every fact traces back to a specific user request
- **Cross-reference insights** — patterns are discovered by analyzing your actual facts
- **Temporal decay** — older memories naturally lose relevance unless you access them
- **Hybrid retrieval** — FTS5 full-text search combined with ONNX vector embeddings

## Facts

### Storing Facts (Remember)

When you say "remember that my dog's name is Max", the following happens:

1. **Trigger detection** — the assistant detects the memory keyword ("remember") and forces the `remember` tool
2. **Duplicate check** — searches existing facts using the first 3 words to detect near-duplicates
3. **SQLite INSERT** — the fact is saved with `user_id`, `content`, `source_type=user`, timestamps
4. **FTS5 index** — an `AFTER INSERT` trigger automatically adds the fact to the `facts_fts` full-text index
5. **Vector embedding** — a background goroutine generates an ONNX embedding (384-dim all-MiniLM-L6-v2) and stores it in `fact_vectors`

```
domain.Fact {
    ID             int64
    ChatID         string     // source channel ("123456789", "console", "web:uuid")
    UserID         string     // iulita UUID — shared across all channels
    Content        string     // the fact text
    SourceType     string     // "user" (explicit) or "system" (auto-extracted)
    CreatedAt      time.Time
    LastAccessedAt time.Time  // reset on every recall
    AccessCount    int        // incremented on every recall
}
```

### Recalling Facts (Recall)

When you ask "what is my dog's name?":

1. **User-scoped search** — first tries `SearchFactsByUser(userID, query, limit)` for cross-channel facts
2. **FTS5 matching** — `SELECT * FROM facts WHERE id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)`
3. **Oversampling** — fetches `limit * 3` candidates (minimum 20) for downstream reranking
4. **Temporal decay** — each candidate is scored: `decay = exp(-ln(2) / halfLife * ageDays) * (1 + log(1 + accessCount))`
5. **MMR reranking** — Maximal Marginal Relevance reduces near-duplicates in the result set
6. **Reinforcement** — each returned fact gets `access_count++` and `last_accessed_at = now`

The assistant also performs a **hybrid search** on every message (not just explicit recall):

```
1. Sanitize query (strip FTS operators, stop words, limit to 5 keywords)
2. Generate query vector via ONNX embedding
3. FTS results: position-based scoring (1 - i/(n+1))
4. Vector results: cosine similarity against all stored vectors
5. Combined: (1-vectorWeight)*ftsScore + vectorWeight*vecScore
6. Union of both sets, sorted by combined score
```

### Forgetting Facts

The `forget` tool deletes a fact by ID. The FTS trigger (`facts_ad`) automatically removes it from the full-text index. The `ON DELETE CASCADE` on `fact_vectors` removes the embedding.

## Temporal Decay

Facts and insights decay over time using exponential (radioactive) decay:

```
decay_factor = exp(-ln(2) / halfLifeDays * ageDays)
```

| Days since access | Half-life = 30 days | Half-life = 90 days |
|-------------------|---------------------|---------------------|
| 0 | 1.00 | 1.00 |
| 15 | 0.71 | 0.89 |
| 30 | 0.50 | 0.79 |
| 60 | 0.25 | 0.63 |
| 90 | 0.13 | 0.50 |

**Key design choices:**
- **Facts** decay from `last_accessed_at` — every recall resets the clock
- **Insights** decay from `created_at` — they have a fixed lifetime
- **Access boost**: `1 + log(1 + accessCount)` — a fact accessed 100 times gets a 4.6x boost
- **Default half-life**: 30 days (configurable via `skills.memory.half_life_days`)

## MMR Reranking

After temporal decay scoring, Maximal Marginal Relevance prevents near-duplicate results:

```
MMR(item) = lambda * relevance_score - (1 - lambda) * max_similarity_to_selected
```

- `lambda = 1.0` → pure relevance, no diversity
- `lambda = 0.7` → recommended: favor relevance but penalize near-duplicates
- `lambda = 0.0` → pure diversity

Similarity is measured via Jaccard similarity on word tokens (zero-dependency approximation that runs without the ONNX embedder).

**Configuration**: `skills.memory.mmr_lambda` (default 0, disabled). Set to 0.7 for best results.

## Insights

Insights are AI-generated cross-references between your facts, discovered by the background scheduler.

### Generation Pipeline

The insight generation job runs every 24 hours (configurable):

```
1. Load all facts for the user
2. Check minimum fact count (default 20)
3. Build TF-IDF vectors
   - Tokenize: lowercase, remove punctuation, filter stopwords
   - Generate bigrams (adjacent word pairs)
   - Compute TF-IDF scores
4. K-means++ clustering
   - k = sqrt(numFacts / 3)
   - Cosine distance metric
   - 20 max iterations
5. Sample cross-cluster pairs
   - Up to 6 pairs per run
   - Skip already-covered fact pairs
6. For each pair:
   a. Send to LLM: "Generate a creative insight from these two clusters"
   b. Score quality (1-5) via separate LLM call
   c. Store if quality >= threshold
```

### Insight Lifecycle

```
domain.Insight {
    ID             int64
    ChatID         string
    UserID         string
    Content        string     // the insight text
    FactIDs        string     // comma-separated source fact IDs
    Quality        int        // 1-5 LLM quality score
    AccessCount    int
    LastAccessedAt time.Time
    CreatedAt      time.Time
    ExpiresAt      *time.Time // default: created + 30 days
}
```

- **Created** by the background scheduler after clustering and LLM synthesis
- **Surfaced** in the assistant's system prompt when contextually relevant (hybrid search)
- **Reinforced** when accessed (access count and last accessed time updated)
- **Promoted** via the `promote_insight` skill (extends or removes expiry)
- **Dismissed** via the `dismiss_insight` skill (immediate deletion)
- **Expired** — the cleanup job runs hourly and removes insights past `expires_at`

### Insight Quality Scoring

After generating an insight, a second LLM call rates it:

```
System: "Rate the following insight on a scale of 1-5 for novelty and usefulness."
User: [insight text]
Response: single digit 1-5
```

If `quality_threshold > 0` and the score is below threshold, the insight is discarded. This prevents low-quality insights from cluttering the memory.

## Embeddings

### ONNX Provider

Iulita uses a pure-Go ONNX runtime (`knights-analytics/hugot`) to generate embeddings locally — no external API calls needed.

- **Model**: `KnightsAnalytics/all-MiniLM-L6-v2` — sentence transformer, 384 dimensions
- **Runtime**: Pure Go (no CGo, no shared libraries)
- **Thread safety**: Protected by `sync.Mutex` (hugot pipeline is not thread-safe)
- **Model caching**: Downloaded once to `~/.local/share/iulita/models/`, reused on subsequent runs

### Vector Storage

Embeddings are stored as binary BLOBs in SQLite:

- **Encoding**: Each `float32` → 4 bytes LittleEndian, packed into `[]byte`
- **384 dimensions** → 1536 bytes per vector
- **Tables**: `fact_vectors` (fact_id PK), `insight_vectors` (insight_id PK)
- **Cascade delete**: removing a fact/insight automatically removes its vector

### Embedding Cache

The `embedding_cache` table prevents re-computing embeddings for identical texts:

- **Key**: SHA-256 hash of the input text
- **LRU eviction**: keeps only the N most recently accessed entries (default 10,000)
- **Used by**: `CachedEmbeddingProvider` wrapper around ONNX

### Hybrid Search Algorithm

```python
# Pseudocode
def hybrid_search(query, user_id, limit):
    # 1. FTS5 results (oversampled)
    fts_results = FTS_MATCH(query, limit * 2)
    fts_scores = {r.id: 1 - i/(len+1) for i, r in enumerate(fts_results)}

    # 2. Vector similarity
    query_vec = onnx.embed(query)
    all_vecs = load_all_vectors(user_id)
    vec_scores = {id: cosine_similarity(query_vec, vec) for id, vec in all_vecs}

    # 3. Combine
    all_ids = set(fts_scores) | set(vec_scores)
    combined = {}
    for id in all_ids:
        fts = fts_scores.get(id, 0)
        vec = vec_scores.get(id, 0)
        combined[id] = (1 - vectorWeight) * fts + vectorWeight * vec

    # 4. Top-N
    return sorted(combined, key=combined.get, reverse=True)[:limit]
```

**Configuration**: `skills.memory.vector_weight` (default 0, FTS-only). Set to 0.3-0.5 for hybrid search.

## Memory in the Assistant Loop

Every message triggers memory injection into the system prompt:

1. **Recent facts** (up to 20): loaded from DB, decay + MMR applied, formatted as `## Remembered Facts`
2. **Relevant insights** (up to 5): hybrid search using the message text, formatted as `## Insights`
3. **User profile** (tech facts): behavioral metadata grouped by category, formatted as `## User Profile`
4. **User directive**: persistent custom instruction, formatted as `## User Directives`

This context appears in the **dynamic system prompt** (per-message, not cached by Claude).

## Memory Export / Import

### Export

```go
memory.ExportFacts(ctx, store, chatID) // → Markdown string
memory.ExportAllFacts(ctx, store, dir) // → one .md file per chat
```

Format:
```markdown
## Fact 42
The user prefers dark mode in all IDEs.

## Fact 43
User's favorite programming language is Go.
```

### Import

```go
memory.ImportFacts(ctx, store, chatID, markdownContent)
```

Parses the markdown, creates new facts (original IDs are discarded — new IDs are assigned by SQLite autoincrement). Each imported fact is automatically embedded.

## Configuration Reference

| Parameter | Default | Description |
|-----------|---------|-------------|
| `skills.memory.half_life_days` | 30 | Temporal decay half-life; 0 = disabled |
| `skills.memory.mmr_lambda` | 0 | MMR diversity (0 = disabled, 0.7 recommended) |
| `skills.memory.vector_weight` | 0 | Hybrid search blend (0 = FTS only, 0.5 = balanced) |
| `skills.insights.min_facts` | 20 | Minimum facts to trigger insight generation |
| `skills.insights.max_pairs` | 6 | Max cross-cluster pairs per generation run |
| `skills.insights.ttl` | 720h | Insight expiry TTL (30 days) |
| `skills.insights.interval` | 24h | How often insights are generated |
| `skills.insights.quality_threshold` | 0 | Minimum quality score (0 = accept all) |
| `embedding.model` | `KnightsAnalytics/all-MiniLM-L6-v2` | ONNX model name |
| `embedding.enabled` | true | Enable ONNX embeddings |
