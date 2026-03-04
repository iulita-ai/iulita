# Fournisseurs LLM

Iulita prend en charge plusieurs fournisseurs LLM a travers une architecture basee sur les decorateurs. Les fournisseurs peuvent etre composes en chaines avec des couches de retry, repli, mise en cache, routage et classification.

## Interface Provider

```go
type Provider interface {
    Complete(ctx context.Context, req Request) (Response, error)
}

type StreamingProvider interface {
    Provider
    CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error)
}

type EmbeddingProvider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimensions() int
}
```

## Request / Response

### Structure de la requete

```go
Request {
    StaticSystemPrompt  string          // mis en cache par Claude (base + prompts des competences)
    SystemPrompt        string          // par message (heure, faits, directives)
    History             []ChatMessage   // historique de la conversation
    Message             string          // message utilisateur actuel
    Images              []ImageAttachment
    Documents           []DocumentAttachment
    Tools               []ToolDefinition
    ToolExchanges       []ToolExchange  // echanges d'outils accumules pour ce tour
    ThinkingBudget      int64           // jetons de reflexion etendue (0 = desactive)
    ForceTool           string          // forcer un appel d'outil specifique
    RouteHint           string          // indication pour le fournisseur de routage
}
```

**Conception cle** : le prompt systeme est divise en `StaticSystemPrompt` (stable, cacheable) et `SystemPrompt` (dynamique, par message). Les fournisseurs non-Claude utilisent `FullSystemPrompt()` qui concatene les deux.

### Structure de la reponse

```go
Response {
    Content    string
    ToolCalls  []ToolCall
    Usage      Usage {
        InputTokens              int
        OutputTokens             int
        CacheReadInputTokens     int
        CacheCreationInputTokens int
    }
}
```

## Fournisseur Claude

Le fournisseur principal, utilisant le SDK officiel `anthropic-sdk-go`.

### Fonctionnalites

- **Mise en cache des prompts** : `StaticSystemPrompt` recoit `cache_control: ephemeral` — Claude met en cache ce bloc entre les requetes, reduisant les couts de jetons d'entree
- **Streaming** : `CompleteStream` utilise l'API de streaming avec traitement des `ContentBlockDeltaEvent`
- **Reflexion etendue** : lorsque `ThinkingBudget > 0`, la configuration de reflexion est ajoutee et les jetons max sont augmentes
- **ForceTool** : utilise `ToolChoiceParamOfTool(name)` pour forcer un outil specifique (desactive la reflexion — contrainte API)
- **Detection de depassement de contexte** : verifie les messages d'erreur pour "prompt is too long" / "context_length_exceeded" et encapsule avec le sentinelle `ErrContextTooLarge`
- **Support des documents** : fichiers PDF via `Base64PDFSourceParam`, fichiers texte via `PlainTextSourceParam`
- **Support des images** : images encodees en base64 avec type de media
- **Rechargeable a chaud** : le modele, les jetons max et la cle API peuvent etre mis a jour a l'execution via `sync.RWMutex`

### Mise en cache des prompts

La separation prompt statique/dynamique est la cle d'une utilisation efficace de Claude :

```
Bloc 1 : StaticSystemPrompt (cache_control: ephemeral)
  ├── Prompt systeme de base (personnalite, instructions)
  └── Prompts systeme des competences (de toutes les competences activees)

Bloc 2 : SystemPrompt (pas de controle de cache)
  ├── ## Current Time
  ├── ## User Directives
  ├── ## User Profile (tech facts)
  ├── ## Remembered Facts
  ├── ## Insights
  └── ## Language directive (si non anglais)
```

Le Bloc 1 est mis en cache par Claude entre les requetes (coute `cache_creation_input_tokens` a la premiere utilisation, `cache_read_input_tokens` lors des acces suivants). Le Bloc 2 change a chaque message et n'est jamais mis en cache.

### Streaming

Le streaming est utilise uniquement lorsque `len(req.Tools) == 0` (l'assistant desactive le streaming pendant la boucle agentique d'utilisation d'outils). La boucle d'evenements de streaming traite :

- `ContentBlockDeltaEvent` avec `type == "text_delta"` → appelle `callback(chunk)` et accumule
- `MessageStartEvent` → capture les jetons d'entree + metriques de cache
- `MessageDeltaEvent` → capture les jetons de sortie

### Recuperation de depassement de contexte

Lorsque l'API Claude retourne une erreur de depassement de contexte :

1. `isContextOverflowError(err)` l'encapsule comme `llm.ErrContextTooLarge`
2. La boucle agentique de l'assistant l'attrape via `llm.IsContextTooLarge(err)`
3. Si pas encore compresse ce tour : forcer la compression de l'historique et reessayer (`i--`)
4. Si deja compresse : propager l'erreur

### Configuration

| Cle | Defaut | Description |
|-----|--------|-------------|
| `claude.api_key` | — | Cle API Anthropic (requise) |
| `claude.model` | `claude-sonnet-4-5-20250929` | Identifiant du modele |
| `claude.max_tokens` | 8192 | Jetons de sortie maximum |
| `claude.base_url` | — | Surcharger l'URL de base de l'API |
| `claude.thinking` | 0 | Budget de reflexion etendue (0 = desactive) |

## Fournisseur Ollama

Fournisseur LLM local pour le developpement et les taches d'arriere-plan.

### Limitations

- **Pas de support d'outils** — retourne une erreur si `len(req.Tools) > 0`
- **Pas de streaming** — `CompleteStream` n'est pas implemente
- Utilise `FullSystemPrompt()` (pas de benefice de mise en cache)

### Cas d'utilisation

- Developpement local sans couts API
- Taches de delegation en arriere-plan (traductions, resumes)
- Classificateur economique pour le `ClassifyingProvider`

### API

Appelle `POST /api/chat` avec les messages en format compatible OpenAI. `ListModels()` interroge `GET /api/tags` pour la decouverte de modeles.

### Configuration

| Cle | Defaut | Description |
|-----|--------|-------------|
| `ollama.url` | `http://localhost:11434` | URL du serveur Ollama |
| `ollama.model` | `llama3` | Nom du modele |

## Fournisseur OpenAI

Client REST compatible OpenAI. Fonctionne avec tout service compatible OpenAI (Together AI, Azure, etc.).

### Limitations

- **Pas de support d'outils** — identique a Ollama
- Utilise `FullSystemPrompt()`

### Configuration

| Cle | Defaut | Description |
|-----|--------|-------------|
| `openai.api_key` | — | Cle API |
| `openai.model` | `gpt-4` | Identifiant du modele |
| `openai.base_url` | `https://api.openai.com/v1` | URL de base de l'API |

## Fournisseur d'embeddings ONNX

Modele d'embeddings local en pur Go pour la recherche vectorielle.

- **Modele** : `KnightsAnalytics/all-MiniLM-L6-v2` (384 dimensions)
- **Runtime** : `knights-analytics/hugot` — ONNX pur Go (pas de CGo)
- **Securite des threads** : `sync.Mutex` (le pipeline hugot n'est pas thread-safe)
- **Cache** : Telecharge une fois dans `~/.local/share/iulita/models/`
- **Normalisation** : vecteurs de sortie normalises L2 (prets pour la similarite cosinus)

Voir [Memoire et observations](memory-and-insights.md#embeddings) pour les details sur l'utilisation des embeddings.

## Decorateurs de fournisseurs

### RetryProvider

Encapsule tout fournisseur avec un retry a backoff exponentiel :

- **Tentatives max** : 3
- **Delai de base** : 500ms
- **Delai max** : 8s
- **Gigue** : multiplicateur aleatoire 0.5-1.5x
- **Codes retriables** : 429, 500, 502, 503, 529 (Anthropic surcharge)
- **Non retriables** : 4xx (sauf 429), depassement de contexte

### FallbackProvider

Essaie les fournisseurs dans l'ordre, retourne le premier succes. Utile pour les chaines de repli `Claude → OpenAI`.

### CachingProvider

Met en cache les reponses LLM par hash de l'entree :

- **Cle** : SHA-256 de `systemPrefix[:200] + "|" + message`
- **TTL** : 60 minutes (configurable)
- **Entrees max** : 1000 (eviction LRU)
- **Ignore** : requetes avec outils ou echanges d'outils (non deterministes)
- **Stockage** : table SQLite `response_cache`

### CachedEmbeddingProvider

Met en cache les embeddings par texte :

- **Cle** : SHA-256 du texte d'entree
- **Entrees max** : 10 000 (eviction LRU)
- **Traitement par lot** : les manques de cache sont groupes pour un seul appel fournisseur
- **Stockage** : table SQLite `embedding_cache`

### RoutingProvider

Route vers des fournisseurs nommes par `req.RouteHint`. Analyse egalement le prefixe `hint:<name> <message>` dans le message utilisateur. Delegue `CompleteStream` au fournisseur resolu s'il est un `StreamingProvider`.

### ClassifyingProvider

Encapsule un `RoutingProvider`. A chaque requete :

1. Envoie un prompt de classification a un fournisseur economique (Ollama) : "Classifier : simple/complexe/creatif"
2. Definit `RouteHint` en fonction de la classification
3. Route vers le fournisseur appropriate

Repli vers le defaut en cas d'erreur du classificateur.

### XMLToolProvider

Pour les fournisseurs sans appel d'outils natif (Ollama, OpenAI) :

1. Injecte un bloc XML `<available_tools>` dans le prompt systeme
2. Ajoute des instructions : "Pour utiliser un outil, repondez avec `<tool_use name="..."><input>{...}</input></tool_use>`"
3. Supprime `Tools` de la requete
4. Analyse les appels d'outils XML dans la reponse via regex

## Assemblage de la chaine de fournisseurs

La chaine est construite dans `cmd/iulita/main.go` :

```
Claude Provider
    └→ Retry Provider
        └→ [Optionnel] Fallback Provider (+ OpenAI)
            └→ [Optionnel] Caching Provider
                └→ [Optionnel] Routing Provider
                    └→ [Optionnel] Classifying Provider (+ Ollama)
```

Chaque couche est ajoutee conditionnellement en fonction de la configuration.
