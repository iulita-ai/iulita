# Primeros Pasos

## Descripcion General

Iulita es un asistente personal de IA que aprende de tus datos reales, no de alucinaciones. Solo almacena datos verificados que compartes explicitamente, construye perspectivas cruzando tus datos reales, y nunca inventa cosas que no sabe.

**Consola primero**: se inicia como un chat TUI de pantalla completa por defecto. Tambien funciona como servidor headless con Telegram, Web Chat y un panel de control web.

## Instalacion

### Opcion 1: Descargar Binario Pre-compilado

Descarga la ultima version desde [GitHub Releases](https://github.com/iulita-ai/iulita/releases/latest):

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

### Opcion 2: Compilar desde el Codigo Fuente

**Requisitos previos**: Go 1.25+, Node.js 22+, npm

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build
```

Esto compila el frontend Vue 3 y el binario Go. La salida es `./bin/iulita`.

Para compilar solo el binario Go (omitiendo el frontend):

```bash
make build-go
```

### Opcion 3: Docker

```bash
cp config.toml.example config.toml
# Editar config.toml — establecer claude.api_key como minimo
mkdir -p data
docker compose up -d
```

Imagen pre-compilada:

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

En la primera ejecucion sin configuracion, el servidor arranca en **modo de configuracion** — un asistente web en `http://localhost:8080` te guia a traves de la seleccion de proveedor, configuracion de funcionalidades e importacion de TOML.

## Primera Ejecucion

### Asistente de Configuracion Interactivo

```bash
iulita init
```

El asistente te guia a traves de:
1. **Seleccion de proveedor LLM** — Claude (recomendado), OpenAI u Ollama
2. **Ingreso de clave API** — almacenada de forma segura en el llavero del sistema (macOS Keychain, Linux SecretService)
3. **Integraciones opcionales** — token de bot de Telegram, configuracion de proxy, proveedor de embeddings
4. **Seleccion de modelo** — obtiene dinamicamente los modelos disponibles del proveedor seleccionado

Los secretos se almacenan en el llavero del sistema operativo cuando esta disponible, con respaldo en un archivo cifrado en `~/.config/iulita/encryption.key`.

### Iniciar la Consola TUI (Modo Predeterminado)

```bash
iulita
```

Esto inicia la TUI interactiva de pantalla completa. Escribe mensajes, usa `/help` para ver los comandos disponibles.

**Comandos de consola:**
| Comando | Descripcion |
|---------|-------------|
| `/help` | Mostrar comandos disponibles |
| `/status` | Mostrar conteo de habilidades, costo diario, tokens de sesion |
| `/compact` | Comprimir manualmente el historial del chat |
| `/clear` | Limpiar el historial del chat en memoria |
| `/quit` / `/exit` | Salir de la aplicacion |

**Atajos de teclado:**
- `Enter` — Enviar mensaje
- `Ctrl+C` — Salir
- `Shift+Enter` — Nueva linea en el mensaje

### Iniciar en Modo Servidor

Para ejecutar como servicio en segundo plano con Telegram, Web Chat y panel de control:

```bash
iulita --server
```

O equivalentemente:
```bash
iulita -d
```

El panel de control es accesible en `http://localhost:8080` (configurable via `server.address`).

## Configuracion

Todas las opciones estan en `config.toml` (opcional — la instalacion local sin configuracion funciona solo con una clave API en el llavero). Cada opcion puede ser sobreescrita via variables de entorno con el prefijo `IULITA_`.

### Ubicacion de Archivos (compatible con XDG)

| Plataforma | Configuracion | Datos | Cache | Logs |
|------------|---------------|-------|-------|------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

Sobreescribe todas las rutas con la variable de entorno `IULITA_HOME`.

### Variables de Entorno Principales

| Variable | Descripcion |
|----------|-------------|
| `IULITA_CLAUDE_API_KEY` | Clave API de Anthropic (requerida para Claude) |
| `IULITA_TELEGRAM_TOKEN` | Token del bot de Telegram |
| `IULITA_CLAUDE_MODEL` | ID del modelo de Claude |
| `IULITA_STORAGE_PATH` | Ruta de la base de datos SQLite |
| `IULITA_SERVER_ADDRESS` | Direccion de escucha del panel de control (`:8080`) |
| `IULITA_PROXY_URL` | Proxy HTTP/SOCKS5 para todas las solicitudes |
| `IULITA_JWT_SECRET` | Clave de firma JWT (auto-generada si no se establece) |
| `IULITA_HOME` | Sobreescribir todas las rutas XDG |

Consulta [`config.toml.example`](../../config.toml.example) para la referencia completa con todas las configuraciones de habilidades.

## Referencia del CLI

| Comando / Flag | Descripcion |
|----------------|-------------|
| `iulita` | Iniciar consola TUI interactiva (predeterminado) |
| `iulita --server` / `-d` | Ejecutar como servidor headless |
| `iulita init` | Asistente de configuracion interactivo |
| `iulita init --print-defaults` | Imprimir config.toml predeterminado |
| `iulita --doctor` | Ejecutar verificaciones de diagnostico |
| `iulita --version` / `-v` | Imprimir version y salir |

## Verificacion Rapida

Despues de la configuracion, verifica que todo funcione:

```bash
# Verificar diagnosticos
iulita --doctor

# Iniciar TUI
iulita

# Escribe: "remember that my favorite color is blue"
# Luego: "what is my favorite color?"
```

Si el asistente recuerda correctamente "blue", la memoria esta funcionando de extremo a extremo.

## Siguientes Pasos

- [Arquitectura](architecture.md) — entender como esta construido el sistema
- [Memoria y Perspectivas](memory-and-insights.md) — como funciona el almacenamiento de datos y las referencias cruzadas
- [Canales](channels.md) — configurar Telegram, Web Chat o personalizar la TUI
- [Habilidades](skills.md) — explorar las mas de 20 herramientas disponibles
- [Configuracion](configuration.md) — inmersion profunda en todas las opciones de configuracion
- [Despliegue](deployment.md) — Docker, Kubernetes y configuracion para produccion
