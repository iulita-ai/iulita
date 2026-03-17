# Orchestration multi-agent

Iulita prend en charge l'execution parallele de sous-agents pour decomposer les taches complexes. Le LLM decide de maniere autonome quand utiliser l'orchestration en fonction de la complexite de la tache.

## Vue d'ensemble

La competence `orchestrate` lance plusieurs sous-agents specialises en parallele. Chaque sous-agent execute une boucle agentique simplifiee avec son propre prompt systeme, sous-ensemble d'outils et routage optionnel de fournisseur LLM. Les resultats sont collectes et renvoyes a l'assistant principal sous forme de rapport markdown structure.

## Architecture

```
Message utilisateur
    │
    ▼
Assistant (boucle agentique principale)
    │ decide que l'orchestration est necessaire
    │ appelle l'outil orchestrate avec les specifications d'agents
    ▼
Competence Orchestrate
    │ valide la profondeur (max 1)
    │ construit le Budget depuis la config + entrees
    ▼
Orchestrateur (execution parallele via errgroup)
    ├── Runner (agent_1: researcher) ──→ LLM ←→ Outils
    ├── Runner (agent_2: analyst)    ──→ LLM ←→ Outils
    └── Runner (agent_3: planner)    ──→ LLM ←→ Outils
         │
         │ budget de tokens atomique partage
         │ delai + contexte par agent
         │ evenements de statut → canaux
         ▼
    AgentResults collectes
    │ formatte en markdown
    ▼
L'assistant continue avec la sortie de l'orchestration
```

## Types d'agents

| Type | Focus du prompt | Outils par defaut | Route Hint |
|------|----------------|-------------------|------------|
| `researcher` | Collecte d'informations, recherche web | `web_search`, `webfetch` | — |
| `analyst` | Identification de motifs, anomalies, insights | tous | — |
| `planner` | Decomposition d'objectifs en etapes ordonnees | `datetime` | — |
| `coder` | Ecriture, revue, debogage de code | tous | — |
| `summarizer` | Condensation en points essentiels | tous | `ollama` |
| `generic` | Usage general | tous | — |

## Systeme de budget

Tous les agents partagent un compteur atomique unique `atomic.Int64`. Le plafond est **souple** par conception.

| Parametre | Defaut | Surcharge |
|-----------|--------|-----------|
| Nombre max de tours | 10 | `Budget.MaxTurns` |
| Delai d'attente | 60s | `Budget.Timeout` ou entree `timeout` |
| Agents paralleles max | 5 | `Budget.MaxAgents` ou config |
| Budget de tokens partage | illimite | `Budget.MaxTokens` ou entree `max_tokens` |

## Controle de profondeur

Les sous-agents ne peuvent pas generer d'autres sous-agents (`MaxDepth = 1`). Applique par verification de contexte et filtrage de l'outil `orchestrate`.

## Securite

- Les competences ApprovalManual/ApprovalPrompt sont filtrees des sous-agents
- Listes blanches d'outils optionnelles par agent
- L'outil `orchestrate` est toujours exclu des sous-agents

## Protocole d'evenements de statut

| Evenement | Quand | Champs |
|-----------|-------|--------|
| `orchestration_started` | Avant le lancement des agents | `agent_count` |
| `agent_started` | Par agent, avant l'execution | `agent_id`, `agent_type` |
| `agent_progress` | Par agent, apres chaque tour LLM | `agent_id`, `turn` |
| `agent_completed` | Par agent, en cas de succes | `agent_id`, `tokens`, `duration_ms` |
| `agent_failed` | Par agent, en cas d'erreur | `agent_id`, `error` |
| `orchestration_done` | Apres la fin de tous les agents | `success_count`, `total_tokens`, `duration_ms` |

Les evenements post-agent utilisent `context.Background()` avec un delai de 5 secondes pour garantir la livraison meme si la date limite du contexte parent a expire.

## Configuration

| Cle | Defaut | Description |
|-----|--------|-------------|
| `skills.orchestrate.enabled` | true | Activer/desactiver |
| `skills.orchestrate.max_tokens` | 0 (illimite) | Plafond du budget de tokens |
| `skills.orchestrate.max_agents` | 5 | Agents paralleles max |
| `skills.orchestrate.timeout` | 60s | Delai par agent |
| `skills.orchestrate.request_timeout` | 1h | Delai global d'orchestration (max 4h) |

Toutes les valeurs supportent le rechargement a chaud via `ConfigReloadable`.

## Frontend

Le composant `AgentProgress.vue` affiche le statut des agents en temps reel dans la vue de chat, avec icones par type, nom, statut et progression.
