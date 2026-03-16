# Deploiement

## Installation locale

### Binaire

```bash
# Telecharger et installer
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-linux-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# Configuration
iulita init        # assistant interactif
iulita             # lancer le TUI (par defaut)
iulita --server    # mode serveur sans interface
```

### Compiler depuis les sources

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build         # frontend + binaire Go → ./bin/iulita
make build-go      # binaire Go uniquement (sans recompiler le frontend)
```

**Prerequis** : Go 1.25+, Node.js 22+, npm

## Docker

### docker-compose.yml

```yaml
services:
  iulita:
    image: ghcr.io/iulita-ai/iulita:latest
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/app/data
      - ./config.toml:/app/config.toml:ro
      - ./skills:/app/skills:ro
    restart: unless-stopped
```

### Premier lancement (assistant web)

Sans `config.toml`, le serveur demarre en **mode configuration** :

1. Naviguez vers `http://localhost:8080`
2. Completez l'assistant en 5 etapes :
   - Bienvenue / Import d'un TOML existant
   - Selection du fournisseur LLM
   - Configuration (cles API, modele)
   - Bascule des fonctionnalites
   - Termine
3. L'assistant sauvegarde la configuration dans la base de donnees
4. Cree le sentinelle `db_managed` (desactive le chargement TOML)

### Avec fichier de configuration

```bash
cp config.toml.example config.toml
# Editez config.toml — definissez claude.api_key au minimum
mkdir -p data
docker compose up -d
```

### Dockerfile (multi-etapes)

```
Etape 1 (ui-builder) : node:22-alpine
    → npm ci + npm run build

Etape 2 (go-builder) : golang:1.25-alpine
    → CGO_ENABLED=1 (requis pour SQLite)
    → Copie le dist UI avant la compilation Go

Etape 3 (runtime) : alpine:3.21
    → ca-certificates + tzdata
    → Utilisateur non-root "iulita" (UID 1000)
    → Expose le port 8080
    → Entrypoint : iulita --server
```

**Volume** : `/app/data` pour la base de donnees SQLite et le cache du modele ONNX.

## Variables d'environnement

Toutes les cles de configuration correspondent a des variables d'environnement :

```bash
# Requises
IULITA_CLAUDE_API_KEY=sk-ant-...

# Optionnelles
IULITA_TELEGRAM_TOKEN=123456:ABC...
IULITA_STORAGE_PATH=/app/data/iulita.db
IULITA_SERVER_ADDRESS=:8080
IULITA_PROXY_URL=socks5://proxy:1080
IULITA_JWT_SECRET=your-secret-here
IULITA_CLAUDE_MODEL=claude-sonnet-4-5-20250929
```

## Reverse proxy

### nginx

```nginx
server {
    listen 443 ssl;
    server_name iulita.example.com;

    ssl_certificate /etc/ssl/certs/iulita.crt;
    ssl_certificate_key /etc/ssl/private/iulita.key;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Support WebSocket
    location /ws {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }

    location /ws/chat {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }
}
```

### Caddy

```txt
iulita.example.com {
    reverse_proxy localhost:8080
}
```

Caddy gere automatiquement la mise a niveau WebSocket.

## Verifications de sante

### Diagnostics CLI

```bash
iulita --doctor
```

Verifications :
- Accessibilite du fichier de configuration
- Connectivite a la base de donnees
- Joignabilite du fournisseur LLM
- Disponibilite du trousseau
- Statut du modele d'embeddings

### Surveillance de sante Telegram

Le canal Telegram appelle `GetMe()` toutes les 60 secondes. Les echecs consecutifs sont journalises. Cela detecte les problemes reseau et les revocations de jetons.

## Surveillance

### Metriques Prometheus

Activer dans la configuration :

```toml
[metrics]
enabled = true
address = ":9090"
```

Metriques cles :
- `iulita_llm_requests_total` — volume d'appels LLM par fournisseur/statut
- `iulita_llm_cost_usd_total` — cout cumule
- `iulita_skill_executions_total` — patrons d'utilisation des competences
- `iulita_messages_total` — volume de messages (entrants/sortants)
- `iulita_cache_hits_total` — efficacite du cache

### Controle des couts

```toml
[cost]
daily_limit_usd = 10.0  # arreter les appels LLM lorsque le cout quotidien atteint 10$
```

Le cout est suivi en memoire (reinitialisation quotidienne) et persiste dans la table `usage_stats`.

## Sauvegarde

### Base de donnees

La base de donnees SQLite est la source unique de verite. Sauvegardez le fichier a `{DataDir}/iulita.db` :

```bash
# Copie simple (securisee en mode WAL lorsqu'aucune ecriture n'est en cours)
cp ~/.local/share/iulita/iulita.db backup/

# Utilisation de l'API de sauvegarde SQLite (securisee pendant les ecritures)
sqlite3 ~/.local/share/iulita/iulita.db ".backup backup/iulita.db"
```

### Configuration

Si vous utilisez la configuration par fichier :
```bash
cp ~/.config/iulita/config.toml backup/
```

Si vous utilisez la configuration geree par la BDD (assistant Docker) :
- La configuration est stockee dans la table `config_overrides` de la base de donnees
- La sauvegarde de la BDD inclut la configuration

### Secrets

Les secrets dans le trousseau ne sont **pas** inclus dans les sauvegardes de fichiers. Exportez-les :
```bash
export IULITA_CLAUDE_API_KEY=$(security find-generic-password -s iulita -a claude-api-key -w)  # macOS
```

## Cibles du Makefile

| Cible | Description |
|-------|-------------|
| `make build` | Compiler le frontend + le binaire Go |
| `make build-go` | Binaire Go uniquement |
| `make ui` | Compiler la SPA Vue uniquement |
| `make run` | Compiler + lancer le TUI console |
| `make console` | Executer le TUI (go run, sans compilation) |
| `make server` | Compiler + executer le serveur sans interface |
| `make dev` | Mode dev : serveur dev Vue + serveur Go |
| `make test` | Executer tous les tests (Go + frontend) |
| `make test-go` | Tests Go uniquement |
| `make test-ui` | Tests frontend uniquement |
| `make test-coverage` | Couverture pour les deux |
| `make tidy` | go mod tidy |
| `make clean` | Supprimer les artefacts de compilation |
| `make check-secrets` | Executer le scan gitleaks |
| `make setup-hooks` | Installer les hooks pre-commit |
| `make release` | Taguer et pousser une release |

## Developpement

### Developpement avec rechargement a chaud

```bash
make dev
```

Cela demarre :
1. Le serveur dev Vue avec HMR sur le port 5173
2. Le serveur Go avec le drapeau `--server`

Le serveur dev Vue proxifie les appels API vers le serveur Go.

### Executer les tests

```bash
make test              # tous les tests
make test-go           # tests Go avec detecteur de races
make test-ui           # Vitest
make test-coverage     # rapports de couverture
```

### Hooks pre-commit

```bash
make setup-hooks
```

Installe un hook git pre-commit qui execute `gitleaks detect` pour empecher les commits accidentels de secrets.
