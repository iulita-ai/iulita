# Proveedores LLM

Iulita soporta multiples proveedores LLM a traves de una arquitectura basada en decoradores. Los proveedores pueden componerse en cadenas con capas de reintentos, respaldo, cache, enrutamiento y clasificacion.

## Interfaz de Proveedor

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

## Request / Response

### Estructura del Request

```go
Request {
    StaticSystemPrompt  string          // cacheado por Claude (base + prompts de habilidades)
    SystemPrompt        string          // por mensaje (hora, datos, directivas)
    History             []ChatMessage   // historial de conversacion
    Message             string          // mensaje actual del usuario
    Images              []ImageAttachment
    Documents           []DocumentAttachment
    Tools               []ToolDefinition
    ToolExchanges       []ToolExchange  // rondas de herramientas acumuladas en este turno
    ThinkingBudget      int64           // tokens de pensamiento extendido (0 = deshabilitado)
    ForceTool           string          // forzar una llamada a herramienta especifica
    RouteHint           string          // pista para el proveedor de enrutamiento
}
```

**Diseno clave**: el prompt del sistema se divide en `StaticSystemPrompt` (estable, cacheable) y `SystemPrompt` (dinamico, por mensaje). Los proveedores no-Claude usan `FullSystemPrompt()` que concatena ambos.

### Estructura del Response

```go
Response {
    Content    string
    ToolCalls  []ToolCall
    Usage      Usage {
        InputTokens              int
        OutputTokens             int
        CacheReadInputTokens     int
        CacheCreationInputTokens int
    }
}
```

## Proveedor Claude

El proveedor principal, usando el SDK oficial `anthropic-sdk-go`.

### Caracteristicas

- **Cache de prompts**: `StaticSystemPrompt` recibe `cache_control: ephemeral` — Claude cachea este bloque entre solicitudes, reduciendo costos de tokens de entrada
- **Streaming**: `CompleteStream` usa la API de streaming con procesamiento de `ContentBlockDeltaEvent`
- **Pensamiento extendido**: cuando `ThinkingBudget > 0`, se agrega configuracion de pensamiento y se aumentan los tokens maximos
- **ForceTool**: usa `ToolChoiceParamOfTool(name)` para forzar una herramienta especifica (desactiva pensamiento — restriccion de la API)
- **Deteccion de desbordamiento de contexto**: verifica mensajes de error para "prompt is too long" / "context_length_exceeded" y envuelve con el sentinel `ErrContextTooLarge`
- **Soporte de documentos**: archivos PDF via `Base64PDFSourceParam`, archivos de texto via `PlainTextSourceParam`
- **Soporte de imagenes**: imagenes codificadas en base64 con tipo de media
- **Recargable en caliente**: modelo, tokens maximos y clave API pueden actualizarse en tiempo de ejecucion via `sync.RWMutex`

### Cache de Prompts

La division de prompt estatico/dinamico es la clave para un uso eficiente de Claude:

```
Bloque 1: StaticSystemPrompt (cache_control: ephemeral)
  ├── Prompt base del sistema (personalidad, instrucciones)
  └── Prompts del sistema de habilidades (de todas las habilidades habilitadas)

Bloque 2: SystemPrompt (sin control de cache)
  ├── ## Current Time
  ├── ## User Directives
  ├── ## User Profile (tech facts)
  ├── ## Remembered Facts
  ├── ## Insights
  └── ## Language directive (si no es ingles)
```

El Bloque 1 es cacheado por Claude entre solicitudes (cuesta `cache_creation_input_tokens` en el primer uso, `cache_read_input_tokens` en accesos posteriores). El Bloque 2 cambia en cada mensaje y nunca se cachea.

### Streaming

El streaming se usa solo cuando `len(req.Tools) == 0` (el asistente desactiva el streaming durante el bucle agentivo de uso de herramientas). El bucle de eventos de streaming procesa:

- `ContentBlockDeltaEvent` con `type == "text_delta"` → llama `callback(chunk)` y acumula
- `MessageStartEvent` → captura tokens de entrada + metricas de cache
- `MessageDeltaEvent` → captura tokens de salida

### Recuperacion de Desbordamiento de Contexto

Cuando la API de Claude devuelve un error de desbordamiento de contexto:

1. `isContextOverflowError(err)` lo envuelve como `llm.ErrContextTooLarge`
2. El bucle agentivo del asistente lo captura via `llm.IsContextTooLarge(err)`
3. Si no se ha comprimido en este turno: compresion forzada del historial y reintento (`i--`)
4. Si ya se comprimio: propagar el error

### Configuracion

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `claude.api_key` | — | Clave API de Anthropic (requerida) |
| `claude.model` | `claude-sonnet-4-5-20250929` | ID del modelo |
| `claude.max_tokens` | 8192 | Tokens maximos de salida |
| `claude.base_url` | — | Sobreescribir URL base de la API |
| `claude.thinking` | 0 | Presupuesto de pensamiento extendido (0 = deshabilitado) |

## Proveedor Ollama

Proveedor LLM local para desarrollo y tareas en segundo plano.

### Limitaciones

- **Sin soporte de herramientas** — devuelve error si `len(req.Tools) > 0`
- **Sin streaming** — `CompleteStream` no esta implementado
- Usa `FullSystemPrompt()` (sin beneficio de cache)

### Casos de Uso

- Desarrollo local sin costos de API
- Tareas de delegacion en segundo plano (traducciones, resumenes)
- Clasificador barato para el `ClassifyingProvider`

### API

Llama `POST /api/chat` con mensajes en formato compatible con OpenAI. `ListModels()` consulta `GET /api/tags` para descubrimiento de modelos.

### Configuracion

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `ollama.url` | `http://localhost:11434` | URL del servidor Ollama |
| `ollama.model` | `llama3` | Nombre del modelo |

## Proveedor OpenAI

Cliente REST compatible con OpenAI. Funciona con cualquier servicio compatible con OpenAI (Together AI, Azure, etc.).

### Limitaciones

- **Sin soporte de herramientas** — igual que Ollama
- Usa `FullSystemPrompt()`

### Configuracion

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `openai.api_key` | — | Clave API |
| `openai.model` | `gpt-4` | ID del modelo |
| `openai.base_url` | `https://api.openai.com/v1` | URL base de la API |

## Proveedor de Embeddings ONNX

Modelo de embeddings local en Go puro para busqueda vectorial.

- **Modelo**: `KnightsAnalytics/all-MiniLM-L6-v2` (384 dimensiones)
- **Runtime**: `knights-analytics/hugot` — ONNX en Go puro (sin CGo)
- **Seguridad de hilos**: `sync.Mutex` (el pipeline de hugot no es seguro para hilos)
- **Cache**: Se descarga una vez a `~/.local/share/iulita/models/`
- **Normalizacion**: Vectores de salida normalizados L2 (listos para similitud coseno)

Consulta [Memoria y Perspectivas](memory-and-insights.md#embeddings) para detalles sobre como se usan los embeddings.

## Decoradores de Proveedores

### RetryProvider

Envuelve cualquier proveedor con reintento con backoff exponencial:

- **Intentos maximos**: 3
- **Retraso base**: 500ms
- **Retraso maximo**: 8s
- **Jitter**: multiplicador aleatorio 0.5-1.5x
- **Codigos reintentables**: 429, 500, 502, 503, 529 (Anthropic sobrecargado)
- **No reintentable**: 4xx (excepto 429), desbordamiento de contexto

### FallbackProvider

Prueba proveedores en orden, devuelve el primer exito. Util para cadenas de respaldo `Claude → OpenAI`.

### CachingProvider

Cachea respuestas LLM por hash de entrada:

- **Clave**: SHA-256 de `systemPrefix[:200] + "|" + message`
- **TTL**: 60 minutos (configurable)
- **Entradas maximas**: 1000 (expulsion LRU)
- **Omitir**: solicitudes con herramientas o intercambios de herramientas (no deterministicas)
- **Almacenamiento**: tabla `response_cache` en SQLite

### CachedEmbeddingProvider

Cachea embeddings por texto:

- **Clave**: SHA-256 del texto de entrada
- **Entradas maximas**: 10,000 (expulsion LRU)
- **Procesamiento por lotes**: los fallos de cache se agrupan para una unica llamada al proveedor
- **Almacenamiento**: tabla `embedding_cache` en SQLite

### RoutingProvider

Enruta a proveedores nombrados por `req.RouteHint`. Tambien parsea el prefijo `hint:<name> <message>` en el mensaje del usuario. Delega `CompleteStream` al proveedor resuelto si es un `StreamingProvider`.

### ClassifyingProvider

Envuelve un `RoutingProvider`. En cada solicitud:

1. Envia un prompt de clasificacion a un proveedor barato (Ollama): "Classify: simple/complex/creative"
2. Establece `RouteHint` basado en la clasificacion
3. Enruta al proveedor apropiado

Usa el predeterminado como respaldo en caso de error del clasificador.

### XMLToolProvider

Para proveedores sin llamadas a herramientas nativas (Ollama, OpenAI):

1. Inyecta bloque XML `<available_tools>` en el prompt del sistema
2. Agrega instrucciones: "To use a tool, respond with `<tool_use name="..."><input>{...}</input></tool_use>`"
3. Elimina `Tools` de la solicitud
4. Parsea llamadas a herramientas XML de la respuesta usando regex

## Ensamblaje de la Cadena de Proveedores

La cadena se construye en `cmd/iulita/main.go`:

```
Claude Provider
    └→ Retry Provider
        └→ [Opcional] Fallback Provider (+ OpenAI)
            └→ [Opcional] Caching Provider
                └→ [Opcional] Routing Provider
                    └→ [Opcional] Classifying Provider (+ Ollama)
```

Cada capa se agrega condicionalmente basado en la configuracion.
