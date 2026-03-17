# Habilidades

Las habilidades son herramientas que el asistente puede invocar durante las conversaciones. Cada habilidad expone una o mas herramientas al LLM con un nombre, descripcion y esquema de entrada JSON.

## Interfaz de Habilidad

```go
type Skill interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage  // nil for text-only skills
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

**Interfaces opcionales:**
- `CapabilityAware` — `RequiredCapabilities() []string`: la habilidad se excluye si falta alguna capacidad
- `ConfigReloadable` — `OnConfigChanged(key, value string)`: llamado en cambio de configuracion en tiempo de ejecucion
- `ApprovalDeclarer` — `ApprovalLevel() ApprovalLevel`: requisito de aprobacion

## Niveles de Aprobacion

| Nivel | Comportamiento | Usado Por |
|-------|----------------|-----------|
| `ApprovalAuto` | Ejecutar inmediatamente (predeterminado) | La mayoria de las habilidades |
| `ApprovalPrompt` | El usuario debe confirmar en el chat | Ejecutor Docker |
| `ApprovalManual` | El administrador debe confirmar | Shell exec |

El flujo de aprobacion es **no bloqueante**: la habilidad devuelve "awaiting approval" al LLM. El siguiente mensaje del usuario se verifica contra el vocabulario de aprobacion sensible al idioma (si/no en 6 idiomas).

## Habilidades Integradas

### Grupo de Memoria

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `remember` | `content` | Almacenar un dato. Verifica duplicados via FTS. Dispara auto-embedding. |
| `recall` | `query`, `limit` | Buscar datos via FTS5. Aplica decaimiento temporal + re-ranking MMR. Refuerza datos accedidos. |
| `forget` | `id` | Eliminar un dato por ID. En cascada a tablas FTS y vectoriales. |

Consulta [Memoria y Perspectivas](memory-and-insights.md) para detalles completos.

### Grupo de Perspectivas

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `list_insights` | `limit` | Listar perspectivas recientes con puntuaciones de calidad |
| `dismiss_insight` | `id` | Eliminar una perspectiva |
| `promote_insight` | `id` | Extender o eliminar la expiracion de una perspectiva |

### Busqueda Web y Fetch

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `websearch` | `query`, `count` | Busqueda web via API Brave + respaldo DuckDuckGo. 1-10 resultados. |
| `webfetch` | `url` | Obtener y resumir una pagina web. Usa go-readability para extraccion de contenido. Protegido contra SSRF. |

La cadena de busqueda web es `Brave → DuckDuckGo` via `FallbackSearcher`. DuckDuckGo no necesita clave API, por lo que la busqueda web siempre funciona.

### Directivas

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `directives` | `action`, `content` | Gestionar instrucciones personalizadas persistentes (set/get/clear). Cargadas en el prompt del sistema. |

### Recordatorios

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `reminders` | `action`, `title`, `due_at`, `timezone`, `id` | Crear/listar/eliminar recordatorios basados en tiempo. Entregados por el planificador. |

### Fecha/Hora

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `datetime` | `timezone` | Fecha actual, hora, nombre de zona horaria, timestamp Unix. Sin dependencias externas. |

### Clima

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `weather` | `location`, `days` | Pronosticos del clima (1-16 dias). Resolucion interactiva de ubicacion. |

**Cadena de backends**: Open-Meteo (principal, gratuito) → wttr.in (respaldo, gratuito) → OpenWeatherMap (opcional, requiere clave).

Caracteristicas:
- Resolucion interactiva de ubicacion via prompts de canal (teclado inline de Telegram, botones de WebChat, opciones numeradas de Consola)
- Soporte de geocodificacion cirilica
- Pronosticos de varios dias con descripciones de codigos meteorologicos WMO (traducidos en 6 idiomas)
- La salida se adapta a las capacidades del canal (markdown vs texto plano)

### Geolocalizacion

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `geolocation` | `ip` | Geolocalizacion basada en IP. Auto-detecta IP publica. |

Cadena de proveedores: ipinfo.io (con clave) → ip-api.com → ipapi.co. Valida que la IP sea publica (bloquea RFC1918, loopback, etc.).

### Tipo de Cambio

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `exchange_rate` | `from`, `to`, `amount` | Tipos de cambio de moneda. Mas de 160 monedas. No requiere clave API. |

### Shell Exec

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `shell_exec` | `command`, `args` | Ejecucion de comandos shell en sandbox. **ApprovalManual** (requiere confirmacion del administrador). |

**Medidas de seguridad:**
- Solo lista blanca: solo pueden ejecutarse los binarios en `AllowedBins`
- `ForbiddenPaths` verificados en argumentos
- Rechaza recorrido de ruta `..`
- Salida maxima de 16KB
- Directorio de ejecucion predeterminado: `os.TempDir()`

### Delegate

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `delegate` | `prompt`, `provider` | Enrutar un sub-prompt a un proveedor LLM secundario (ej., Ollama para tareas baratas). |

### Lector de PDF

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `pdf_read` | `url` | Obtener y leer documentos PDF. Valida los bytes magicos `%PDF-`. |

### Establecer Idioma

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `set_language` | `language` | Cambiar idioma de la interfaz. Acepta codigos BCP-47 o nombres de idiomas (English/Russian). |

Actualiza `user_channels.locale` en la base de datos. El mensaje de confirmacion esta en el **nuevo** idioma.

### Google Workspace

| Herramienta | Descripcion |
|-------------|-------------|
| `google_auth` | Inicio del flujo OAuth2, listado de cuentas |
| `google_calendar` | Listar/crear/actualizar/eliminar eventos, disponibilidad |
| `google_contacts` | Listar contactos, consultas de cumpleanos |
| `google_mail` | Listar/leer/buscar Gmail (solo lectura) |
| `google_tasks` | CRUD en Google Tasks |

Requiere configuracion OAuth2 via el panel de control. Soporte multi-cuenta.

### Todoist

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `todoist` | `action`, ... | Gestion completa de tareas Todoist. 34 acciones. |

**Acciones**: create, list, get, update, complete, reopen, delete, move, quick_add, filter, completed history. Soporta prioridades (P1-P4), fechas limite, fechas/horas de vencimiento, recurrencia, etiquetas, proyectos, secciones, subtareas, comentarios.

Usa la API Unificada v1 (`api.todoist.com/api/v1`). Autenticacion por token API.

### Tareas Unificadas

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `tasks` | `action`, `provider`, ... | Agrega Todoist + Google Tasks + Craft Tasks. |

**Acciones**: `overview` (todos los proveedores), `list`, `create`, `complete`, `provider` (passthrough).

### Craft

| Herramienta | Descripcion |
|-------------|-------------|
| `craft_read` | Leer documentos de Craft |
| `craft_write` | Escribir documentos de Craft |
| `craft_tasks` | Gestionar tareas de Craft |
| `craft_search` | Buscar documentos de Craft |

### Orquestacion Multi-Agente

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `orchestrate` | `agents[]`, `timeout`, `max_tokens` | Lanzar multiples sub-agentes especializados en paralelo. |

**Tipos de agente**: `researcher`, `analyst`, `planner`, `coder`, `summarizer`, `generic` — cada uno con prompt de sistema especializado y subconjunto de herramientas.

Los sub-agentes se ejecutan en paralelo via `errgroup`, comparten un presupuesto atomico de tokens y no pueden generar mas sub-agentes (profundidad maxima = 1). Las habilidades que requieren aprobacion (ApprovalManual, ApprovalPrompt) se filtran de las listas de herramientas.

Ver [Orquestacion Multi-Agente](multi-agent.md) para detalles completos.

### Gestion de Habilidades

| Herramienta | Entrada | Descripcion |
|-------------|---------|-------------|
| `skills` | `action`, ... | Listar/habilitar/deshabilitar/obtener_config/establecer_config habilidades via chat. Solo administrador para mutaciones. |

## Habilidades de Texto (Inyeccion en el Prompt del Sistema)

Las habilidades pueden ser puramente una inyeccion en el prompt del sistema — sin metodo `Execute`, sin definicion de herramienta para el LLM.

### Formato SKILL.md

```yaml
---
name: my-skill
description: What this skill does
capabilities: [optional-cap]
config_keys: [skills.my-skill.setting]
secret_keys: [skills.my-skill.api_key]
force_triggers: [keyword1, keyword2]
---

Markdown instructions injected into the system prompt.
```

- Las habilidades con `InputSchema() == nil` contribuyen solo a `staticSystemPrompt()`
- El cuerpo markdown se convierte en parte de las instrucciones del LLM
- `force_triggers` fuerza llamadas a herramientas especificas cuando las palabras clave coinciden con el mensaje del usuario

### Rutas de Carga

1. **Embebidas en paquetes Go**: `//go:embed SKILL.md` + `LoadManifestFromFS()`
2. **Directorio externo**: `LoadExternalManifests(dir)` desde `~/.local/share/iulita/skills/`
3. **Externas instaladas**: via marketplace ClawhHub o URL

## Gestor de Habilidades (Habilidades Externas)

### Marketplace ClawhHub

Instalar habilidades de la comunidad desde [ClawhHub](https://clawhub.ai):

```
# Via panel de control: Skills → External → Search
# Via chat: instalar desde URL
```

La API del marketplace (`clawhub.ai/api/v1`) soporta:
- `Search(query)` — resultados ordenados por relevancia BM25
- `Resolve(slug)` — obtener URL de descarga y checksum
- `Download()` — descargar archivo (maximo 50MB)

### Flujo de Instalacion

1. Verificar limite `MaxInstalled`
2. Resolver desde la fuente (ClawhHub, URL o directorio local)
3. Validar slug contra `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`
4. Descargar y verificar checksum SHA-256
5. Parsear `SKILL.md` con frontmatter extendido
6. Validar nivel de aislamiento contra la configuracion
7. Escanear por patrones de inyeccion de prompts
8. Mover atomicamente al directorio de instalacion
9. Registrar en el registro de habilidades

### Niveles de Aislamiento

| Nivel | Comportamiento | Aprobacion |
|-------|----------------|------------|
| `text_only` | Solo inyeccion de prompt del sistema | Auto |
| `shell` | Ejecucion shell via `ShellExecutor` | Manual |
| `docker` | Ejecucion en contenedor Docker | Prompt |
| `wasm` | Runtime WebAssembly | Auto |

**Cadena de respaldo**: Si una habilidad requiere shell pero shell_exec esta deshabilitado, recurre a `webfetchProxySkill` (extrae URLs del prompt y las obtiene), luego a `text_only`.

### Seguridad

- Validacion de slug previene recorrido de ruta
- Verificacion de checksum para descargas remotas
- Validacion de nivel de aislamiento contra la configuracion (`AllowShell`, `AllowDocker`, `AllowWASM`)
- Deteccion de archivos de codigo: rechaza habilidades con archivos `.py`/`.js`/`.go`/etc. a menos que esten correctamente aisladas
- Escaneo de inyeccion de prompts: advierte sobre patrones sospechosos en el cuerpo de la habilidad

## Recarga en Caliente de Habilidades

Las habilidades soportan cambios de configuracion en tiempo de ejecucion sin reinicio:

1. Las habilidades llaman `RegisterKey()` al inicio para declarar sus claves de configuracion
2. El editor de configuracion del panel llama `Store.Set()` que publica `ConfigChanged`
3. El bus de eventos despacha a `registry.DispatchConfigChanged()`
4. Las habilidades que implementan `ConfigReloadable` reciben el nuevo valor

**Regla critica**: Las habilidades DEBEN registrarse incondicionalmente (no dentro de `if apiKey != ""`). Usar control por capacidades en su lugar: `AddCapability("web")` cuando la clave API esta presente, `RemoveCapability("web")` cuando se elimina.

## Disparadores Forzados

Las habilidades pueden declarar palabras clave que fuerzan la invocacion de herramientas:

```yaml
force_triggers: [weather, погода, météo]
```

Cuando el mensaje del usuario contiene una palabra clave disparadora (coincidencia de subcadena sin importar mayusculas/minusculas), `ForceTool` se establece en la solicitud LLM para la iteracion 0. Esto asegura que el LLM siempre llame a la herramienta en lugar de responder desde los datos de entrenamiento.

Los disparadores de memoria (ej., "remember", "запомни") se configuran por separado y fuerzan la herramienta `remember`.
