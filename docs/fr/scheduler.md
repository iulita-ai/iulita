# Planificateur

Le planificateur est un systeme a deux composants : un **coordinateur** qui produit des taches selon un calendrier, et un **worker** qui les reclame et les execute. Les deux utilisent SQLite comme file de taches.

## Architecture

```
Scheduler (Coordinateur)
    │ sonde toutes les 30s
    │ verifie les horaires des taches dans scheduler_states
    │
    ├── InsightJob (24h) → taches insight.generate
    ├── InsightCleanupJob (1h) → taches insight.cleanup
    ├── TechFactsJob (6h) → taches techfact.analyze
    ├── HeartbeatJob (6h) → taches heartbeat.check
    ├── RemindersJob (30s) → taches reminder.fire
    ├── AgentJobsJob (30s) → taches agent.job
    └── TodoSyncJob (cron horaire) → taches todo.sync
           │
           ▼
    table tasks (SQLite)
           │
           ▼
Worker
    │ sonde toutes les 5s
    │ reclame les taches de maniere atomique
    │ dispatche vers les gestionnaires enregistres
    │
    ├── InsightGenerateHandler
    ├── InsightCleanupHandler
    ├── TechFactAnalyzeHandler
    ├── HeartbeatHandler
    ├── ReminderFireHandler
    ├── AgentJobHandler
    ├── RefineBookmarkHandler
    └── TodoSyncHandler
```

## Coordinateur

### Definition de tache

```go
type JobDefinition struct {
    Name        string
    Interval    time.Duration
    CronExpr    string           // cron standard (5 champs)
    Timezone    string           // fuseau horaire IANA pour cron
    Enabled     bool
    CreateTasks func(ctx) ([]domain.Task, error)
}
```

Chaque tache declare soit un `Interval` fixe, soit une `CronExpr`. Cron utilise `robfig/cron/v3` avec support des fuseaux horaires.

### Boucle de planification

1. **Echauffement** : au premier demarrage, `NextRun = now + 1 minute` (periode de grace)
2. **Tick** toutes les 30 secondes :
   - Maintenance : reclamer les taches perimees (en cours > 5 min), supprimer les anciennes taches (> 7 jours)
   - Pour chaque tache activee : si `now >= state.NextRun`, appeler `CreateTasks`
   - Inserer les taches via `CreateTaskIfNotExists` (idempotent par `UniqueKey`)
   - Mettre a jour l'etat : `LastRun = now`, `NextRun = computeNextRun()`

### Declenchement manuel

`TriggerJob(name)` :
- Trouve la tache nommee
- Appelle `CreateTasks` avec `Priority = 1` (haute)
- Insere les taches immediatement
- Ne met PAS a jour l'etat du calendrier (la prochaine execution reguliere a toujours lieu)

Disponible via le tableau de bord : `POST /api/schedulers/:name/trigger`

## Worker

### Reclamation de taches

```
Toutes les 5 secondes :
    pour chaque slot de concurrence disponible :
        ClaimTask(ctx, workerID, capabilities)  // transaction SQLite atomique
        si tache reclamee :
            go executeTask(task)
        sinon :
            break  // plus de taches disponibles
```

`workerID = hostname-pid` (unique par processus).

### Routage base sur les capacites

Les taches declarent les capacites requises sous forme de chaine separee par des virgules (ex. `"llm,storage"`). La liste de capacites du worker doit etre un sur-ensemble.

**Capacites du worker local** : `["storage", "llm", "telegram"]`

**Worker distant** : tout ensemble de capacites, authentifie via `Scheduler.WorkerToken`.

### Cycle de vie des taches

```
pending → claimed (par le worker) → running → completed / failed
```

- `ClaimTask` : SELECT + UPDATE atomique dans une transaction
- `StartTask` : definir le statut a `running`, enregistrer l'heure de debut
- `CompleteTask` : stocker le resultat, publier l'evenement `TaskCompleted`
- `FailTask` : stocker l'erreur, publier l'evenement `TaskFailed`

### API Worker distant

Pour les deploiements distribues, le tableau de bord expose une API REST :

| Endpoint | Methode | Description |
|----------|---------|-------------|
| `/api/tasks/` | GET | Lister les taches |
| `/api/tasks/counts` | GET | Comptages par statut |
| `/api/tasks/claim` | POST | Reclamer une tache |
| `/api/tasks/:id/start` | POST | Marquer comme en cours |
| `/api/tasks/:id/complete` | POST | Completer avec resultat |
| `/api/tasks/:id/fail` | POST | Echouer avec erreur |

Authentifie via jeton Bearer statique (`scheduler.worker_token`).

## Taches integrees

### Generation d'observations (`insights`)

- **Intervalle** : 24 heures (configurable via `skills.insights.interval`)
- **Type de tache** : `insight.generate`
- **Capacites** : `llm,storage`
- **Condition** : le chat/utilisateur doit avoir >= `minFacts` (defaut 20) faits

**Pipeline du gestionnaire :**
1. Charger tous les faits de l'utilisateur
2. Construire les vecteurs TF-IDF (tokeniser, bigrammes, scores TF-IDF)
3. Clustering K-means++ : `k = sqrt(numFacts / 3)`, distance cosinus, 20 iterations
4. Echantillonner jusqu'a 6 paires inter-clusters (ignorer les paires deja couvertes)
5. Pour chaque paire : le LLM genere une observation + note la qualite (1-5)
6. Stocker les observations avec qualite >= seuil

### Nettoyage des observations (`insight_cleanup`)

- **Intervalle** : 1 heure
- **Type de tache** : `insight.cleanup`
- **Capacites** : `storage`

Supprime les observations ou `expires_at < now`. TTL par defaut de 30 jours.

### Analyse des faits techniques (`techfacts`)

- **Intervalle** : 6 heures (configurable)
- **Type de tache** : `techfact.analyze`
- **Capacites** : `llm,storage`
- **Condition** : 10+ messages dont 5+ de l'utilisateur

**Gestionnaire** : envoie les messages utilisateur au LLM en demandant un JSON structure : `[{category, key, value, confidence}]`. Les categories incluent les sujets, le style de communication et les schemas comportementaux. Upsert dans la table `tech_facts`.

### Heartbeat (`heartbeat`)

- **Intervalle** : 6 heures (configurable)
- **Type de tache** : `heartbeat.check`
- **Capacites** : `llm,storage,telegram`

**Gestionnaire** : rassemble les faits recents, les observations et les rappels en attente. Demande au LLM si un message de suivi est justifie. Si la reponse n'est pas `HEARTBEAT_OK`, envoie le message a l'utilisateur.

### Rappels (`reminders`)

- **Intervalle** : 30 secondes
- **Type de tache** : `reminder.fire`
- **Capacites** : `telegram,storage`

**Gestionnaire** : formate le rappel avec l'heure locale, envoie via `MessageSender`, marque comme declenche.

### Agent Jobs (`agent_jobs`)

- **Intervalle** : 30 secondes
- **Type de tache** : `agent.job`
- **Capacites** : `llm`

Sonde `GetDueAgentJobs(now)` pour les taches LLM planifiees par l'utilisateur. Met a jour `next_run` immediatement (avant l'execution) pour eviter les doublons.

**Gestionnaire** : appelle `provider.Complete` avec le prompt defini par l'utilisateur. Livre optionnellement le resultat a un chat configure.

### Raffinement de signets (`bookmark.refine`)

- **Declencheur** : a la demande (cree par `bookmark.Service.Save`)
- **Type de tache** : `bookmark.refine`
- **Capacites** : `llm,storage`
- **Tentatives max** : 2
- **Suppression apres execution** : oui

**Gestionnaire** : Recoit `{fact_id, content, chat_id, user_id}`. Appelle le LLM avec un prompt de resume pour extraire 1-3 phrases concises. Met a jour le contenu du fait si le raffinement est significativement plus court (<90% de l'original). Gere correctement les faits deja supprimes.

**Ce n'est pas une tache planifiee** — les taches sont creees a la demande lorsque les utilisateurs cliquent sur le bouton de signet. Le worker les recupere au prochain cycle de sondage (toutes les 5 secondes).

### Synchronisation des taches (`todo_sync`)

- **Cron** : `0 * * * *` (horaire)
- **Type de tache** : `todo.sync`
- **Capacites** : `storage`

**Gestionnaire** : itere sur toutes les instances `TodoProvider` disponibles (Todoist, Google Tasks, Craft). Pour chacune : `FetchAll` → upsert dans `todo_items` → supprimer les entrees perimees.

## Agent Jobs (definis par l'utilisateur)

Les utilisateurs peuvent creer des taches LLM planifiees via le tableau de bord :

```json
{
  "name": "Daily Summary",
  "prompt": "Summarize my recent facts and insights",
  "cron_expr": "0 9 * * *",
  "delivery_chat_id": "123456789"
}
```

Champs :
- `name` — nom d'affichage
- `prompt` — prompt LLM a executer
- `cron_expr` ou `interval` — planification
- `delivery_chat_id` — ou envoyer le resultat (optionnel)

Gere via le tableau de bord : `GET/POST/PUT/DELETE /api/agent-jobs/`
