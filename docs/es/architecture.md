# Arquitectura

## Vision General de Alto Nivel

```
Console TUI ─┐
Telegram ────┤
Web Chat ────┼→ Channel Manager → Assistant → LLM Provider Chain
                     ↕                ↕
                 UserResolver      Storage (SQLite)
                                     ↕
                               Scheduler → Worker
                               (insights, analysis, reminders)
                                     ↕
                                Event Bus → Dashboard (WebSocket)
                                          → Prometheus Metrics
                                          → Push Notifications
                                          → Cost Tracker
```

## Principios de Diseno Fundamentales

1. **Memoria basada en hechos** — solo se almacenan datos verificados del usuario, nunca conocimiento alucinado
2. **Consola primero** — la TUI es el modo predeterminado; el modo servidor es opcional
3. **Arquitectura limpia** — modelos de dominio → interfaces → implementaciones → orquestador
4. **Multi-canal, identidad unica** — los datos y perspectivas se comparten entre todos los canales via user_id
5. **Instalacion local sin configuracion** — funciona de inmediato solo con una clave API
6. **Recarga en caliente** — las habilidades, la configuracion e incluso el token de Telegram pueden cambiar en tiempo de ejecucion sin reiniciar

## Mapa de Componentes

| Componente | Paquete | Descripcion |
|------------|---------|-------------|
| Punto de entrada | `cmd/iulita/` | Parseo de CLI, inyeccion de dependencias, apagado gracioso |
| Asistente | `internal/assistant/` | Orquestador: bucle LLM, memoria, compresion, aprobaciones, streaming |
| Canales | `internal/channel/` | Adaptadores de entrada: Consola TUI, Telegram, WebChat |
| Gestor de canales | `internal/channelmgr/` | Ciclo de vida de canales, enrutamiento, recarga en caliente |
| Proveedores LLM | `internal/llm/` | Claude, Ollama, OpenAI, embeddings ONNX |
| Habilidades | `internal/skill/` | Mas de 30 implementaciones de herramientas |
| Gestor de habilidades | `internal/skillmgr/` | Habilidades externas: marketplace ClawhHub, URL, local |
| Marcadores | `internal/bookmark/` | Guardado rapido de respuestas del asistente como datos + refinamiento en segundo plano |
| Almacenamiento | `internal/storage/sqlite/` | SQLite con FTS5, vectores, modo WAL |
| Planificador | `internal/scheduler/` | Cola de tareas con soporte cron/intervalo |
| Panel de control | `internal/dashboard/` | API REST GoFiber + SPA Vue 3 embebida |
| Configuracion | `internal/config/` | Configuracion por capas: predeterminados → TOML → env → llavero → BD |
| Autenticacion | `internal/auth/` | JWT + bcrypt, middleware |
| i18n | `internal/i18n/` | 6 idiomas, catalogos TOML, propagacion de contexto |
| Busqueda web | `internal/web/` | Brave + DuckDuckGo como respaldo, proteccion SSRF |
| Dominio | `internal/domain/` | Modelos de dominio puros |
| Memoria | `internal/memory/` | Clustering TF-IDF, exportacion/importacion de memoria |
| Metricas | `internal/metrics/` | Contadores e histogramas Prometheus |
| Agente | `internal/agent/` | Runner de sub-agentes, orquestador, control de presupuesto |
| Eventos | `internal/eventbus/` | Bus de eventos publicar/suscribir |
| Costos | `internal/cost/` | Seguimiento de costos LLM con limites diarios |
| Limite de tasa | `internal/ratelimit/` | Limitadores de tasa por chat y globales |
| Frontend | `ui/` | SPA Vue 3 + Naive UI + UnoCSS |

## Orden de Inicio

La secuencia de inicio esta estrictamente ordenada para satisfacer las dependencias:

```
1. Parsear argumentos CLI, resolver rutas XDG, asegurar directorios
2. Manejar subcomandos: init, --version, --doctor (salida temprana)
3. Cargar configuracion: predeterminados → TOML → env → llavero
4. Crear logger (modo consola redirige a archivo)
5. Abrir SQLite, ejecutar migraciones
6. Inicializar catalogo i18n (despues de migraciones, antes de habilidades)
7. Inicializar usuario administrador (antes de backfill)
8. BackfillUserIDs (asociar datos heredados con usuarios)
9. Crear almacen de configuracion, cargar sobreescrituras de BD
10. Verificar puerta de modo configuracion (sin LLM + sin asistente = solo configuracion)
11. Validar configuracion
12. Crear servicio de autenticacion
13. Inicializar instancias de canal
14. Crear proveedor de embeddings ONNX (opcional)
15. Construir cadena de proveedores LLM (Claude → reintentos → respaldo → cache → enrutamiento)
16. Registrar todas las habilidades (incondicionalmente — controladas por capacidades)
17. Crear asistente
18. Conectar bus de eventos (recarga de config, metricas, costos, notificaciones)
19. Reproducir sobreescrituras de config de BD (recarga en caliente para credenciales establecidas desde el panel)
20. Crear gestor de canales, planificador, worker
21. Iniciar planificador, worker, bucle de ejecucion del asistente
22. Iniciar servidor del panel de control
23. Iniciar todos los canales
24. Bloquear en senal de apagado
```

## Apagado Gracioso (7 Fases)

```
1. Detener todos los canales (dejar de aceptar nuevos mensajes)
2. Esperar goroutines de fondo del asistente
3. Esperar backfill de embeddings
4. Cerrar proveedor ONNX
5. Apagar bus de eventos (esperar manejadores asincronos)
6. Esperar planificador/worker/panel (timeout de 10s)
7. Cerrar conexion SQLite (ultimo)
```

## Flujo de Mensajes

Cuando un usuario envia un mensaje, esta es la ruta de ejecucion completa:

```
El usuario escribe "remember that I love Go"
    │
    ▼
Canal (Telegram/WebChat/Consola)
    │ construye IncomingMessage con campos especificos de la plataforma
    │ establece bitmask ChannelCaps (streaming, markdown, etc.)
    ▼
UserResolver (solo Telegram/Consola)
    │ mapea identidad de plataforma → UUID de iulita
    │ auto-registra nuevos usuarios si esta permitido
    ▼
Channel Manager
    │ enruta a Assistant.HandleMessage
    ▼
Asistente — Fase 1: Configuracion de Contexto
    │ timeout, rol de usuario, locale, caps → contexto
    │ verificar aprobacion pendiente → ejecutar si aprobado
    ▼
Asistente — Fase 2: Enriquecimiento
    │ guardar mensaje en BD
    │ fondo: TechFactAnalyzer (Cirilico/Latino, longitud del mensaje)
    │ enviar evento de estado "procesando"
    ▼
Asistente — Fase 3: Historial y Compresion
    │ cargar ultimos 50 mensajes
    │ si tokens > 80% de ventana de contexto → comprimir mitad antigua
    ▼
Asistente — Fase 4: Datos de Contexto
    │ cargar directiva, datos recientes, perspectivas relevantes
    │ busqueda hibrida: FTS5 + vectores ONNX + reranking MMR
    │ cargar tech facts (perfil de usuario)
    │ resolver zona horaria
    ▼
Asistente — Fase 5: Construccion del Prompt
    │ prompt estatico = base + prompts de sistema de habilidades (cacheado por Claude)
    │ prompt dinamico = hora + directivas + perfil + datos + perspectivas + idioma
    ▼
Asistente — Fase 6: Deteccion de Herramienta Forzada
    │ palabra clave "remember" → ForceTool = "remember"
    ▼
Asistente — Fase 7: Bucle Agentivo (max 10 iteraciones)
    │ Llamar LLM (streaming si no hay herramientas, sino estandar)
    │ En desbordamiento de contexto → comprimir forzado → reintentar una vez
    │ Si hay llamadas a herramientas:
    │   ├── verificar nivel de aprobacion
    │   ├── ejecutar habilidad
    │   ├── acumular en ToolExchanges
    │   └── siguiente iteracion
    │ Si no hay llamadas a herramientas → devolver respuesta
    ▼
Ejecucion de Habilidad (ej., RememberSkill)
    │ verificacion de duplicados via busqueda FTS
    │ guardar en SQLite → trigger FTS se dispara
    │ fondo: embedding ONNX → fact_vectors
    ▼
Respuesta enviada de vuelta a traves del canal al usuario
```

## Interfaces Clave

### Provider (LLM)

```go
type Provider interface {
    Complete(ctx context.Context, req Request) (Response, error)
}

type StreamingProvider interface {
    Provider
    CompleteStream(ctx context.Context, req Request, callback StreamCallback) (Response, error)
}

type EmbeddingProvider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimensions() int
}
```

### Skill

```go
type Skill interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage  // nil for text-only skills
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

Interfaces opcionales: `CapabilityAware`, `ConfigReloadable`, `ApprovalDeclarer`.

### Channel

```go
type InputChannel interface {
    Start(ctx context.Context, handler MessageHandler) error
}

type MessageHandler func(ctx context.Context, msg IncomingMessage) (string, error)

type StreamingSender interface {
    MessageSender
    StartStream(ctx context.Context, chatID string, replyTo int) (editFn, doneFn func(string), err error)
}

// Opcional — los canales implementan esto para agregar botones de marcador a las respuestas en streaming
type BookmarkStreamingSender interface {
    StreamingSender
    StartStreamWithBookmark(ctx context.Context, chatID string, replyTo int, userID string) (editFn, doneFn func(string), err error)
}
```

### Storage

```go
type Repository interface {
    // Messages
    SaveMessage(ctx, msg) error
    GetHistory(ctx, chatID, limit) ([]ChatMessage, error)

    // Memory
    SaveFact(ctx, fact) error
    SearchFacts(ctx, chatID, query, limit) ([]Fact, error)
    SearchFactsByUser(ctx, userID, query, limit) ([]Fact, error)
    SearchFactsHybridByUser(ctx, userID, query, queryVec, limit) ([]Fact, error)

    // Tasks
    CreateTask(ctx, task) error
    ClaimTask(ctx, workerID, capabilities) (*Task, error)

    // ... 60+ methods total
}
```

## Bus de Eventos

El bus de eventos (`internal/eventbus/`) implementa un patron tipado de publicar/suscribir. Los eventos fluyen entre componentes sin acoplamiento directo:

| Evento | Publicador | Suscriptores |
|--------|------------|--------------|
| `MessageReceived` | Assistant | Metricas, hub WebSocket |
| `ResponseSent` | Assistant | Metricas, hub WebSocket |
| `LLMUsage` | Assistant | Metricas, rastreador de costos |
| `SkillExecuted` | Assistant | Metricas |
| `TaskCompleted` | Worker | Hub WebSocket |
| `TaskFailed` | Worker | Hub WebSocket |
| `FactSaved` | Storage | Hub WebSocket |
| `InsightCreated` | Storage | Hub WebSocket |
| `ConfigChanged` | Config store | Manejador de recarga de config → habilidades |
| `AgentOrchestrationStarted` | Orquestador | Metricas, hub WebSocket |
| `AgentOrchestrationDone` | Orquestador | Metricas, hub WebSocket |

## Cadena de Proveedores LLM

Los proveedores se componen como decoradores:

```
Claude Provider
    └→ Retry Provider (3 intentos, backoff exponencial, 429/5xx)
        └→ Fallback Provider (Claude → OpenAI)
            └→ Caching Provider (clave SHA-256, TTL 60min)
                └→ Routing Provider (despacho basado en RouteHint)
                    └→ Classifying Provider (clasificador Ollama → seleccion de ruta)
```

Para proveedores que no soportan llamadas a herramientas nativas (Ollama, OpenAI), el wrapper `XMLToolProvider` inyecta definiciones de herramientas como XML en el prompt del sistema y parsea las llamadas a herramientas XML de la respuesta.

## Alcance de Datos

Todos los datos estan delimitados por `user_id` para compartir entre canales:

```
User (UUID de iulita)
    ├── user_channels (vinculacion Telegram, vinculacion WebChat, ...)
    ├── chat_messages (de todos los canales)
    ├── facts (compartidos entre canales)
    ├── insights (compartidos entre canales)
    ├── directives (por usuario)
    ├── tech_facts (perfil de comportamiento)
    ├── reminders
    └── todo_items
```

Un usuario que chatea en Telegram puede recordar datos que almaceno a traves de la Consola TUI, porque ambos canales resuelven al mismo `user_id`.

## Estructura del Proyecto

```
cmd/iulita/              # punto de entrada, inyeccion de dependencias, apagado gracioso
internal/
  assistant/             # orquestador (bucle LLM, memoria, compresion, aprobaciones)
  channel/
    console/             # TUI bubbletea
    telegram/            # bot de Telegram
    webchat/             # web chat WebSocket
  bookmark/              # guardado rapido de respuestas del asistente como datos
  channelmgr/            # gestor de ciclo de vida de canales
  config/                # configuracion TOML + env + llavero, asistente de configuracion
  domain/                # modelos de dominio
  auth/                  # autenticacion JWT + bcrypt
  i18n/                  # internacionalizacion (6 idiomas, catalogos TOML)
  llm/                   # proveedores LLM (Claude, Ollama, OpenAI, ONNX)
  scheduler/             # cola de tareas (planificador + worker)
  agent/                 # orquestacion multi-agente (runner, orquestador, presupuesto)
  skill/                 # implementaciones de habilidades
  skillmgr/              # gestor de habilidades externas (ClawhHub, URL, local)
  storage/sqlite/        # repositorio SQLite, FTS5, vectores, migraciones
  dashboard/             # API REST GoFiber + SPA Vue
  web/                   # busqueda web (Brave, DuckDuckGo, proteccion SSRF)
  transcription/         # transcripcion de audio/voz
  doctor/                # verificaciones de diagnostico (flag --doctor)
  memory/                # clustering TF-IDF, exportacion/importacion
  eventbus/              # bus de eventos publicar/suscribir
  cost/                  # seguimiento de costos LLM
  metrics/               # metricas Prometheus
  ratelimit/             # limitacion de tasa
  notify/                # notificaciones push
ui/                      # frontend Vue 3 + Naive UI + UnoCSS
skills/                  # archivos de habilidades de texto (Markdown)
docs/                    # documentacion
```
