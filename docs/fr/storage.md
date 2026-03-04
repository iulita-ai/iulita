# Stockage

Iulita utilise SQLite comme unique backend de stockage avec le mode WAL, la recherche plein texte FTS5, la recherche vectorielle ONNX et l'ORM bun.

## Configuration SQLite

### Connexion

- **Pilote** : `modernc.org/sqlite` (pur Go, pas de CGo)
- **ORM** : `uptrace/bun` avec `sqlitedialect`
- **DSN** : `file:{path}?cache=shared&mode=rwc`

### PRAGMAs de performance

```sql
PRAGMA journal_mode = WAL;       -- Write-Ahead Logging (lectures concurrentes)
PRAGMA synchronous = NORMAL;     -- Securise avec WAL, 2x plus rapide en ecriture vs FULL
PRAGMA mmap_size = 8388608;      -- 8 Mo d'E/S mappees en memoire
PRAGMA cache_size = -2000;       -- ~2 Mo de cache de pages en processus
PRAGMA temp_store = MEMORY;      -- Tables temporaires en RAM (accelere FTS5/tris)
```

## Schema

### Tables principales

| Table | Objectif | Colonnes cles |
|-------|----------|---------------|
| `users` | Comptes utilisateur | id (UUID), username, password_hash, role, timezone |
| `user_channels` | Liaisons de canaux | user_id, channel_type, channel_user_id, locale |
| `channel_instances` | Configs bot/integration | slug, type, enabled, config_json |
| `chat_messages` | Historique de conversation | chat_id, user_id, role, content |
| `facts` | Souvenirs stockes | user_id, content, source_type, access_count, last_accessed_at |
| `insights` | Observations par croisement | user_id, content, fact_ids, quality, expires_at |
| `directives` | Instructions personnalisees | user_id, content |
| `tech_facts` | Profil comportemental | user_id, category, key, value, confidence |
| `reminders` | Rappels temporels | user_id, title, due_at, fired |
| `tasks` | File de taches du planificateur | type, status, payload, worker_id, capabilities |
| `scheduler_states` | Horaires des taches | job_name, last_run, next_run |
| `agent_jobs` | Taches LLM planifiees par l'utilisateur | name, prompt, cron_expr, delivery_chat_id |
| `config_overrides` | Configuration a l'execution | key, value, encrypted, updated_by |
| `google_accounts` | Jetons OAuth2 | user_id, account_email, tokens (chiffres) |
| `installed_skills` | Competences externes | slug, name, version, source |
| `todo_items` | Taches unifiees | user_id, title, provider, external_id, due_date |
| `audit_log` | Piste d'audit | user_id, action, details |
| `usage_stats` | Utilisation de jetons par heure | chat_id, hour, input_tokens, output_tokens, cost_usd |

### Tables FTS5

```sql
CREATE VIRTUAL TABLE facts_fts USING fts5(content, content_rowid=id);
CREATE VIRTUAL TABLE insights_fts USING fts5(content, content_rowid=id);
```

Ce sont des tables FTS5 a « contenu externe » — l'index reflette le contenu des tables de base via des declencheurs.

### Declencheurs FTS5

Six declencheurs maintiennent les index FTS synchronises :

```sql
-- Facts
CREATE TRIGGER facts_ai AFTER INSERT ON facts BEGIN
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER facts_ad AFTER DELETE ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
END;

-- CRITIQUE : "UPDATE OF content" — PAS "UPDATE"
CREATE TRIGGER facts_au AFTER UPDATE OF content ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;
```

**Le piege des declencheurs** : utiliser `AFTER UPDATE ON facts` (sans `OF content`) provoque le declenchement du trigger a CHAQUE mise a jour de colonne (ex. `access_count++`). Avec l'implementation FTS5 de modernc SQLite, cela cause `SQL logic error (1)`. La solution est `AFTER UPDATE OF content` — le declencheur ne se declenche que lorsque `content` change specifiquement.

### Tables vectorielles

```sql
CREATE TABLE IF NOT EXISTS fact_vectors (
    fact_id INTEGER PRIMARY KEY REFERENCES facts(id) ON DELETE CASCADE,
    embedding BLOB NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS insight_vectors (
    insight_id INTEGER PRIMARY KEY REFERENCES insights(id) ON DELETE CASCADE,
    embedding BLOB NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

**Encodage** : chaque `float32` → 4 octets LittleEndian. 384 dimensions = 1536 octets par vecteur.

**Auto-detection** : `decodeVector()` verifie si les donnees commencent par `[` (tableau JSON) pour la compatibilite legacy ; sinon decode en binaire.

### Tables de cache

```sql
CREATE TABLE IF NOT EXISTS embedding_cache (
    content_hash TEXT PRIMARY KEY,
    embedding BLOB NOT NULL,
    dimensions INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS response_cache (
    prompt_hash TEXT PRIMARY KEY,
    model TEXT NOT NULL,
    response TEXT NOT NULL,
    usage_json TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    hit_count INTEGER NOT NULL DEFAULT 0
);
```

Les deux utilisent l'eviction LRU (le plus ancien `accessed_at` supprime lorsque le nombre max d'entrees est depasse).

## Portee des donnees multi-utilisateur

Toutes les tables de donnees ont une colonne `user_id TEXT`. Les requetes utilisent les variantes scopees par utilisateur lorsque disponibles :

```
SearchFacts(ctx, chatID, query, limit)        // legacy, canal unique
SearchFactsByUser(ctx, userID, query, limit)  // inter-canal
```

La variante scopee par utilisateur est preferee : les faits d'un utilisateur depuis Telegram, WebChat et Console sont tous accessibles quel que soit le canal actuel.

### BackfillUserIDs

Migration qui associe les donnees legacy (creees avant le support multi-utilisateur) aux utilisateurs :

1. Supprimer les declencheurs FTS (requis — UPDATE les declencherait)
2. Joindre `chat_messages → user_channels` sur `chat_id`
3. UPDATE en masse : definir `user_id` sur facts, insights, messages, etc.
4. Recreer les declencheurs FTS

## Migrations

Toutes les migrations s'executent dans `RunMigrations(ctx)` comme une fonction idempotente unique :

1. **Creation de tables** via `bun.CreateTableIfNotExists` pour les 18 modeles de domaine
2. **Tables SQL brutes** pour les caches (pas gerees par bun)
3. **Colonnes additives** via `ALTER TABLE ADD COLUMN` (erreurs ignorees pour l'idempotence)
4. **Renommages legacy** (ex. `dreams → insights`)
5. **Creation d'index** pour les requetes critiques en performance
6. **Recreation FTS5** — toujours supprimer et recreer les declencheurs + tables pour corriger la corruption des versions anterieures

### Index cles

| Index | Objectif |
|-------|----------|
| `idx_tasks_status_scheduled` | File de taches : taches en attente par scheduled_at |
| `idx_tasks_unique_key` | Creation de taches idempotente |
| `idx_facts_user` | Requetes de faits scopees par utilisateur |
| `idx_insights_user` | Requetes d'observations scopees par utilisateur |
| `idx_user_channels_binding` | Unique (channel_type, channel_user_id) |
| `idx_todos_user_due` | Tableau de bord des taches : taches par date d'echeance |
| `idx_todos_external` | Deduplication de taches externes par provider+external_id |
| `idx_techfacts_unique` | Unique (chat_id, category, key) |
| `idx_google_accounts_user_email` | Unique (user_id, account_email) |

## File de taches

Le planificateur utilise SQLite comme file de taches avec reclamation atomique :

### Cycle de vie des taches

```
pending → claimed → running → completed/failed
```

### Reclamation atomique

`ClaimTask(ctx, workerID, capabilities)` utilise une transaction :
1. SELECT une tache en attente dont les `Capabilities` sont un sous-ensemble de celles du worker
2. UPDATE du statut a `claimed`, definir `worker_id`
3. Retourner la tache

Cela garantit qu'aucun worker ne reclame la meme tache.

### Nettoyage

- **Taches perimees** : les taches `running` depuis plus de 5 minutes sont remises a `pending`
- **Anciennes taches** : les taches completed/failed de plus de 7 jours sont supprimees
- **Ponctuelles** : les taches avec `one_shot = true` et `delete_after_run = true` sont supprimees apres completion

## Recherche hybride

Voir [Memoire et observations — Recherche hybride](memory-and-insights.md#algorithme-de-recherche-hybride) pour l'algorithme complet.

### Patron de requete

```sql
-- Recherche FTS5
SELECT * FROM facts
WHERE user_id = ?
  AND id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)
ORDER BY access_count DESC, last_accessed_at DESC
LIMIT ?

-- Similarite vectorielle (en Go)
for _, vec := range allVectors {
    score := cosineSimilarity(queryVec, vec.Embedding)
}

-- Scoring combine
combined = (1-vectorWeight)*ftsScore + vectorWeight*vecScore
```

## Interface Repository

L'interface `Repository` dans `internal/storage/storage.go` possede plus de 60 methodes organisees par domaine :

| Domaine | Methodes |
|---------|----------|
| Messages | Save, GetHistory, Clear, DeleteBefore |
| Faits | Save, Search (FTS/Hybride), Update, Delete, Reinforce, GetAll, GetRecent |
| Observations | Save, Search (FTS/Hybride), Delete, Reinforce, GetRecent, DeleteExpired |
| Vecteurs | CreateTables, SaveFactVector, SaveInsightVector, GetWithoutEmbeddings |
| Utilisateurs | Create, Get, Update, Delete, List, BindChannel, GetByChannel |
| Taches | Create, Claim, Start, Complete, Fail, List, Cleanup |
| Configuration | Get, List, Save, Delete overrides |
| Caches | Embedding (get/save/evict), Response (get/save/evict/stats) |
| Locale | Update, Get (par type de canal ou ID de chat) |
| Todos | CRUD, sync, counts, requetes par fournisseur |
| Agent Jobs | CRUD, GetDue |
| Google | Account CRUD, stockage de jetons |
| Competences externes | Install, List, Get, Delete |
