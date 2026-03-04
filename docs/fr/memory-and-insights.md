# Memoire et observations

## Philosophie

La plupart des assistants IA oublient tout entre les sessions ou hallucinent des « souvenirs » a partir de leurs donnees d'entrainement. Iulita adopte une approche fondamentalement differente :

- **Stockage explicite uniquement** — les faits ne sont stockes que lorsque vous le demandez (« remember that... »)
- **Donnees verifiees** — chaque fait remonte a une demande specifique de l'utilisateur
- **Observations par croisement** — les tendances sont decouvertes en analysant vos faits reels
- **Decroissance temporelle** — les souvenirs plus anciens perdent naturellement en pertinence sauf si vous y accedez
- **Recherche hybride** — recherche plein texte FTS5 combinee avec des embeddings vectoriels ONNX

## Faits

### Stocker des faits (Remember)

Lorsque vous dites « remember that my dog's name is Max », voici ce qui se passe :

1. **Detection du declencheur** — l'assistant detecte le mot-cle de memorisation (« remember ») et force l'outil `remember`
2. **Verification des doublons** — recherche dans les faits existants en utilisant les 3 premiers mots pour detecter les quasi-doublons
3. **INSERT SQLite** — le fait est sauvegarde avec `user_id`, `content`, `source_type=user`, horodatages
4. **Index FTS5** — un declencheur `AFTER INSERT` ajoute automatiquement le fait a l'index plein texte `facts_fts`
5. **Embedding vectoriel** — une goroutine en arriere-plan genere un embedding ONNX (384 dimensions, all-MiniLM-L6-v2) et le stocke dans `fact_vectors`

```
domain.Fact {
    ID             int64
    ChatID         string     // canal source ("123456789", "console", "web:uuid")
    UserID         string     // UUID iulita — partage entre tous les canaux
    Content        string     // le texte du fait
    SourceType     string     // "user" (explicite) ou "system" (extrait automatiquement)
    CreatedAt      time.Time
    LastAccessedAt time.Time  // reinitialise a chaque rappel
    AccessCount    int        // incremente a chaque rappel
}
```

### Rappeler des faits (Recall)

Lorsque vous demandez « what is my dog's name? » :

1. **Recherche scopee par utilisateur** — essaie d'abord `SearchFactsByUser(userID, query, limit)` pour les faits inter-canaux
2. **Correspondance FTS5** — `SELECT * FROM facts WHERE id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)`
3. **Sur-echantillonnage** — recupere `limit * 3` candidats (minimum 20) pour le reordonnancement en aval
4. **Decroissance temporelle** — chaque candidat est note : `decay = exp(-ln(2) / halfLife * ageDays) * (1 + log(1 + accessCount))`
5. **Reordonnancement MMR** — la pertinence marginale maximale reduit les quasi-doublons dans l'ensemble de resultats
6. **Renforcement** — chaque fait retourne recoit `access_count++` et `last_accessed_at = now`

L'assistant effectue egalement une **recherche hybride** a chaque message (pas seulement lors d'un rappel explicite) :

```
1. Assainir la requete (supprimer les operateurs FTS, les mots vides, limiter a 5 mots-cles)
2. Generer le vecteur de la requete via embedding ONNX
3. Resultats FTS : score base sur la position (1 - i/(n+1))
4. Resultats vectoriels : similarite cosinus avec tous les vecteurs stockes
5. Combine : (1-vectorWeight)*ftsScore + vectorWeight*vecScore
6. Union des deux ensembles, tries par score combine
```

### Oublier des faits

L'outil `forget` supprime un fait par ID. Le declencheur FTS (`facts_ad`) le retire automatiquement de l'index plein texte. Le `ON DELETE CASCADE` sur `fact_vectors` supprime l'embedding.

## Decroissance temporelle

Les faits et les observations decroissent au fil du temps selon une decroissance exponentielle (radioactive) :

```
decay_factor = exp(-ln(2) / halfLifeDays * ageDays)
```

| Jours depuis le dernier acces | Demi-vie = 30 jours | Demi-vie = 90 jours |
|-------------------------------|---------------------|---------------------|
| 0 | 1.00 | 1.00 |
| 15 | 0.71 | 0.89 |
| 30 | 0.50 | 0.79 |
| 60 | 0.25 | 0.63 |
| 90 | 0.13 | 0.50 |

**Choix de conception cles :**
- Les **faits** decroissent depuis `last_accessed_at` — chaque rappel reinitialise le compteur
- Les **observations** decroissent depuis `created_at` — elles ont une duree de vie fixe
- **Boost d'acces** : `1 + log(1 + accessCount)` — un fait accede 100 fois obtient un boost de 4.6x
- **Demi-vie par defaut** : 30 jours (configurable via `skills.memory.half_life_days`)

## Reordonnancement MMR

Apres le scoring par decroissance temporelle, la pertinence marginale maximale (MMR) empeche les resultats quasi-dupliques :

```
MMR(item) = lambda * relevance_score - (1 - lambda) * max_similarity_to_selected
```

- `lambda = 1.0` → pertinence pure, pas de diversite
- `lambda = 0.7` → recommande : favoriser la pertinence mais penaliser les quasi-doublons
- `lambda = 0.0` → diversite pure

La similarite est mesuree via la similarite de Jaccard sur les tokens de mots (approximation sans dependance qui fonctionne sans l'embedder ONNX).

**Configuration** : `skills.memory.mmr_lambda` (defaut 0, desactive). Definir a 0.7 pour de meilleurs resultats.

## Observations

Les observations sont des croisements generes par l'IA entre vos faits, decouverts par le planificateur en arriere-plan.

### Pipeline de generation

La tache de generation d'observations s'execute toutes les 24 heures (configurable) :

```
1. Charger tous les faits de l'utilisateur
2. Verifier le nombre minimum de faits (defaut 20)
3. Construire les vecteurs TF-IDF
   - Tokeniser : minuscules, supprimer la ponctuation, filtrer les mots vides
   - Generer des bigrammes (paires de mots adjacents)
   - Calculer les scores TF-IDF
4. Clustering K-means++
   - k = sqrt(numFacts / 3)
   - Metrique de distance cosinus
   - 20 iterations maximum
5. Echantillonner des paires inter-clusters
   - Jusqu'a 6 paires par execution
   - Ignorer les paires de faits deja couvertes
6. Pour chaque paire :
   a. Envoyer au LLM : "Genere une observation creative a partir de ces deux clusters"
   b. Noter la qualite (1-5) via un appel LLM separe
   c. Stocker si qualite >= seuil
```

### Cycle de vie des observations

```
domain.Insight {
    ID             int64
    ChatID         string
    UserID         string
    Content        string     // le texte de l'observation
    FactIDs        string     // IDs des faits sources, separes par des virgules
    Quality        int        // score de qualite LLM 1-5
    AccessCount    int
    LastAccessedAt time.Time
    CreatedAt      time.Time
    ExpiresAt      *time.Time // defaut : creation + 30 jours
}
```

- **Creees** par le planificateur en arriere-plan apres clustering et synthese LLM
- **Surfacees** dans le prompt systeme de l'assistant lorsque contextuellement pertinentes (recherche hybride)
- **Renforcees** lors de l'acces (compteur d'acces et heure du dernier acces mis a jour)
- **Promues** via la competence `promote_insight` (prolonge ou supprime l'expiration)
- **Rejetees** via la competence `dismiss_insight` (suppression immediate)
- **Expirees** — la tache de nettoyage s'execute toutes les heures et supprime les observations au-dela de `expires_at`

### Notation de la qualite des observations

Apres la generation d'une observation, un second appel LLM l'evalue :

```
Systeme : "Notez l'observation suivante sur une echelle de 1 a 5 pour la nouveaute et l'utilite."
Utilisateur : [texte de l'observation]
Reponse : un seul chiffre 1-5
```

Si `quality_threshold > 0` et que le score est inferieur au seuil, l'observation est ecartee. Cela empeche les observations de faible qualite d'encombrer la memoire.

## Embeddings

### Fournisseur ONNX

Iulita utilise un runtime ONNX en pur Go (`knights-analytics/hugot`) pour generer des embeddings localement — aucun appel API externe necessaire.

- **Modele** : `KnightsAnalytics/all-MiniLM-L6-v2` — transformeur de phrases, 384 dimensions
- **Runtime** : Pur Go (pas de CGo, pas de bibliotheques partagees)
- **Securite des threads** : Protege par `sync.Mutex` (le pipeline hugot n'est pas thread-safe)
- **Cache du modele** : Telecharge une fois dans `~/.local/share/iulita/models/`, reutilise lors des executions suivantes

### Stockage vectoriel

Les embeddings sont stockes sous forme de BLOBs binaires en SQLite :

- **Encodage** : Chaque `float32` → 4 octets LittleEndian, empaquetes en `[]byte`
- **384 dimensions** → 1536 octets par vecteur
- **Tables** : `fact_vectors` (fact_id PK), `insight_vectors` (insight_id PK)
- **Suppression en cascade** : la suppression d'un fait/observation supprime automatiquement son vecteur

### Cache d'embeddings

La table `embedding_cache` evite de recalculer les embeddings pour des textes identiques :

- **Cle** : hash SHA-256 du texte d'entree
- **Eviction LRU** : conserve uniquement les N entrees les plus recemment accedees (defaut 10 000)
- **Utilise par** : le wrapper `CachedEmbeddingProvider` autour d'ONNX

### Algorithme de recherche hybride

```python
# Pseudocode
def hybrid_search(query, user_id, limit):
    # 1. Resultats FTS5 (sur-echantillonnes)
    fts_results = FTS_MATCH(query, limit * 2)
    fts_scores = {r.id: 1 - i/(len+1) for i, r in enumerate(fts_results)}

    # 2. Similarite vectorielle
    query_vec = onnx.embed(query)
    all_vecs = load_all_vectors(user_id)
    vec_scores = {id: cosine_similarity(query_vec, vec) for id, vec in all_vecs}

    # 3. Combiner
    all_ids = set(fts_scores) | set(vec_scores)
    combined = {}
    for id in all_ids:
        fts = fts_scores.get(id, 0)
        vec = vec_scores.get(id, 0)
        combined[id] = (1 - vectorWeight) * fts + vectorWeight * vec

    # 4. Top-N
    return sorted(combined, key=combined.get, reverse=True)[:limit]
```

**Configuration** : `skills.memory.vector_weight` (defaut 0, FTS uniquement). Definir a 0.3-0.5 pour la recherche hybride.

## Memoire dans la boucle de l'assistant

Chaque message declenche l'injection de memoire dans le prompt systeme :

1. **Faits recents** (jusqu'a 20) : charges depuis la BDD, decroissance + MMR appliques, formates comme `## Remembered Facts`
2. **Observations pertinentes** (jusqu'a 5) : recherche hybride utilisant le texte du message, formatees comme `## Insights`
3. **Profil utilisateur** (tech facts) : metadonnees comportementales groupees par categorie, formatees comme `## User Profile`
4. **Directive utilisateur** : instruction personnalisee persistante, formatee comme `## User Directives`

Ce contexte apparait dans le **prompt systeme dynamique** (par message, non mis en cache par Claude).

## Export / Import de memoire

### Export

```go
memory.ExportFacts(ctx, store, chatID) // → chaine Markdown
memory.ExportAllFacts(ctx, store, dir) // → un fichier .md par chat
```

Format :
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

Analyse le Markdown, cree de nouveaux faits (les IDs originaux sont ignores — de nouveaux IDs sont attribues par l'auto-increment SQLite). Chaque fait importe est automatiquement encode en vecteur.

## Reference de configuration

| Parametre | Defaut | Description |
|-----------|--------|-------------|
| `skills.memory.half_life_days` | 30 | Demi-vie de la decroissance temporelle ; 0 = desactive |
| `skills.memory.mmr_lambda` | 0 | Diversite MMR (0 = desactive, 0.7 recommande) |
| `skills.memory.vector_weight` | 0 | Ponderation de la recherche hybride (0 = FTS uniquement, 0.5 = equilibre) |
| `skills.insights.min_facts` | 20 | Nombre minimum de faits pour declencher la generation d'observations |
| `skills.insights.max_pairs` | 6 | Nombre max de paires inter-clusters par execution |
| `skills.insights.ttl` | 720h | TTL d'expiration des observations (30 jours) |
| `skills.insights.interval` | 24h | Frequence de generation des observations |
| `skills.insights.quality_threshold` | 0 | Score de qualite minimum (0 = tout accepter) |
| `embedding.model` | `KnightsAnalytics/all-MiniLM-L6-v2` | Nom du modele ONNX |
| `embedding.enabled` | true | Activer les embeddings ONNX |
