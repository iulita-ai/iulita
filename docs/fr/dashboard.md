# Tableau de bord

Le tableau de bord est une API REST GoFiber servant une SPA Vue 3 embarquee. Il fournit une interface web pour gerer tous les aspects d'Iulita.

## Architecture

```
GoFiber Server
    ├── /api/*          API REST (authentifiee par JWT)
    ├── /ws             Hub WebSocket (mises a jour en temps reel)
    ├── /ws/chat        Canal WebChat (endpoint separe)
    └── /*              SPA Vue 3 (embarquee, routage cote client)
```

La SPA Vue est embarquee dans le binaire Go via `//go:embed dist/*` et servie avec un repli `index.html` pour tous les chemins inconnus.

## Authentification

| Endpoint | Authentification | Description |
|----------|------------------|-------------|
| `POST /api/auth/login` | Public | Verification bcrypt des identifiants, retourne jetons d'acces + rafraichissement |
| `POST /api/auth/refresh` | Public | Valider le jeton de rafraichissement, retourner un nouveau jeton d'acces |
| `POST /api/auth/change-password` | JWT | Changer son propre mot de passe |
| `GET /api/auth/me` | JWT | Profil utilisateur actuel |
| `PATCH /api/auth/locale` | JWT | Mettre a jour la locale pour tous les canaux |

**Details JWT :**
- Algorithme : HMAC-SHA256
- TTL du jeton d'acces : 24 heures
- TTL du jeton de rafraichissement : 7 jours
- Claims : `user_id`, `username`, `role`
- Secret : genere automatiquement si non configure

## API REST

### Endpoints publics

| Methode | Chemin | Description |
|---------|--------|-------------|
| GET | `/api/system` | Info systeme, version, uptime, statut de l'assistant |

### Endpoints utilisateur (JWT requis)

| Methode | Chemin | Description |
|---------|--------|-------------|
| GET | `/api/stats` | Compteurs de messages, faits, observations, rappels |
| GET | `/api/chats` | Lister tous les IDs de chat avec compteurs de messages |
| GET | `/api/facts` | Lister/rechercher des faits (par chat_id, user_id, query) |
| PUT | `/api/facts/:id` | Mettre a jour le contenu d'un fait |
| DELETE | `/api/facts/:id` | Supprimer un fait |
| GET | `/api/facts/search` | Recherche FTS de faits |
| GET | `/api/insights` | Lister les observations |
| GET | `/api/reminders` | Lister les rappels |
| GET | `/api/directives` | Obtenir la directive d'un chat |
| GET | `/api/messages` | Historique de chat avec pagination |
| GET | `/api/skills` | Lister toutes les competences avec statut active/config |
| PUT | `/api/skills/:name/toggle` | Activer/desactiver une competence a l'execution |
| GET | `/api/skills/:name/config` | Schema de config de competence + valeurs actuelles |
| PUT | `/api/skills/:name/config/:key` | Definir une cle de config de competence (chiffrement auto des secrets) |
| GET | `/api/techfacts` | Profil comportemental groupe par categorie |
| GET | `/api/usage/summary` | Utilisation de jetons + estimation de cout |
| GET | `/api/schedulers` | Statut des taches planifiees |
| POST | `/api/schedulers/:name/trigger` | Declenchement manuel d'une tache |

### Endpoints des taches (JWT requis)

| Methode | Chemin | Description |
|---------|--------|-------------|
| GET | `/api/todos/providers` | Lister les fournisseurs de taches |
| GET | `/api/todos/today` | Taches du jour |
| GET | `/api/todos/overdue` | Taches en retard |
| GET | `/api/todos/upcoming` | Taches a venir (7 jours par defaut) |
| GET | `/api/todos/all` | Toutes les taches incompletes |
| GET | `/api/todos/counts` | Compteurs aujourd'hui + en retard |
| POST | `/api/todos/` | Creer une tache |
| POST | `/api/todos/sync` | Declencher la synchronisation manuelle des taches |
| POST | `/api/todos/:id/complete` | Completer une tache |
| DELETE | `/api/todos/:id` | Supprimer une tache integree |

### Endpoints Google Workspace (JWT requis)

| Methode | Chemin | Description |
|---------|--------|-------------|
| GET | `/api/google/status` | Statut des comptes |
| POST | `/api/google/upload-credentials` | Telecharger le fichier d'identifiants OAuth |
| GET | `/api/google/auth` | Demarrer le flux OAuth2 |
| GET | `/api/google/callback` | Callback OAuth2 |
| GET | `/api/google/accounts` | Lister les comptes |
| DELETE | `/api/google/accounts/:id` | Supprimer un compte |
| PUT | `/api/google/accounts/:id` | Mettre a jour un compte |

### Endpoints administrateur (role admin requis)

| Methode | Chemin | Description |
|---------|--------|-------------|
| GET/PUT/DELETE | `/api/config/*` | Surcharges de config, schema, debogage |
| GET/POST/PUT/DELETE | `/api/users/*` | CRUD utilisateur + liaisons de canaux |
| GET/POST/PUT/DELETE | `/api/channels/*` | CRUD d'instances de canaux |
| GET/POST/PUT/DELETE | `/api/agent-jobs/*` | CRUD des taches planifiees |
| GET/POST/DELETE | `/api/skills/external/*` | Gestion des competences externes |
| GET/POST | `/api/wizard/*` | Assistant de configuration |
| PUT | `/api/todos/default-provider` | Definir le fournisseur de taches par defaut |

### Endpoints worker (jeton Bearer)

| Methode | Chemin | Description |
|---------|--------|-------------|
| GET | `/api/tasks/` | Lister les taches du planificateur |
| GET | `/api/tasks/counts` | Compteurs par statut |
| POST | `/api/tasks/claim` | Reclamer une tache (worker distant) |
| POST | `/api/tasks/:id/start` | Marquer une tache comme en cours |
| POST | `/api/tasks/:id/complete` | Completer une tache |
| POST | `/api/tasks/:id/fail` | Echouer une tache |

## Hub WebSocket

Le hub WebSocket a `/ws` fournit des mises a jour en temps reel aux clients du tableau de bord connectes.

### Evenements

| Evenement | Source | Payload |
|-----------|--------|---------|
| `task.completed` | Worker | Details de la tache |
| `task.failed` | Worker | Tache + erreur |
| `message.received` | Assistant | Metadonnees du message |
| `response.sent` | Assistant | Metadonnees de la reponse |
| `fact.saved` | Storage | Details du fait |
| `insight.created` | Storage | Details de l'observation |
| `config.changed` | Magasin de config | Cle + valeur |

Les evenements sont publies via le bus d'evenements en utilisant `SubscribeAsync` (non bloquant).

### Protocole

```json
// Serveur → Client
{"type": "task.completed", "payload": {...}}
{"type": "fact.saved", "payload": {...}}
```

## SPA Vue 3

### Stack technique

- **Vue 3** — Composition API
- **Naive UI** — bibliotheque de composants
- **UnoCSS** — CSS utilitaire
- **vue-i18n** — internationalisation (6 langues)
- **vue-router** — routage cote client

### Vues

| Chemin | Composant | Authentification | Description |
|--------|-----------|------------------|-------------|
| `/` | Dashboard | JWT | Vue d'ensemble des statistiques, statut du planificateur |
| `/facts` | Facts | JWT | Navigateur de faits avec recherche, edition, suppression |
| `/insights` | Insights | JWT | Liste des observations |
| `/reminders` | Reminders | JWT | Liste des rappels |
| `/profile` | TechFacts | JWT | Metadonnees du profil comportemental |
| `/settings` | Settings | JWT | Gestion des competences, editeur de config |
| `/tasks` | Tasks | JWT | Onglets Aujourd'hui/En retard/A venir/Tous |
| `/chat` | Chat | JWT | Chat web WebSocket |
| `/users` | Users | Admin | CRUD utilisateur + liaisons de canaux |
| `/channels` | Channels | Admin | CRUD d'instances de canaux |
| `/agent-jobs` | AgentJobs | Admin | CRUD des taches planifiees |
| `/skills` | ExternalSkills | Admin | Marketplace + competences installees |
| `/setup` | Setup | Admin | Assistant de configuration web |
| `/config-debug` | ConfigDebug | Admin | Visualiseur brut des surcharges de config |
| `/login` | Login | Public | Formulaire de connexion |

### Gardes de routeur

```javascript
router.beforeEach((to, from, next) => {
    if (to.meta.public) { next(); return }
    if (!isLoggedIn()) { next({ name: 'login' }); return }
    if (to.meta.admin && !isAdmin()) { next({ name: 'dashboard' }); return }
    next()
})
```

### Composables cles

- `useWebSocket` — WebSocket avec reconnexion automatique et evenements types
- `useLocale` — etat reactif de la locale, detection RTL, synchronisation backend
- `useSkillStatus` — conditionne les elements de la barre laterale selon la disponibilite des competences

### Interface de gestion des competences

La vue Parametres fournit :

1. **Bascule de competence** — activer/desactiver chaque competence a l'execution
2. **Editeur de configuration** — configuration par competence avec :
   - Champs de formulaire pilotes par le schema
   - Protection des cles secretes (valeurs jamais divulguees dans l'API)
   - Chiffrement automatique pour les valeurs sensibles
   - Rechargement a chaud a la sauvegarde

### Tableau de bord des taches

La vue Taches agrege les taches de tous les fournisseurs :

- **Onglet Aujourd'hui** — taches dues aujourd'hui
- **Onglet En retard** — taches en retard
- **Onglet A venir** — 7 prochains jours
- **Onglet Tous** — toutes les taches incompletes
- **Bouton Synchroniser** — declenche une tache de synchronisation ponctuelle
- **Bouton Creer** — nouvelle tache avec selection du fournisseur

## Metriques Prometheus

Lorsque activees (`metrics.enabled = true`), les metriques sont exposees sur un port separe :

| Metrique | Type | Labels |
|----------|------|--------|
| `iulita_llm_requests_total` | Counter | provider, model, status |
| `iulita_llm_tokens_input_total` | Counter | provider |
| `iulita_llm_tokens_output_total` | Counter | provider |
| `iulita_llm_request_duration_seconds` | Histogram | provider |
| `iulita_llm_cost_usd_total` | Counter | — |
| `iulita_skill_executions_total` | Counter | skill, status |
| `iulita_task_total` | Counter | type, status |
| `iulita_messages_total` | Counter | direction |
| `iulita_cache_hits_total` | Counter | cache_type |
| `iulita_cache_misses_total` | Counter | cache_type |
| `iulita_active_sessions` | Gauge | — |

Les metriques sont alimentees par abonnement au bus d'evenements (non bloquant).
