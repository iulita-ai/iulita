# Configuracion

Iulita usa un sistema de configuracion por capas que soporta instalacion local sin configuracion mientras permite personalizacion completa para despliegues avanzados.

## Capas de Configuracion

La configuracion se carga en orden, con capas posteriores sobreescribiendo las anteriores:

```
1. Valores predeterminados compilados (siempre presentes)
2. Archivo TOML (~/.config/iulita/config.toml, opcional)
3. Variables de entorno (prefijo IULITA_*)
4. Secretos del llavero (macOS Keychain, Linux SecretService)
5. Sobreescrituras de BD (tabla config_overrides, editable en tiempo de ejecucion)
```

### Capa 1: Valores Predeterminados Compilados

`DefaultConfig()` proporciona una configuracion funcional sin archivos externos. Todos los IDs de modelo, timeouts, configuraciones de memoria y flags de funcionalidades tienen valores predeterminados sensatos. El sistema funciona de inmediato solo con una clave API.

### Capa 2: Archivo TOML

Opcional. Ubicado en `~/.config/iulita/config.toml` (o `$IULITA_HOME/config.toml`).

El archivo TOML se **omite** si:
- No existe archivo en la ruta de configuracion
- Existe el archivo sentinel `db_managed` (modo asistente web)

Consulta `config.toml.example` para la referencia completa.

### Capa 3: Variables de Entorno

Todas las configuraciones pueden sobreescribirse via variables de entorno `IULITA_*`:

```
IULITA_CLAUDE_API_KEY      → claude.api_key
IULITA_TELEGRAM_TOKEN      → telegram.token
IULITA_CLAUDE_MODEL        → claude.model
IULITA_STORAGE_PATH        → storage.path
IULITA_SERVER_ADDRESS      → server.address
IULITA_PROXY_URL           → proxy.url
```

**Regla de mapeo**: eliminar prefijo `IULITA_`, minusculas, reemplazar `_` con `.`.

### Capa 4: Secretos del Llavero

Los secretos se almacenan de forma segura en el llavero del sistema operativo:

| Secreto | Variable de Entorno | Cuenta del Llavero |
|---------|--------------------|--------------------|
| Clave API de Claude | `IULITA_CLAUDE_API_KEY` | `claude-api-key` |
| Token de Telegram | `IULITA_TELEGRAM_TOKEN` | `telegram-token` |
| Secreto JWT | `IULITA_JWT_SECRET` | `jwt-secret` |
| Clave de cifrado de config | `IULITA_CONFIG_KEY` | `config-encryption-key` |

**Cadena de respaldo** para cada secreto: variable de entorno → llavero → archivo (solo para clave de cifrado) → auto-generar (solo para JWT).

El llavero usa `zalando/go-keyring`:
- **macOS**: Keychain
- **Linux**: SecretService (GNOME Keyring, KDE Wallet)
- **Respaldo**: archivo cifrado en `~/.config/iulita/encryption.key`

### Capa 5: Sobreescrituras de BD (Config Store)

Configuracion editable en tiempo de ejecucion almacenada en la tabla SQLite `config_overrides`. Gestionada via:
- Editor de configuracion del panel de control
- Herramienta `skills` basada en chat (accion `set_config`)
- Asistente de configuracion web

**Caracteristicas:**
- Cifrado AES-256-GCM para valores secretos
- Recarga en caliente inmediata via bus de eventos
- Registro de auditoria (quien cambio que, cuando)
- Claves inmutables de solo reinicio protegidas

## Rutas Compatibles con XDG

| Plataforma | Configuracion | Datos | Cache | Estado |
|------------|---------------|-------|-------|--------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

**Sobreescribir**: establecer `IULITA_HOME` para usar una raiz personalizada con subdirectorios `data/`, `cache/`, `state/`.

### Rutas Derivadas

| Ruta | Ubicacion |
|------|-----------|
| Archivo de configuracion | `{ConfigDir}/config.toml` |
| Base de datos | `{DataDir}/iulita.db` |
| Modelos ONNX | `{DataDir}/models/` |
| Habilidades | `{DataDir}/skills/` |
| Habilidades externas | `{DataDir}/external-skills/` |
| Archivo de log | `{StateDir}/iulita.log` |
| Clave de cifrado | `{ConfigDir}/encryption.key` |

## Secciones de Configuracion

### App

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `app.system_prompt` | (incorporado) | Prompt base del sistema para el asistente |
| `app.context_window` | 200000 | Presupuesto de tokens para el contexto |
| `app.request_timeout` | 120s | Timeout por mensaje |

### Claude (LLM Principal)

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `claude.api_key` | — | Clave API de Anthropic (requerida) |
| `claude.model` | `claude-sonnet-4-5-20250929` | ID del modelo |
| `claude.max_tokens` | 8192 | Tokens maximos de salida |
| `claude.base_url` | — | Sobreescribir URL base de la API |
| `claude.thinking` | 0 | Presupuesto de pensamiento extendido (0 = deshabilitado) |

### Ollama (LLM Local)

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `ollama.url` | `http://localhost:11434` | URL del servidor Ollama |
| `ollama.model` | `llama3` | Nombre del modelo |

### OpenAI (Compatibilidad)

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `openai.api_key` | — | Clave API |
| `openai.model` | `gpt-4` | ID del modelo |
| `openai.base_url` | `https://api.openai.com/v1` | URL base de la API |

### Telegram

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `telegram.token` | — | Token del bot (recargable en caliente) |
| `telegram.allowed_ids` | `[]` | Lista blanca de IDs de usuario (vacio = todos) |
| `telegram.debounce_window` | 2s | Ventana de fusion de mensajes |

### Almacenamiento

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `storage.path` | `{DataDir}/iulita.db` | Ruta de la base de datos SQLite (solo reinicio) |

### Servidor

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `server.enabled` | true | Habilitar servidor del panel de control |
| `server.address` | `:8080` | Direccion de escucha (solo reinicio) |

### Autenticacion

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `auth.jwt_secret` | (auto-generado) | Clave de firma JWT |
| `auth.token_ttl` | 24h | TTL del token de acceso |
| `auth.refresh_ttl` | 7d | TTL del token de refresco |

### Proxy

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `proxy.url` | — | Proxy HTTP/SOCKS5 (solo reinicio) |

### Memoria

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `skills.memory.half_life_days` | 30 | Vida media del decaimiento temporal |
| `skills.memory.mmr_lambda` | 0 | Diversidad MMR (0.7 recomendado) |
| `skills.memory.vector_weight` | 0 | Peso de busqueda hibrida |
| `skills.memory.triggers` | `[]` | Palabras clave disparadoras de memoria |

### Perspectivas

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `skills.insights.min_facts` | 20 | Datos minimos para generacion |
| `skills.insights.max_pairs` | 6 | Pares maximos de clusters por ejecucion |
| `skills.insights.ttl` | 720h | Expiracion de perspectivas (30 dias) |
| `skills.insights.interval` | 24h | Frecuencia de generacion |
| `skills.insights.quality_threshold` | 0 | Puntuacion minima de calidad |

### Embedding

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `embedding.enabled` | true | Habilitar embeddings ONNX |
| `embedding.model` | `KnightsAnalytics/all-MiniLM-L6-v2` | Nombre del modelo |

### Planificador

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `scheduler.enabled` | true | Habilitar planificador de tareas |
| `scheduler.worker_token` | — | Token Bearer para workers remotos |

### Costos

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `cost.daily_limit_usd` | 0 | Limite de costo diario (0 = ilimitado) |

### Cache

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `cache.enabled` | false | Habilitar cache de respuestas |
| `cache.ttl` | 60m | TTL del cache |
| `cache.max_items` | 1000 | Respuestas maximas cacheadas |

### Metricas

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `metrics.enabled` | false | Habilitar metricas Prometheus |
| `metrics.address` | `:9090` | Direccion del servidor de metricas |

## Asistente de Configuracion

### Asistente CLI (`iulita init`)

Configuracion interactiva que guia a traves de:
1. Seleccion de proveedor LLM (Claude/OpenAI/Ollama, seleccion multiple)
2. Ingreso de clave API (almacenada en llavero)
3. Integraciones opcionales (Telegram, proxy, embedding)
4. Seleccion de modelo (obtencion dinamica del proveedor)

Los secretos van al llavero; los no secretos van a `config.toml`.

### Asistente de Configuracion Web (Docker)

Para despliegues Docker sin acceso a terminal:

1. El servidor arranca en **modo configuracion** cuando no hay LLM configurado y el asistente no se ha completado
2. Modo solo panel de control (sin habilidades, planificador o canales)
3. Asistente de 5 pasos: Bienvenida/Importar → Proveedor → Config → Funcionalidades → Completar
4. Soporte de importacion TOML (pegar configuracion existente)
5. Crea archivo sentinel `db_managed` (deshabilita carga de TOML)
6. Establece `_system.wizard_completed` en config_overrides

## Recarga en Caliente

Estas configuraciones pueden cambiar en tiempo de ejecucion sin reinicio:

| Configuracion | Disparador | Mecanismo |
|---------------|------------|-----------|
| Modelo/tokens/clave de Claude | Editor de config del panel | `UpdateModel()`/`UpdateMaxTokens()`/`UpdateAPIKey()` |
| Token de Telegram | Editor de config del panel | `channelmgr.UpdateConfigToken()` → reiniciar instancia |
| Habilitar/deshabilitar habilidad | Panel o chat | `registry.EnableSkill()`/`DisableSkill()` |
| Config de habilidad (claves API) | Editor de config del panel | `ConfigReloadable.OnConfigChanged()` |
| Prompt del sistema | Editor de config del panel | `asst.SetSystemPrompt()` |
| Presupuesto de pensamiento | Editor de config del panel | `asst.SetThinkingBudget()` |

### Configuraciones de Solo Reinicio

Estas requieren un reinicio completo:
- `storage.path`
- `server.address`
- `proxy.url`
- `security.config_key_env`

## Cifrado AES-256-GCM

Los valores secretos de configuracion en la BD estan cifrados:

1. **Fuente de la clave**: env `IULITA_CONFIG_KEY` → llavero → archivo auto-generado
2. **Algoritmo**: AES-256-GCM (cifrado autenticado)
3. **Formato**: `base64(12-byte-nonce ‖ ciphertext)`
4. **Auto-cifrado**: las claves declaradas como `secret_keys` en SKILL.md siempre se cifran
5. **Sin fugas**: la API del panel devuelve valores vacios para claves cifradas
