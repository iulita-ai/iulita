# Architecture

## Vue d'ensemble

```
Console TUI ─┐
Telegram ────┤
Web Chat ────┼→ Channel Manager → Assistant → LLM Provider Chain
                     ↕                ↕
                 UserResolver      Storage (SQLite)
                                     ↕
                               Scheduler → Worker
                               (insights, analysis, reminders)
                                     ↕
                                Event Bus → Dashboard (WebSocket)
                                          → Prometheus Metrics
                                          → Push Notifications
                                          → Cost Tracker
```

## Principes de conception fondamentaux

1. **Memoire basee sur les faits** — seules les donnees utilisateur verifiees sont stockees, jamais de connaissances hallucinees
2. **Console d'abord** — le TUI est le mode par defaut ; le mode serveur est optionnel
3. **Architecture propre** — modeles de domaine -> interfaces -> implementations -> orchestrateur
4. **Multi-canal, identite unique** — les faits et observations sont partages entre tous les canaux via user_id
5. **Installation locale sans configuration** — fonctionne directement avec uniquement une cle API
6. **Rechargement a chaud** — les competences, la configuration et meme le jeton Telegram peuvent changer a l'execution sans redemarrage

## Carte des composants

| Composant | Paquet | Description |
|-----------|--------|-------------|
| Point d'entree | `cmd/iulita/` | Analyse CLI, injection de dependances, arret gracieux |
| Assistant | `internal/assistant/` | Orchestrateur : boucle LLM, memoire, compression, approbations, streaming |
| Canaux | `internal/channel/` | Adaptateurs d'entree : Console TUI, Telegram, WebChat |
| Gestionnaire de canaux | `internal/channelmgr/` | Cycle de vie des canaux, routage, rechargement a chaud |
| Fournisseurs LLM | `internal/llm/` | Claude, Ollama, OpenAI, embeddings ONNX |
| Competences | `internal/skill/` | 20+ implementations d'outils |
| Gestionnaire de competences | `internal/skillmgr/` | Competences externes : marketplace ClawhHub, URL, local |
| Stockage | `internal/storage/sqlite/` | SQLite avec FTS5, vecteurs, mode WAL |
| Planificateur | `internal/scheduler/` | File de taches avec support cron/intervalle |
| Tableau de bord | `internal/dashboard/` | API REST GoFiber + SPA Vue 3 embarquee |
| Configuration | `internal/config/` | Configuration en couches : defauts -> TOML -> env -> trousseau -> BDD |
| Authentification | `internal/auth/` | JWT + bcrypt, middleware |
| i18n | `internal/i18n/` | 6 langues, catalogues TOML, propagation de contexte |
| Recherche web | `internal/web/` | Brave + DuckDuckGo en repli, protection SSRF |
| Domaine | `internal/domain/` | Modeles de domaine purs |
| Memoire | `internal/memory/` | Clustering TF-IDF, export/import de memoire |
| Metriques | `internal/metrics/` | Compteurs et histogrammes Prometheus |
| Evenements | `internal/eventbus/` | Bus d'evenements publication/abonnement |
| Couts | `internal/cost/` | Suivi des couts LLM avec limites quotidiennes |
| Limitation de debit | `internal/ratelimit/` | Limiteurs de debit par chat et globaux |
| Frontend | `ui/` | SPA Vue 3 + Naive UI + UnoCSS |

## Ordre de demarrage

La sequence de demarrage est strictement ordonnee pour satisfaire les dependances :

```
1. Analyser les arguments CLI, resoudre les chemins XDG, creer les repertoires
2. Gerer les sous-commandes : init, --version, --doctor (sortie anticipee)
3. Charger la configuration : defauts → TOML → env → trousseau
4. Creer le logger (le mode console redirige vers un fichier)
5. Ouvrir SQLite, executer les migrations
6. Initialiser le catalogue i18n (apres les migrations, avant les competences)
7. Initialiser l'utilisateur administrateur (avant le remplissage)
8. BackfillUserIDs (associer les donnees legacy aux utilisateurs)
9. Creer le magasin de configuration, charger les surcharges BDD
10. Verifier le portail du mode configuration (pas de LLM + pas d'assistant = configuration uniquement)
11. Valider la configuration
12. Creer le service d'authentification
13. Initialiser les instances de canaux
14. Creer le fournisseur d'embeddings ONNX (optionnel)
15. Construire la chaine de fournisseurs LLM (Claude → retry → repli → cache → routage)
16. Enregistrer toutes les competences (inconditionnellement — controle par capacite)
17. Creer l'assistant
18. Connecter le bus d'evenements (rechargement config, metriques, couts, notifications)
19. Rejouer les surcharges de configuration BDD (rechargement a chaud pour les identifiants definis via le tableau de bord)
20. Creer le gestionnaire de canaux, le planificateur, le worker
21. Demarrer le planificateur, le worker, la boucle de l'assistant
22. Demarrer le serveur du tableau de bord
23. Demarrer tous les canaux
24. Bloquer sur le signal d'arret
```

## Arret gracieux (7 phases)

```
1. Arreter tous les canaux (cesser d'accepter les nouveaux messages)
2. Attendre les goroutines d'arriere-plan de l'assistant
3. Attendre le remplissage des embeddings
4. Fermer le fournisseur ONNX
5. Arreter le bus d'evenements (attendre les gestionnaires asynchrones)
6. Attendre le planificateur/worker/tableau de bord (delai de 10s)
7. Fermer la connexion SQLite (en dernier)
```

## Flux de messages

Lorsqu'un utilisateur envoie un message, voici le chemin d'execution complet :

```
L'utilisateur tape "remember that I love Go"
    │
    ▼
Canal (Telegram/WebChat/Console)
    │ construit un IncomingMessage avec les champs specifiques a la plateforme
    │ definit le masque de bits ChannelCaps (streaming, markdown, etc.)
    ▼
UserResolver (Telegram/Console uniquement)
    │ mappe l'identite de la plateforme → UUID iulita
    │ enregistre automatiquement les nouveaux utilisateurs si autorise
    ▼
Gestionnaire de canaux
    │ route vers Assistant.HandleMessage
    ▼
Assistant — Phase 1 : Configuration du contexte
    │ delai, role utilisateur, locale, capacites → contexte
    │ verifier l'approbation en attente → executer si approuvee
    ▼
Assistant — Phase 2 : Enrichissement
    │ sauvegarder le message en BDD
    │ arriere-plan : TechFactAnalyzer (cyrillique/latin, longueur du message)
    │ envoyer l'evenement de statut "traitement"
    ▼
Assistant — Phase 3 : Historique et compression
    │ charger les 50 derniers messages
    │ si tokens > 80% de la fenetre de contexte → compresser la moitie ancienne
    ▼
Assistant — Phase 4 : Donnees de contexte
    │ charger la directive, les faits recents, les observations pertinentes
    │ recherche hybride : FTS5 + vecteurs ONNX + reordonnancement MMR
    │ charger les faits techniques (profil utilisateur)
    │ resoudre le fuseau horaire
    ▼
Assistant — Phase 5 : Construction du prompt
    │ prompt statique = base + prompts systeme des competences (mis en cache par Claude)
    │ prompt dynamique = heure + directives + profil + faits + observations + langue
    ▼
Assistant — Phase 6 : Detection de l'outil force
    │ mot-cle "remember" → ForceTool = "remember"
    ▼
Assistant — Phase 7 : Boucle agentique (max 10 iterations)
    │ Appeler le LLM (streaming si pas d'outils, sinon standard)
    │ En cas de depassement de contexte → forcer la compression → reessayer une fois
    │ Si appels d'outils :
    │   ├── verifier le niveau d'approbation
    │   ├── executer la competence
    │   ├── accumuler dans ToolExchanges
    │   └── iteration suivante
    │ Si pas d'appels d'outils → retourner la reponse
    ▼
Execution de la competence (ex. RememberSkill)
    │ verification des doublons via recherche FTS
    │ sauvegarde en SQLite → declencheur FTS active
    │ arriere-plan : embedding ONNX → fact_vectors
    ▼
Reponse renvoyee a travers le canal vers l'utilisateur
```

## Interfaces principales

### Provider (LLM)

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

### Skill

```go
type Skill interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage  // nil pour les competences texte uniquement
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

Interfaces optionnelles : `CapabilityAware`, `ConfigReloadable`, `ApprovalDeclarer`.

### Channel

```go
type InputChannel interface {
    Start(ctx context.Context, handler MessageHandler) error
}

type MessageHandler func(ctx context.Context, msg IncomingMessage) (string, error)

type StreamingSender interface {
    MessageSender
    StartStream(ctx context.Context, chatID string, replyTo int) (editFn, doneFn func(string), err error)
}
```

### Storage

```go
type Repository interface {
    // Messages
    SaveMessage(ctx, msg) error
    GetHistory(ctx, chatID, limit) ([]ChatMessage, error)

    // Memoire
    SaveFact(ctx, fact) error
    SearchFacts(ctx, chatID, query, limit) ([]Fact, error)
    SearchFactsByUser(ctx, userID, query, limit) ([]Fact, error)
    SearchFactsHybridByUser(ctx, userID, query, queryVec, limit) ([]Fact, error)

    // Taches
    CreateTask(ctx, task) error
    ClaimTask(ctx, workerID, capabilities) (*Task, error)

    // ... 60+ methodes au total
}
```

## Bus d'evenements

Le bus d'evenements (`internal/eventbus/`) implemente un patron publication/abonnement type. Les evenements circulent entre les composants sans couplage direct :

| Evenement | Emetteur | Abonnes |
|-----------|----------|---------|
| `MessageReceived` | Assistant | Metriques, hub WebSocket |
| `ResponseSent` | Assistant | Metriques, hub WebSocket |
| `LLMUsage` | Assistant | Metriques, suivi des couts |
| `SkillExecuted` | Assistant | Metriques |
| `TaskCompleted` | Worker | Hub WebSocket |
| `TaskFailed` | Worker | Hub WebSocket |
| `FactSaved` | Storage | Hub WebSocket |
| `InsightCreated` | Storage | Hub WebSocket |
| `ConfigChanged` | Magasin de configuration | Gestionnaire de rechargement → competences |

## Chaine de fournisseurs LLM

Les fournisseurs sont composes en decorateurs :

```
Claude Provider
    └→ Retry Provider (3 tentatives, backoff exponentiel, 429/5xx)
        └→ Fallback Provider (Claude → OpenAI)
            └→ Caching Provider (cle SHA-256, TTL 60min)
                └→ Routing Provider (dispatch base sur RouteHint)
                    └→ Classifying Provider (classificateur Ollama → selection de route)
```

Pour les fournisseurs qui ne supportent pas nativement l'appel d'outils (Ollama, OpenAI), le wrapper `XMLToolProvider` injecte les definitions d'outils en XML dans le prompt systeme et analyse les appels d'outils XML dans la reponse.

## Portee des donnees

Toutes les donnees sont scopees par `user_id` pour le partage inter-canal :

```
User (UUID iulita)
    ├── user_channels (liaison Telegram, liaison WebChat, ...)
    ├── chat_messages (de tous les canaux)
    ├── facts (partages entre les canaux)
    ├── insights (partages entre les canaux)
    ├── directives (par utilisateur)
    ├── tech_facts (profil comportemental)
    ├── reminders
    └── todo_items
```

Un utilisateur discutant sur Telegram peut rappeler des faits qu'il a stockes via le Console TUI, car les deux canaux se resolvent vers le meme `user_id`.

## Structure du projet

```
cmd/iulita/              # point d'entree, injection de dependances, arret gracieux
internal/
  assistant/             # orchestrateur (boucle LLM, memoire, compression, approbations)
  channel/
    console/             # TUI bubbletea
    telegram/            # bot Telegram
    webchat/             # chat web WebSocket
  channelmgr/            # gestionnaire de cycle de vie des canaux
  config/                # configuration TOML + env + trousseau, assistant de configuration
  domain/                # modeles de domaine
  auth/                  # authentification JWT + bcrypt
  i18n/                  # internationalisation (6 langues, catalogues TOML)
  llm/                   # fournisseurs LLM (Claude, Ollama, OpenAI, ONNX)
  scheduler/             # file de taches (planificateur + worker)
  skill/                 # implementations des competences
  skillmgr/              # gestionnaire de competences externes (ClawhHub, URL, local)
  storage/sqlite/        # depot SQLite, FTS5, vecteurs, migrations
  dashboard/             # API REST GoFiber + SPA Vue
  web/                   # recherche web (Brave, DuckDuckGo, protection SSRF)
  memory/                # clustering TF-IDF, export/import
  eventbus/              # bus d'evenements publication/abonnement
  cost/                  # suivi des couts LLM
  metrics/               # metriques Prometheus
  ratelimit/             # limitation de debit
  notify/                # notifications push
ui/                      # frontend SPA Vue 3 + Naive UI + UnoCSS
skills/                  # fichiers de competences texte (Markdown)
docs/                    # documentation
```
