# Despliegue

## Instalacion Local

### Binario

```bash
# Descargar e instalar
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-linux-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# Configuracion
iulita init        # asistente interactivo
iulita             # iniciar TUI (predeterminado)
iulita --server    # modo servidor headless
```

### Compilar desde el Codigo Fuente

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build         # frontend + binario Go → ./bin/iulita
make build-go      # solo binario Go (omitir recompilacion del frontend)
```

**Requisitos previos**: Go 1.25+, Node.js 22+, npm

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

### Primera Ejecucion (Asistente Web)

Sin un `config.toml`, el servidor arranca en **modo configuracion**:

1. Navegar a `http://localhost:8080`
2. Completar el asistente de 5 pasos:
   - Bienvenida / Importar TOML existente
   - Seleccion de proveedor LLM
   - Configuracion (claves API, modelo)
   - Toggles de funcionalidades
   - Completar
3. El asistente guarda la configuracion en la base de datos
4. Crea el sentinel `db_managed` (deshabilita la carga de TOML)

### Con Archivo de Configuracion

```bash
cp config.toml.example config.toml
# Editar config.toml — establecer claude.api_key como minimo
mkdir -p data
docker compose up -d
```

### Dockerfile (Multi-Etapa)

```
Etapa 1 (ui-builder): node:22-alpine
    → npm ci + npm run build

Etapa 2 (go-builder): golang:1.25-alpine
    → CGO_ENABLED=1 (requerido para SQLite)
    → Copia dist de UI antes de compilar Go

Etapa 3 (runtime): alpine:3.21
    → ca-certificates + tzdata
    → Usuario no-root "iulita" (UID 1000)
    → Expone puerto 8080
    → Entrypoint: iulita --server
```

**Volumen**: `/app/data` para la base de datos SQLite y cache del modelo ONNX.

## Variables de Entorno

Todas las claves de configuracion se mapean a variables de entorno:

```bash
# Requerido
IULITA_CLAUDE_API_KEY=sk-ant-...

# Opcional
IULITA_TELEGRAM_TOKEN=123456:ABC...
IULITA_STORAGE_PATH=/app/data/iulita.db
IULITA_SERVER_ADDRESS=:8080
IULITA_PROXY_URL=socks5://proxy:1080
IULITA_JWT_SECRET=your-secret-here
IULITA_CLAUDE_MODEL=claude-sonnet-4-5-20250929
```

## Proxy Inverso

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

    # Soporte WebSocket
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

```caddyfile
iulita.example.com {
    reverse_proxy localhost:8080
}
```

Caddy maneja la actualizacion WebSocket automaticamente.

## Verificaciones de Salud

### Diagnosticos por CLI

```bash
iulita --doctor
```

Verificaciones:
- Accesibilidad del archivo de configuracion
- Conectividad de la base de datos
- Alcanzabilidad del proveedor LLM
- Disponibilidad del llavero
- Estado del modelo de embeddings

### Monitor de Salud de Telegram

El canal Telegram llama `GetMe()` cada 60 segundos. Los fallos consecutivos se registran. Esto detecta problemas de red y revocaciones de token.

## Monitoreo

### Metricas Prometheus

Habilitar en la configuracion:

```toml
[metrics]
enabled = true
address = ":9090"
```

Metricas clave:
- `iulita_llm_requests_total` — volumen de llamadas LLM por proveedor/estado
- `iulita_llm_cost_usd_total` — costo acumulado
- `iulita_skill_executions_total` — patrones de uso de habilidades
- `iulita_messages_total` — volumen de mensajes (entrada/salida)
- `iulita_cache_hits_total` — efectividad del cache

### Controles de Costos

```toml
[cost]
daily_limit_usd = 10.0  # detener llamadas LLM cuando el costo diario alcance $10
```

El costo se rastrea en memoria (se reinicia diariamente) y se persiste en la tabla `usage_stats`.

## Respaldo

### Base de Datos

La base de datos SQLite es la unica fuente de verdad. Respaldar el archivo en `{DataDir}/iulita.db`:

```bash
# Copia simple (segura con modo WAL cuando no hay escrituras en curso)
cp ~/.local/share/iulita/iulita.db backup/

# Usando la API de respaldo de SQLite (segura durante escrituras)
sqlite3 ~/.local/share/iulita/iulita.db ".backup backup/iulita.db"
```

### Configuracion

Si se usa configuracion basada en archivo:
```bash
cp ~/.config/iulita/config.toml backup/
```

Si se usa configuracion gestionada por BD (asistente Docker):
- La configuracion se almacena en la tabla `config_overrides` dentro de la base de datos
- Respaldar la BD incluye la configuracion

### Secretos

Los secretos en el llavero **no** se incluyen en los respaldos de archivos. Exportarlos:
```bash
export IULITA_CLAUDE_API_KEY=$(security find-generic-password -s iulita -a claude-api-key -w)  # macOS
```

## Objetivos del Makefile

| Objetivo | Descripcion |
|----------|-------------|
| `make build` | Compilar frontend + binario Go |
| `make build-go` | Solo binario Go |
| `make ui` | Compilar solo SPA Vue |
| `make run` | Compilar + iniciar consola TUI |
| `make console` | Ejecutar TUI (go run, sin compilar) |
| `make server` | Compilar + ejecutar servidor headless |
| `make dev` | Modo desarrollo: servidor Vue dev + servidor Go |
| `make test` | Ejecutar todas las pruebas (Go + frontend) |
| `make test-go` | Solo pruebas Go |
| `make test-ui` | Solo pruebas frontend |
| `make test-coverage` | Cobertura para ambos |
| `make tidy` | go mod tidy |
| `make clean` | Eliminar artefactos de compilacion |
| `make check-secrets` | Ejecutar escaneo gitleaks |
| `make setup-hooks` | Instalar hooks pre-commit |
| `make release` | Etiquetar y publicar release |

## Desarrollo

### Desarrollo con Recarga en Caliente

```bash
make dev
```

Esto inicia:
1. Servidor de desarrollo Vue con HMR en el puerto 5173
2. Servidor Go con flag `--server`

El servidor de desarrollo Vue redirige las llamadas API al servidor Go.

### Ejecutar Pruebas

```bash
make test              # todas las pruebas
make test-go           # pruebas Go con detector de carreras
make test-ui           # Vitest
make test-coverage     # reportes de cobertura
```

### Hooks Pre-commit

```bash
make setup-hooks
```

Instala un hook git pre-commit que ejecuta `gitleaks detect` para prevenir commits accidentales de secretos.
