# Canaux

Iulita prend en charge plusieurs canaux de communication. Chaque canal convertit les messages specifiques a la plateforme en un format universel `IncomingMessage` et les route a travers l'assistant.

## Capacites des canaux

Chaque canal declare ses capacites via un masque de bits sur chaque message :

| Capacite | Console | Telegram | WebChat |
|----------|---------|----------|---------|
| Streaming | via bubbletea | Réponse finale en tampon | Oui (WebSocket) |
| Markdown | via glamour | Oui | HTML |
| Reactions | Non | Non | Non |
| Boutons | Non | Oui (clavier en ligne) | Oui |
| Indicateur de saisie | Oui | Oui | Non |
| HTML | Non | Non | Oui |

Les capacites sont par message (pas par canal), ce qui permet un comportement mixte lorsque plusieurs canaux partagent un meme assistant. Les competences peuvent verifier les capacites via `channel.CapsFrom(ctx)` pour adapter leur format de sortie.

## Console TUI

Le mode par defaut — un chat terminal plein ecran propulse par [bubbletea](https://github.com/charmbracelet/bubbletea).

### Fonctionnalites

- **Disposition plein ecran** : viewport (historique du chat) + separateur + zone de texte (saisie) + barre de statut
- **Rendu Markdown** : via [glamour](https://github.com/charmbracelet/glamour) avec retour a la ligne adaptatif
- **Streaming** : apparition du texte en direct avec indicateur de chargement
- **Commandes slash** : `/help`, `/status`, `/compact`, `/clear`, `/quit`
- **Invites interactives** : options numerotees pour les interactions de competences (ex. selection de localisation meteo)
- **Detection de la couleur de fond** : adapte le rendu avant le demarrage de bubbletea

### Architecture

```
tuiModel (bubbletea)
    ├── viewport.Model (historique de chat defilable)
    ├── textarea.Model (saisie utilisateur)
    ├── statusBar (nom de competence, compteur de jetons, cout)
    └── streamBuf (texte de streaming en direct)
```

La structure `console.Channel` contient un `*tea.Program` protege par `sync.RWMutex`. Le programme bubbletea s'execute dans sa propre goroutine (`Start()` bloquant), tandis que `StartStream`, `SendMessage` et `NotifyStatus` sont appeles depuis la goroutine de l'assistant de maniere concurrente.

### Pont de streaming

Lorsque l'assistant diffuse une reponse en streaming :

1. `StartStream()` retourne des closures `editFn` et `doneFn`
2. `editFn(text)` envoie `streamChunkMsg` a bubbletea (texte complet accumule)
3. `doneFn(text)` envoie `streamDoneMsg` a bubbletea (finaliser et ajouter a l'historique)
4. Tous les messages sont thread-safe via `p.Send()` de bubbletea

### Commandes slash

| Commande | Description |
|----------|-------------|
| `/help` | Afficher toutes les commandes avec descriptions |
| `/status` | Compteur de competences, cout quotidien, jetons de session, nombre de messages |
| `/compact` | Declencher manuellement la compression de l'historique (asynchrone) |
| `/clear` | Effacer l'historique en memoire (TUI uniquement) |
| `/quit` / `/exit` | Quitter l'application |

### Coexistence avec le mode serveur

En mode console, le serveur s'execute en arriere-plan :
- Les journaux sont rediriges vers `iulita.log` (pas stderr, pour eviter de corrompre le TUI)
- Le tableau de bord reste accessible a l'adresse configuree
- Telegram et les autres canaux fonctionnent parallelement au TUI

## Telegram

Le [guide complet Telegram (EN)](../en/telegram.md) décrit les changements du code non encore publiés : sujets privés, cartes du jour et météo, alias, citations et callbacks personnels.

Configurez le jeton et `telegram.allowed_ids`, puis activez les sujets dans BotFather. Un message ordinaire à la racine crée un sujet ; `/topic` liste les sujets connus. `/clear` et `/compact` concernent le contexte courant. `/topic pause` suspend Iulita sans fermer le sujet ; `/topic delete` demande confirmation et annule le travail local lié, sans supprimer les tâches externes.

Utilisez `/day`, `/day text`, `/weather London 4` ou `/weather text London 4`. `telegram.day_widgets` et `telegram.day_actions` sont activés par défaut, y compris après mise à jour sans ces clés ; false explicite est conservé. Ces réglages nécessitent un redémarrage. Les actions demandent la confirmation du propriétaire.

Les réponses sont mises en mémoire tampon jusqu'au contrôle final de confidentialité. Aucun fragment non vérifié n'est publié. L'échec d'un envoi privé sensible ne déclenche pas un envoi ordinaire de ces données. Le regroupement sépare utilisateurs et sujets. Les widgets de production restent privés ; inline/shared nécessitent le serveur de test Telegram. Voir le guide EN pour la voix, les citations rich, la disponibilité et la récupération.

## WebChat

Chat web base sur WebSocket integre dans le tableau de bord.

### Protocole

**Connexion** : WebSocket a `/ws/chat?token=<access-token>`

**Messages entrants** (client → serveur) :
```json
{
  "text": "user message",
  "chat_id": "web:abc123",
  "prompt_id": "prompt_123_1",       // uniquement pour les reponses aux invites
  "prompt_answer": "option_id",      // uniquement pour les reponses aux invites
  "remember_message_id": "nano_ts"   // uniquement pour les requetes de signets
}
```

**Messages sortants** (serveur → client) :

| Type | Objectif | Champs cles |
|------|----------|-------------|
| `message` | Reponse normale | `text`, `timestamp` |
| `stream_edit` | Mise a jour en streaming | `text`, `message_id`, `timestamp` |
| `stream_done` | Stream finalise | `text`, `message_id`, `timestamp` |
| `status` | Evenements de traitement | `status`, `skill_name`, `success`, `duration_ms` |
| `prompt` | Question interactive | `text`, `prompt_id`, `options[]` |
| `remember_ack` | Confirmation de signet | `remember_ack.message_id`, `remember_ack.fact_id`, `remember_ack.status` |

### Protocole de signets

La fonctionnalite de signets permet aux utilisateurs de sauvegarder les reponses de l'assistant comme faits via un bouton de l'interface.

**Flux :**
1. Le serveur envoie `message` ou `stream_done` avec `message_id` (horodatage Unix en nanosecondes)
2. Le serveur met en cache le contenu avec la cle `(message_id, chatID)` pendant 10 minutes
3. Le frontend affiche l'icone 💾 au survol des messages de l'assistant
4. L'utilisateur clique → envoie `{"remember_message_id": "<message_id>"}`
5. Le serveur valide la propriete (`chatID` doit correspondre a l'entree en cache), sauvegarde comme fait avec `source_type="bookmark"`, met en file le raffinement LLM en arriere-plan
6. Le serveur envoie `{"type": "remember_ack", "remember_ack": {"message_id": "...", "status": "saved", "fact_id": 42}}`
7. Le frontend met a jour l'icone vers ✅

**Valeurs de status** : `saved`, `error`, `expired` (message plus en cache)

### Authentification

Le serveur valide le jeton d’accès et le compte courant avant l’upgrade. Il détermine lui-même l’identité et `web:<user_id>` ; les champs du client ne permettent pas de choisir un autre compte. L’Origin fourni doit correspondre à l’hôte et au protocole. L’expiration ou le remplacement de la connexion annule le travail en attente. WebChat conserve un chat par utilisateur ; l’API multiconversation utilise un OAuth distinct.

### Serialisation des ecritures

Toutes les ecritures WebSocket passent par un `sync.Mutex` par connexion pour eviter les panics d'ecritures concurrentes. Chaque connexion est suivie dans une map `clients[chatID]`.

### Invites interactives

Les invites utilisent des IDs bases sur un compteur atomique : `prompt_<timestamp>_<counter>`. Le serveur envoie un message `prompt` avec des options ; le client repond avec `prompt_id` et `prompt_answer`. Les invites en attente sont stockees dans une `sync.Map` avec un delai d'expiration.

## Gestionnaire de canaux

Le `channelmgr.Manager` orchestre toutes les instances de canaux a l'execution.

### Cycle de vie

- **StartAll** : charge toutes les instances de canaux depuis la BDD, demarre chacune dans une goroutine
- **StopInstance** : annule le contexte, attend sur le canal done (delai de 5s)
- **AddInstance / UpdateInstance** : pour les instances creees/modifiees via le tableau de bord
- **Rechargement a chaud** : `UpdateConfigToken(token)` redemarre les instances Telegram configurees par fichier

### Routage des messages

Lorsque l'assistant doit envoyer un message proactif (rappel, heartbeat) :

1. Rechercher quelle instance de canal possede le `chatID` via la BDD
2. Si trouvee et en cours d'execution, utiliser l'expediteur de ce canal
3. Repli : utiliser le premier canal en cours d'execution

### Types de canaux supportes

| Type | Source | Rechargement a chaud |
|------|--------|----------------------|
| Telegram | Configuration ou BDD | Rechargement du jeton |
| WebChat | BDD (initialisation) | — |
| Console | Mode console uniquement | — |

## Resolution d'utilisateur

Le `DBUserResolver` mappe les identites de plateforme vers les UUIDs iulita :

1. Rechercher `user_channels` par `(channel_type, channel_user_id)`
2. Si trouve → retourner le `user.ID` existant
3. Si non trouve et enregistrement automatique active :
   - Creer un nouveau `User` avec mot de passe aleatoire et `MustChangePass: true`
   - Lier le canal a l'utilisateur
   - Retourner le nouvel UUID
4. Si non trouve et enregistrement automatique desactive → rejeter

**Locale par canal** : apres la resolution, `GetChannelLocale(ctx, channelType, channelUserID)` est appelee pour definir `msg.Locale` a partir de la preference stockee en BDD.

## Evenements de statut

Les canaux recoivent des notifications `StatusEvent` pour le retour d'experience :

| Type | Quand | Utilisation |
|------|-------|-------------|
| `processing` | Message recu, avant l'appel LLM | Afficher "en cours de reflexion..." |
| `skill_start` | Avant l'execution de la competence | Afficher le nom de la competence |
| `skill_done` | Apres l'execution de la competence | Afficher la duree, succes/echec |
| `stream_start` | Avant le debut du streaming | Preparer l'interface de streaming |
| `error` | En cas d'erreur | Afficher le message d'erreur |
| `locale_changed` | Apres la competence set_language | Mettre a jour la locale de l'interface |
| `orchestration_started` | Avant le lancement des sous-agents | Afficher le nombre d'agents |
| `agent_started` | Par agent, avant l'execution | Afficher le nom + type d'agent |
| `agent_progress` | Par agent, apres chaque tour LLM | Mettre a jour le compteur de tours |
| `agent_completed` | Par agent, en cas de succes | Afficher la duree + tokens |
| `agent_failed` | Par agent, en cas d'erreur | Afficher le message d'erreur |
| `orchestration_done` | Apres la fin de tous les agents | Afficher les statistiques |
