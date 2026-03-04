# Premiers pas

## Apercu

Iulita est un assistant IA personnel qui apprend a partir de vos donnees reelles, et non d'hallucinations. Il ne stocke que les faits verifies que vous partagez explicitement, construit des observations en croisant vos donnees reelles, et n'invente jamais ce qu'il ne sait pas.

**Console d'abord** : lance une interface TUI plein ecran par defaut. Fonctionne egalement en mode serveur sans interface avec Telegram, Web Chat et un tableau de bord web.

## Installation

### Option 1 : Telecharger le binaire pre-compile

Telechargez la derniere version depuis [GitHub Releases](https://github.com/iulita-ai/iulita/releases/latest) :

```bash
# macOS (Apple Silicon)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-darwin-arm64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-darwin-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# Linux (x86_64)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-linux-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/
```

### Option 2 : Compiler depuis les sources

**Prerequis** : Go 1.25+, Node.js 22+, npm

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build
```

Cela compile le frontend Vue 3 et le binaire Go. Le resultat se trouve dans `./bin/iulita`.

Pour compiler uniquement le binaire Go (sans le frontend) :

```bash
make build-go
```

### Option 3 : Docker

```bash
cp config.toml.example config.toml
# Editez config.toml — definissez claude.api_key au minimum
mkdir -p data
docker compose up -d
```

Image pre-construite :

```yaml
services:
  iulita:
    image: ghcr.io/iulita-ai/iulita:latest
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/app/data
      - ./config.toml:/app/config.toml:ro
```

Au premier lancement sans configuration, le serveur demarre en **mode configuration** — un assistant web a l'adresse `http://localhost:8080` vous guide a travers la selection du fournisseur, la configuration des fonctionnalites et l'import TOML.

## Premier lancement

### Assistant de configuration interactif

```bash
iulita init
```

L'assistant vous guide a travers :
1. **Selection du fournisseur LLM** — Claude (recommande), OpenAI ou Ollama
2. **Saisie de la cle API** — stockee de maniere securisee dans le trousseau systeme (macOS Keychain, Linux SecretService)
3. **Integrations optionnelles** — jeton Telegram, parametres de proxy, fournisseur d'embeddings
4. **Selection du modele** — recupere dynamiquement les modeles disponibles du fournisseur selectionne

Les secrets sont stockes dans le trousseau du systeme d'exploitation lorsque disponible, avec un repli vers un fichier chiffre a `~/.config/iulita/encryption.key`.

### Lancer le TUI Console (mode par defaut)

```bash
iulita
```

Cela lance le TUI interactif en plein ecran. Tapez vos messages, utilisez `/help` pour les commandes disponibles.

**Commandes console :**
| Commande | Description |
|----------|-------------|
| `/help` | Afficher les commandes disponibles |
| `/status` | Afficher le nombre de competences, le cout quotidien, les jetons de session |
| `/compact` | Compresser manuellement l'historique de conversation |
| `/clear` | Effacer l'historique en memoire |
| `/quit` / `/exit` | Quitter l'application |

**Raccourcis clavier :**
- `Enter` — Envoyer un message
- `Ctrl+C` — Quitter
- `Shift+Enter` — Nouvelle ligne dans le message

### Lancer le mode serveur

Pour fonctionner en service d'arriere-plan avec Telegram, Web Chat et le tableau de bord :

```bash
iulita --server
```

Ou de maniere equivalente :
```bash
iulita -d
```

Le tableau de bord est accessible a l'adresse `http://localhost:8080` (configurable via `server.address`).

## Configuration

Tous les parametres se trouvent dans `config.toml` (optionnel — l'installation locale sans configuration fonctionne avec uniquement une cle API dans le trousseau). Chaque option peut etre surchargee via des variables d'environnement avec le prefixe `IULITA_`.

### Emplacements des fichiers (conformes XDG)

| Plateforme | Configuration | Donnees | Cache | Journaux |
|------------|---------------|---------|-------|----------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

Surchargez tous les chemins avec la variable d'environnement `IULITA_HOME`.

### Variables d'environnement principales

| Variable | Description |
|----------|-------------|
| `IULITA_CLAUDE_API_KEY` | Cle API Anthropic (requise pour Claude) |
| `IULITA_TELEGRAM_TOKEN` | Jeton du bot Telegram |
| `IULITA_CLAUDE_MODEL` | Identifiant du modele Claude |
| `IULITA_STORAGE_PATH` | Chemin de la base de donnees SQLite |
| `IULITA_SERVER_ADDRESS` | Adresse d'ecoute du tableau de bord (`:8080`) |
| `IULITA_PROXY_URL` | Proxy HTTP/SOCKS5 pour toutes les requetes |
| `IULITA_JWT_SECRET` | Cle de signature JWT (generee automatiquement si non definie) |
| `IULITA_HOME` | Surcharger tous les chemins XDG |

Voir [`config.toml.example`](../../config.toml.example) pour la reference complete avec toutes les configurations de competences.

## Reference CLI

| Commande / Option | Description |
|--------------------|-------------|
| `iulita` | Lancer le TUI console interactif (par defaut) |
| `iulita --server` / `-d` | Executer en mode serveur sans interface |
| `iulita init` | Assistant de configuration interactif |
| `iulita init --print-defaults` | Afficher la configuration config.toml par defaut |
| `iulita --doctor` | Executer les verifications de diagnostic |
| `iulita --version` / `-v` | Afficher la version et quitter |

## Verification rapide

Apres la configuration, verifiez que tout fonctionne :

```bash
# Verifier les diagnostics
iulita --doctor

# Lancer le TUI
iulita

# Tapez : "remember that my favorite color is blue"
# Puis : "what is my favorite color?"
```

Si l'assistant rappelle correctement "blue", la memoire fonctionne de bout en bout.

## Etapes suivantes

- [Architecture](architecture.md) — comprendre comment le systeme est construit
- [Memoire et observations](memory-and-insights.md) — fonctionnement du stockage de faits et du croisement de donnees
- [Canaux](channels.md) — configurer Telegram, Web Chat ou personnaliser le TUI
- [Competences](skills.md) — explorer les 20+ outils disponibles
- [Configuration](configuration.md) — exploration detaillee de toutes les options de configuration
- [Deploiement](deployment.md) — Docker, Kubernetes et mise en production
