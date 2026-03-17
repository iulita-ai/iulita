# Competences

Les competences sont des outils que l'assistant peut invoquer pendant les conversations. Chaque competence expose un ou plusieurs outils au LLM avec un nom, une description et un schema d'entree JSON.

## Interface Skill

```go
type Skill interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage  // nil pour les competences texte uniquement
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

**Interfaces optionnelles :**
- `CapabilityAware` — `RequiredCapabilities() []string` : competence exclue si une capacite est absente
- `ConfigReloadable` — `OnConfigChanged(key, value string)` : appele lors d'un changement de configuration a l'execution
- `ApprovalDeclarer` — `ApprovalLevel() ApprovalLevel` : exigence d'approbation

## Niveaux d'approbation

| Niveau | Comportement | Utilise par |
|--------|-------------|-------------|
| `ApprovalAuto` | Executer immediatement (par defaut) | La plupart des competences |
| `ApprovalPrompt` | L'utilisateur doit confirmer dans le chat | Executeur Docker |
| `ApprovalManual` | L'administrateur doit confirmer | Execution shell |

Le flux d'approbation est **non bloquant** : la competence retourne « en attente d'approbation » au LLM. Le message suivant de l'utilisateur est verifie contre le vocabulaire d'approbation sensible a la locale (oui/non en 6 langues).

## Competences integrees

### Groupe Memoire

| Outil | Entree | Description |
|-------|--------|-------------|
| `remember` | `content` | Stocker un fait. Verifie les doublons via FTS. Declenche l'embedding automatique. |
| `recall` | `query`, `limit` | Rechercher des faits via FTS5. Applique la decroissance temporelle + reordonnancement MMR. Renforce les faits accedes. |
| `forget` | `id` | Supprimer un fait par ID. Cascade vers les tables FTS et vectorielles. |

Voir [Memoire et observations](memory-and-insights.md) pour les details complets.

### Groupe Observations

| Outil | Entree | Description |
|-------|--------|-------------|
| `list_insights` | `limit` | Lister les observations recentes avec les scores de qualite |
| `dismiss_insight` | `id` | Supprimer une observation |
| `promote_insight` | `id` | Prolonger ou supprimer l'expiration d'une observation |

### Recherche web et recuperation

| Outil | Entree | Description |
|-------|--------|-------------|
| `websearch` | `query`, `count` | Recherche web via l'API Brave + repli DuckDuckGo. 1-10 resultats. |
| `webfetch` | `url` | Recuperer et resumer une page web. Utilise go-readability pour l'extraction de contenu. Protege contre SSRF. |

La chaine de recherche web est `Brave → DuckDuckGo` via `FallbackSearcher`. DuckDuckGo ne necessite pas de cle API, donc la recherche web fonctionne toujours.

### Directives

| Outil | Entree | Description |
|-------|--------|-------------|
| `directives` | `action`, `content` | Gerer les instructions personnalisees persistantes (set/get/clear). Chargees dans le prompt systeme. |

### Rappels

| Outil | Entree | Description |
|-------|--------|-------------|
| `reminders` | `action`, `title`, `due_at`, `timezone`, `id` | Creer/lister/supprimer des rappels temporels. Livres par le planificateur. |

### Date/Heure

| Outil | Entree | Description |
|-------|--------|-------------|
| `datetime` | `timezone` | Date, heure, nom du fuseau horaire, timestamp Unix actuels. Zero dependance externe. |

### Meteo

| Outil | Entree | Description |
|-------|--------|-------------|
| `weather` | `location`, `days` | Previsions meteo (1-16 jours). Resolution interactive de la localisation. |

**Chaine de backends** : Open-Meteo (principal, gratuit) → wttr.in (repli, gratuit) → OpenWeatherMap (optionnel, necessite une cle).

Fonctionnalites :
- Resolution interactive de la localisation via les invites du canal (clavier en ligne Telegram, boutons WebChat, options numerotees Console)
- Support du geocodage cyrillique
- Previsions multi-jours avec descriptions des codes meteo OMM (traduits en 6 langues)
- La sortie s'adapte aux capacites du canal (markdown vs texte brut)

### Geolocalisation

| Outil | Entree | Description |
|-------|--------|-------------|
| `geolocation` | `ip` | Geolocalisation basee sur l'IP. Detection automatique de l'IP publique. |

Chaine de fournisseurs : ipinfo.io (avec cle) → ip-api.com → ipapi.co. Valide que l'IP est publique (bloque RFC1918, loopback, etc.).

### Taux de change

| Outil | Entree | Description |
|-------|--------|-------------|
| `exchange_rate` | `from`, `to`, `amount` | Taux de change. 160+ devises. Aucune cle API requise. |

### Execution shell

| Outil | Entree | Description |
|-------|--------|-------------|
| `shell_exec` | `command`, `args` | Execution de commandes shell en bac a sable. **ApprovalManual** (confirmation administrateur requise). |

**Mesures de securite :**
- Liste blanche uniquement : seuls les binaires dans `AllowedBins` peuvent s'executer
- `ForbiddenPaths` verifies dans les arguments
- Rejette les traversees de chemin `..`
- Sortie maximale 16 Ko
- Repertoire d'execution par defaut : `os.TempDir()`

### Delegation

| Outil | Entree | Description |
|-------|--------|-------------|
| `delegate` | `prompt`, `provider` | Router un sous-prompt vers un fournisseur LLM secondaire (ex. Ollama pour les taches economiques). |

### Lecteur PDF

| Outil | Entree | Description |
|-------|--------|-------------|
| `pdf_read` | `url` | Recuperer et lire des documents PDF. Valide les octets magiques `%PDF-`. |

### Changer de langue

| Outil | Entree | Description |
|-------|--------|-------------|
| `set_language` | `language` | Changer la langue de l'interface. Accepte les codes BCP-47 ou les noms de langues (English/Russian). |

Met a jour `user_channels.locale` dans la base de donnees. Le message de confirmation est dans la **nouvelle** langue.

### Google Workspace

| Outil | Description |
|-------|-------------|
| `google_auth` | Lancement du flux OAuth2, liste des comptes |
| `google_calendar` | Lister/creer/modifier/supprimer des evenements, disponibilite |
| `google_contacts` | Lister les contacts, requetes d'anniversaires |
| `google_mail` | Lister/lire/rechercher Gmail (lecture seule) |
| `google_tasks` | CRUD sur Google Tasks |

Necessite la configuration OAuth2 via le tableau de bord. Support multi-comptes.

### Todoist

| Outil | Entree | Description |
|-------|--------|-------------|
| `todoist` | `action`, ... | Gestion complete des taches Todoist. 34 actions. |

**Actions** : creer, lister, obtenir, modifier, completer, rouvrir, supprimer, deplacer, ajout rapide, filtrer, historique des completes. Supporte les priorites (P1-P4), les echeances, les dates/heures d'echeance, les recurrences, les etiquettes, les projets, les sections, les sous-taches, les commentaires.

Utilise l'API unifiee v1 (`api.todoist.com/api/v1`). Authentification par jeton API.

### Taches unifiees

| Outil | Entree | Description |
|-------|--------|-------------|
| `tasks` | `action`, `provider`, ... | Agregation Todoist + Google Tasks + Craft Tasks. |

**Actions** : `overview` (tous les fournisseurs), `list`, `create`, `complete`, `provider` (passthrough).

### Craft

| Outil | Description |
|-------|-------------|
| `craft_read` | Lire des documents Craft |
| `craft_write` | Ecrire des documents Craft |
| `craft_tasks` | Gerer les taches Craft |
| `craft_search` | Rechercher des documents Craft |

### Orchestration multi-agent

| Outil | Entree | Description |
|-------|--------|-------------|
| `orchestrate` | `agents[]`, `timeout`, `max_tokens` | Lancer plusieurs sous-agents specialises en parallele. |

**Types d'agents** : `researcher`, `analyst`, `planner`, `coder`, `summarizer`, `generic` — chacun avec un prompt systeme specialise et un sous-ensemble d'outils.

Les sous-agents s'executent en parallele via `errgroup`, partagent un budget de tokens atomique et ne peuvent pas generer d'autres sous-agents (profondeur max = 1). Les competences necessitant une approbation (ApprovalManual, ApprovalPrompt) sont filtrees des listes d'outils.

Voir [Orchestration multi-agent](multi-agent.md) pour les details complets.

### Gestion des competences

| Outil | Entree | Description |
|-------|--------|-------------|
| `skills` | `action`, ... | Lister/activer/desactiver/obtenir_config/definir_config les competences via le chat. Reservee aux administrateurs pour les modifications. |

## Competences texte (injection de prompt systeme)

Les competences peuvent etre purement une injection de prompt systeme — pas de methode `Execute`, pas de definition d'outil pour le LLM.

### Format SKILL.md

```yaml
---
name: my-skill
description: Ce que fait cette competence
capabilities: [optional-cap]
config_keys: [skills.my-skill.setting]
secret_keys: [skills.my-skill.api_key]
force_triggers: [keyword1, keyword2]
---

Instructions Markdown injectees dans le prompt systeme.
```

- Les competences avec `InputSchema() == nil` contribuent uniquement a `staticSystemPrompt()`
- Le corps Markdown devient partie des instructions du LLM
- `force_triggers` force des appels d'outils specifiques lorsque les mots-cles correspondent au message de l'utilisateur

### Chemins de chargement

1. **Embarques dans les paquets Go** : `//go:embed SKILL.md` + `LoadManifestFromFS()`
2. **Repertoire externe** : `LoadExternalManifests(dir)` depuis `~/.local/share/iulita/skills/`
3. **Externes installees** : via le marketplace ClawhHub ou URL

## Gestionnaire de competences (competences externes)

### Marketplace ClawhHub

Installez des competences communautaires depuis [ClawhHub](https://clawhub.ai) :

```
# Via le tableau de bord : Competences → Externes → Rechercher
# Via le chat : installer depuis une URL
```

L'API du marketplace (`clawhub.ai/api/v1`) supporte :
- `Search(query)` — resultats classes par pertinence BM25
- `Resolve(slug)` — obtenir l'URL de telechargement et la somme de controle
- `Download()` — telecharger l'archive (max 50 Mo)

### Flux d'installation

1. Verifier la limite `MaxInstalled`
2. Resoudre depuis la source (ClawhHub, URL ou repertoire local)
3. Valider le slug contre `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`
4. Telecharger et verifier la somme de controle SHA-256
5. Analyser `SKILL.md` avec le frontmatter etendu
6. Valider le niveau d'isolation contre la configuration
7. Scanner les motifs d'injection de prompt
8. Deplacement atomique vers le repertoire d'installation
9. Enregistrer dans le registre de competences

### Niveaux d'isolation

| Niveau | Comportement | Approbation |
|--------|-------------|-------------|
| `text_only` | Injection de prompt systeme uniquement | Auto |
| `shell` | Execution shell via `ShellExecutor` | Manuelle |
| `docker` | Execution en conteneur Docker | Invite |
| `wasm` | Runtime WebAssembly | Auto |

**Chaine de repli** : si une competence necessite shell mais que shell_exec est desactive, elle se replie vers `webfetchProxySkill` (extrait les URLs du prompt et les recupere), puis vers `text_only`.

### Securite

- La validation du slug empeche les traversees de chemin
- Verification de la somme de controle pour les telechargements distants
- Validation du niveau d'isolation contre la configuration (`AllowShell`, `AllowDocker`, `AllowWASM`)
- Detection de fichiers de code : rejette les competences avec des fichiers `.py`/`.js`/`.go`/etc. sauf si correctement isolees
- Scan d'injection de prompt : avertit sur les motifs suspects dans le corps de la competence

## Rechargement a chaud des competences

Les competences supportent les changements de configuration a l'execution sans redemarrage :

1. Les competences appellent `RegisterKey()` au demarrage pour declarer leurs cles de configuration
2. L'editeur de configuration du tableau de bord appelle `Store.Set()` qui publie `ConfigChanged`
3. Le bus d'evenements dispatche vers `registry.DispatchConfigChanged()`
4. Les competences implementant `ConfigReloadable` recoivent la nouvelle valeur

**Regle critique** : les competences DOIVENT etre enregistrees inconditionnellement (pas a l'interieur d'un `if apiKey != ""`). Utilisez le controle par capacite a la place : `AddCapability("web")` lorsque la cle API est presente, `RemoveCapability("web")` lorsqu'elle est retiree.

## Declencheurs forces

Les competences peuvent declarer des mots-cles qui forcent l'invocation de l'outil :

```yaml
force_triggers: [weather, погода, météo]
```

Lorsque le message de l'utilisateur contient un mot-cle declencheur (correspondance de sous-chaine insensible a la casse), `ForceTool` est defini sur la requete LLM pour l'iteration 0. Cela garantit que le LLM appelle toujours l'outil plutot que de repondre a partir de ses donnees d'entrainement.

Les declencheurs de memoire (ex. "remember", "zapomni") sont configures separement et forcent l'outil `remember`.
