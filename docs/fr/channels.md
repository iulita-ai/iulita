# Canaux

Iulita prend en charge plusieurs canaux de communication. Chaque canal convertit les messages specifiques a la plateforme en un format universel `IncomingMessage` et les route a travers l'assistant.

## Capacites des canaux

Chaque canal declare ses capacites via un masque de bits sur chaque message :

| Capacite | Console | Telegram | WebChat |
|----------|---------|----------|---------|
| Streaming | via bubbletea | Oui (base sur l'edition) | Oui (WebSocket) |
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

Bot Telegram complet avec streaming, regroupement et invites interactives.

### Configuration

1. Creez un bot via [@BotFather](https://t.me/BotFather)
2. Definissez le jeton : `iulita init` (trousseau) ou variable d'environnement `IULITA_TELEGRAM_TOKEN`
3. Optionnel : definissez `telegram.allowed_ids` pour restreindre l'acces a des IDs Telegram specifiques

### Fonctionnalites

- **Liste blanche d'utilisateurs** : `allowed_ids` restreint qui peut discuter avec le bot. Vide = autoriser tous (avertissement journalise)
- **Regroupement de messages** : les messages rapides du meme chat sont fusionnes (fenetre configurable)
- **Editions en streaming** : les reponses apparaissent progressivement via `EditMessageText` (limite a 1 edition/1.5s)
- **Decoupage de messages** : les messages de plus de 4000 caracteres sont divises aux limites de paragraphe/ligne/mot, en preservant les blocs de code
- **Thread de reponse** : le premier morceau repond au message de l'utilisateur ; les morceaux suivants sont independants
- **Indicateur de saisie** : action `ChatTyping` envoyee toutes les 4 secondes pendant le traitement
- **Surveillance de la sante** : `GetMe()` appele toutes les 60 secondes pour detecter les problemes de connectivite
- **Invites interactives** : claviers en ligne pour les interactions de competences (localisation meteo, etc.)
- **Support multimedia** : photos (plus grande taille), documents (limite 30 Mo), voix/audio (avec transcription)
- **Commandes integrees** : `/clear` (effacer l'historique), commandes enregistrees personnalisees
- **Bouton de signet** : bouton 💾 de clavier en ligne sur chaque reponse de l'assistant ; un clic sauvegarde la reponse complete comme fait avec raffinement LLM en arriere-plan
- **Messages de statut en temps reel** : mises a jour du statut pendant l'execution des outils et l'orchestration des agents, affichant la competence en cours et la progression des agents

### Pipeline de traitement des messages

```
Mise a jour Telegram entrante
    │
    ├── Requete callback ?
    │   ├── "noop" → acquitter silencieusement
    │   ├── "remember:*" → gestionnaire de signets (sauvegarder fait + retour ✅)
    │   └── autre → router vers le gestionnaire d'invites
    ├── Pas un message ? → ignorer
    ├── Utilisateur pas dans la liste blanche ? → rejeter
    ├── Commande /clear ? → traiter directement
    ├── Commande enregistree ? → router vers le gestionnaire
    ├── Invite active ? → router le texte vers l'invite
    │
    ▼
Construire IncomingMessage
    │ Caps = Streaming | Markdown | Typing | Buttons
    │
    ├── Resoudre l'utilisateur (plateforme → UUID iulita)
    ├── Rechercher la locale dans la BDD
    ├── Telecharger le media (photo/document/voix)
    ├── Verifier la limite de debit
    │
    ▼
Regroupement
    │ fusionner les messages rapides (texte joint avec \n)
    │ le minuteur se reinitialise a chaque nouveau message
    │
    ▼
Gestionnaire (Assistant.HandleMessage)
```

### Regroupeur

Le regroupeur fusionne les messages rapides du meme chat pour eviter plusieurs appels LLM :

- Chaque `chatID` possede un tampon avec un minuteur `time.AfterFunc`
- L'ajout d'un message reinitialise le minuteur
- Lorsque le minuteur expire, tous les messages tamponnes sont fusionnes :
  - Textes joints avec `"\n"`
  - Images et documents concatenes
  - Metadonnees du premier message preservees
- Si `debounce_window = 0`, les messages sont traites immediatement (non bloquant)
- `flushAll()` traite les tampons restants lors de l'arret

### Decoupage de messages

Les reponses longues sont decoupees en morceaux compatibles Telegram (4000 caracteres max) :

1. Essayer de decouper aux limites de paragraphe (`\n\n`)
2. Essayer de decouper aux limites de ligne (`\n`)
3. Essayer de decouper aux limites de mot (` `)
4. Decoupage brut en dernier recours
5. **Gestion des blocs de code** : si le decoupage se fait a l'interieur d'un bloc ``` , le fermer avec ``` et le rouvrir dans le morceau suivant

### Configuration

| Cle | Defaut | Description |
|-----|--------|-------------|
| `telegram.token` | — | Jeton du bot (rechargeable a chaud) |
| `telegram.allowed_ids` | `[]` | Liste blanche d'IDs utilisateur (vide = autoriser tous) |
| `telegram.debounce_window` | 2s | Fenetre de regroupement des messages |

## WebChat

Chat web base sur WebSocket integre dans le tableau de bord.

### Protocole

**Connexion** : WebSocket a `/ws/chat?user_id=<uuid>&username=<name>&chat_id=<optional>`

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

WebChat n'utilise **pas** le UserResolver. Le frontend obtient un jeton JWT via `/api/auth/login`, extrait `user_id` du payload, et le passe en parametre de requete WebSocket. Le canal fait confiance a ce `user_id` directement.

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
