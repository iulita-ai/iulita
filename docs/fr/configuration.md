# Configuration

Iulita utilise un systeme de configuration en couches qui supporte l'installation locale sans configuration tout en permettant une personnalisation complete pour les deploiements avances.

## Couches de configuration

La configuration est chargee dans l'ordre, les couches ulterieures surchargeant les precedentes :

```
1. Valeurs par defaut compilees (toujours presentes)
2. Fichier TOML (~/.config/iulita/config.toml, optionnel)
3. Variables d'environnement (prefixe IULITA_*)
4. Secrets du trousseau (macOS Keychain, Linux SecretService)
5. Surcharges BDD (table config_overrides, modifiables a l'execution)
```

### Couche 1 : Valeurs par defaut compilees

`DefaultConfig()` fournit une configuration fonctionnelle sans fichiers externes. Tous les identifiants de modele, delais d'attente, parametres de memoire et drapeaux de fonctionnalites ont des valeurs par defaut raisonnables. Le systeme fonctionne directement avec uniquement une cle API.

### Couche 2 : Fichier TOML

Optionnel. Situe a `~/.config/iulita/config.toml` (ou `$IULITA_HOME/config.toml`).

Le fichier TOML est **ignore** si :
- Aucun fichier n'existe au chemin de configuration
- Le fichier sentinelle `db_managed` existe (mode assistant web)

Voir `config.toml.example` pour la reference complete.

### Couche 3 : Variables d'environnement

Tous les parametres peuvent etre surcharges via les variables d'environnement `IULITA_*` :

```
IULITA_CLAUDE_API_KEY      → claude.api_key
IULITA_TELEGRAM_TOKEN      → telegram.token
IULITA_CLAUDE_MODEL        → claude.model
IULITA_STORAGE_PATH        → storage.path
IULITA_SERVER_ADDRESS      → server.address
IULITA_PROXY_URL           → proxy.url
```

**Regle de correspondance** : supprimer le prefixe `IULITA_`, minuscules, remplacer `_` par `.`.

### Couche 4 : Secrets du trousseau

Les secrets sont stockes de maniere securisee dans le trousseau du systeme d'exploitation :

| Secret | Variable d'environnement | Compte trousseau |
|--------|--------------------------|------------------|
| Cle API Claude | `IULITA_CLAUDE_API_KEY` | `claude-api-key` |
| Jeton Telegram | `IULITA_TELEGRAM_TOKEN` | `telegram-token` |
| Secret JWT | `IULITA_JWT_SECRET` | `jwt-secret` |
| Cle de chiffrement config | `IULITA_CONFIG_KEY` | `config-encryption-key` |

**Chaine de repli** pour chaque secret : variable d'environnement → trousseau → fichier (pour la cle de chiffrement uniquement) → generation automatique (pour JWT uniquement).

Le trousseau utilise `zalando/go-keyring` :
- **macOS** : Keychain
- **Linux** : SecretService (GNOME Keyring, KDE Wallet)
- **Repli** : fichier chiffre a `~/.config/iulita/encryption.key`

### Couche 5 : Surcharges BDD (Config Store)

Configuration modifiable a l'execution stockee dans la table SQLite `config_overrides`. Geree via :
- L'editeur de configuration du tableau de bord
- L'outil `skills` par chat (action `set_config`)
- L'assistant de configuration web

**Fonctionnalites :**
- Chiffrement AES-256-GCM pour les valeurs secretes
- Rechargement a chaud immediat via le bus d'evenements
- Journalisation d'audit (qui a change quoi, quand)
- Protection des cles immuables necessitant un redemarrage

## Chemins conformes XDG

| Plateforme | Configuration | Donnees | Cache | Etat |
|------------|---------------|---------|-------|------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

**Surcharge** : definir `IULITA_HOME` pour utiliser une racine personnalisee avec des sous-repertoires `data/`, `cache/`, `state/`.

### Chemins derives

| Chemin | Emplacement |
|--------|-------------|
| Fichier de configuration | `{ConfigDir}/config.toml` |
| Base de donnees | `{DataDir}/iulita.db` |
| Modeles ONNX | `{DataDir}/models/` |
| Competences | `{DataDir}/skills/` |
| Competences externes | `{DataDir}/external-skills/` |
| Fichier journal | `{StateDir}/iulita.log` |
| Cle de chiffrement | `{ConfigDir}/encryption.key` |

## Sections de configuration

### App

| Cle | Defaut | Description |
|-----|--------|-------------|
| `app.system_prompt` | (integre) | Prompt systeme de base de l'assistant |
| `app.context_window` | 200000 | Budget de jetons pour le contexte |
| `app.request_timeout` | 120s | Delai par message |

### Claude (LLM principal)

| Cle | Defaut | Description |
|-----|--------|-------------|
| `claude.api_key` | — | Cle API Anthropic (requise) |
| `claude.model` | `claude-sonnet-4-5-20250929` | Identifiant du modele |
| `claude.max_tokens` | 8192 | Jetons de sortie maximum |
| `claude.base_url` | — | Surcharger l'URL de base de l'API |
| `claude.thinking` | 0 | Budget de reflexion etendue (0 = desactive) |

### Ollama (LLM local)

| Cle | Defaut | Description |
|-----|--------|-------------|
| `ollama.url` | `http://localhost:11434` | URL du serveur Ollama |
| `ollama.model` | `llama3` | Nom du modele |

### OpenAI (compatibilite)

| Cle | Defaut | Description |
|-----|--------|-------------|
| `openai.api_key` | — | Cle API |
| `openai.model` | `gpt-4` | Identifiant du modele |
| `openai.base_url` | `https://api.openai.com/v1` | URL de base de l'API |

### Telegram

| Cle | Defaut | Description |
|-----|--------|-------------|
| `telegram.token` | — | Jeton du bot (rechargeable a chaud) |
| `telegram.allowed_ids` | `[]` | Liste blanche d'IDs utilisateur (vide = tous) |
| `telegram.debounce_window` | 2s | Fenetre de regroupement des messages |

### Stockage

| Cle | Defaut | Description |
|-----|--------|-------------|
| `storage.path` | `{DataDir}/iulita.db` | Chemin de la base SQLite (redemarrage uniquement) |

### Serveur

| Cle | Defaut | Description |
|-----|--------|-------------|
| `server.enabled` | true | Activer le serveur du tableau de bord |
| `server.address` | `:8080` | Adresse d'ecoute (redemarrage uniquement) |

### Authentification

| Cle | Defaut | Description |
|-----|--------|-------------|
| `auth.jwt_secret` | (genere automatiquement) | Cle de signature JWT |
| `auth.token_ttl` | 24h | TTL du jeton d'acces |
| `auth.refresh_ttl` | 7d | TTL du jeton de rafraichissement |

### Proxy

| Cle | Defaut | Description |
|-----|--------|-------------|
| `proxy.url` | — | Proxy HTTP/SOCKS5 (redemarrage uniquement) |

### Memoire

| Cle | Defaut | Description |
|-----|--------|-------------|
| `skills.memory.half_life_days` | 30 | Demi-vie de la decroissance temporelle |
| `skills.memory.mmr_lambda` | 0 | Diversite MMR (0.7 recommande) |
| `skills.memory.vector_weight` | 0 | Ponderation de la recherche hybride |
| `skills.memory.triggers` | `[]` | Mots-cles declencheurs de memorisation |

### Observations

| Cle | Defaut | Description |
|-----|--------|-------------|
| `skills.insights.min_facts` | 20 | Minimum de faits pour la generation |
| `skills.insights.max_pairs` | 6 | Max de paires de clusters par execution |
| `skills.insights.ttl` | 720h | Expiration des observations (30 jours) |
| `skills.insights.interval` | 24h | Frequence de generation |
| `skills.insights.quality_threshold` | 0 | Score de qualite minimum |

### Embeddings

| Cle | Defaut | Description |
|-----|--------|-------------|
| `embedding.enabled` | true | Activer les embeddings ONNX |
| `embedding.model` | `KnightsAnalytics/all-MiniLM-L6-v2` | Nom du modele |

### Planificateur

| Cle | Defaut | Description |
|-----|--------|-------------|
| `scheduler.enabled` | true | Activer le planificateur de taches |
| `scheduler.worker_token` | — | Jeton Bearer pour les workers distants |

### Couts

| Cle | Defaut | Description |
|-----|--------|-------------|
| `cost.daily_limit_usd` | 0 | Plafond de cout quotidien (0 = illimite) |

### Cache

| Cle | Defaut | Description |
|-----|--------|-------------|
| `cache.enabled` | false | Activer le cache de reponses |
| `cache.ttl` | 60m | TTL du cache |
| `cache.max_items` | 1000 | Nombre max de reponses en cache |

### Metriques

| Cle | Defaut | Description |
|-----|--------|-------------|
| `metrics.enabled` | false | Activer les metriques Prometheus |
| `metrics.address` | `:9090` | Adresse du serveur de metriques |

## Assistant de configuration

### Assistant CLI (`iulita init`)

Configuration interactive qui guide a travers :
1. Selection du fournisseur LLM (Claude/OpenAI/Ollama, selection multiple)
2. Saisie de la cle API (stockee dans le trousseau)
3. Integrations optionnelles (Telegram, proxy, embedding)
4. Selection du modele (recuperation dynamique depuis le fournisseur)

Les secrets vont dans le trousseau ; les non-secrets dans `config.toml`.

### Assistant de configuration web (Docker)

Pour les deploiements Docker sans acces terminal :

1. Le serveur demarre en **mode configuration** lorsqu'aucun LLM n'est configure et que l'assistant n'est pas termine
2. Mode tableau de bord uniquement (pas de competences, planificateur ou canaux)
3. Assistant en 5 etapes : Bienvenue/Import → Fournisseur → Configuration → Fonctionnalites → Termine
4. Support d'import TOML (coller une configuration existante)
5. Cree le fichier sentinelle `db_managed` (desactive le chargement TOML)
6. Definit `_system.wizard_completed` dans config_overrides

## Rechargement a chaud

Ces parametres peuvent changer a l'execution sans redemarrage :

| Parametre | Declencheur | Mecanisme |
|-----------|-------------|-----------|
| Modele/jetons/cle Claude | Editeur de config du tableau de bord | `UpdateModel()`/`UpdateMaxTokens()`/`UpdateAPIKey()` |
| Jeton Telegram | Editeur de config du tableau de bord | `channelmgr.UpdateConfigToken()` → redemarrage de l'instance |
| Activation/desactivation de competence | Tableau de bord ou chat | `registry.EnableSkill()`/`DisableSkill()` |
| Config de competence (cles API) | Editeur de config du tableau de bord | `ConfigReloadable.OnConfigChanged()` |
| Prompt systeme | Editeur de config du tableau de bord | `asst.SetSystemPrompt()` |
| Budget de reflexion | Editeur de config du tableau de bord | `asst.SetThinkingBudget()` |

### Parametres necessitant un redemarrage

Ces parametres necessitent un redemarrage complet :
- `storage.path`
- `server.address`
- `proxy.url`
- `security.config_key_env`

## Chiffrement AES-256-GCM

Les valeurs de configuration secretes en BDD sont chiffrees :

1. **Source de la cle** : env `IULITA_CONFIG_KEY` → trousseau → fichier genere automatiquement
2. **Algorithme** : AES-256-GCM (chiffrement authentifie)
3. **Format** : `base64(nonce-12-octets ‖ texte-chiffre)`
4. **Chiffrement automatique** : les cles declarees comme `secret_keys` dans SKILL.md sont toujours chiffrees
5. **Jamais divulguees** : l'API du tableau de bord retourne des valeurs vides pour les cles chiffrees
