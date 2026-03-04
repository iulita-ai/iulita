# Canales

Iulita soporta multiples canales de comunicacion. Cada canal convierte mensajes especificos de la plataforma en un formato universal `IncomingMessage` y los enruta a traves del asistente.

## Capacidades de los Canales

Cada canal declara sus capacidades mediante un bitmask en cada mensaje:

| Capacidad | Consola | Telegram | WebChat |
|-----------|---------|----------|---------|
| Streaming | via bubbletea | Si (basado en edicion) | Si (WebSocket) |
| Markdown | via glamour | Si | HTML |
| Reacciones | No | No | No |
| Botones | No | Si (teclado inline) | Si |
| Indicador de escritura | Si | Si | No |
| HTML | No | No | Si |

Las capacidades son por mensaje (no por canal), permitiendo comportamiento mixto cuando multiples canales comparten un asistente. Las habilidades pueden verificar capacidades via `channel.CapsFrom(ctx)` para adaptar su formato de salida.

## Consola TUI

El modo predeterminado — un chat de terminal de pantalla completa impulsado por [bubbletea](https://github.com/charmbracelet/bubbletea).

### Caracteristicas

- **Diseno de pantalla completa**: viewport (historial del chat) + divisor + textarea (entrada) + barra de estado
- **Renderizado Markdown**: via [glamour](https://github.com/charmbracelet/glamour) con ajuste de palabras adaptativo
- **Streaming**: aparicion de texto en vivo con indicador de spinner
- **Comandos slash**: `/help`, `/status`, `/compact`, `/clear`, `/quit`
- **Prompts interactivos**: opciones numeradas para interacciones de habilidades (ej., seleccion de ubicacion del clima)
- **Deteccion de color de fondo**: adapta el renderizado antes de que bubbletea inicie

### Arquitectura

```
tuiModel (bubbletea)
    ├── viewport.Model (historial de chat desplazable)
    ├── textarea.Model (entrada del usuario)
    ├── statusBar (nombre de habilidad, conteos de tokens, costo)
    └── streamBuf (texto de streaming en vivo)
```

La estructura `console.Channel` contiene un `*tea.Program` protegido por `sync.RWMutex`. El programa bubbletea se ejecuta en su propia goroutine (bloquea `Start()`), mientras que `StartStream`, `SendMessage` y `NotifyStatus` se llaman desde la goroutine del asistente concurrentemente.

### Puente de Streaming

Cuando el asistente transmite una respuesta:

1. `StartStream()` devuelve closures `editFn` y `doneFn`
2. `editFn(text)` envia `streamChunkMsg` a bubbletea (texto completo acumulado)
3. `doneFn(text)` envia `streamDoneMsg` a bubbletea (finaliza y agrega al historial)
4. Todos los mensajes son seguros para hilos via `p.Send()` de bubbletea

### Comandos Slash

| Comando | Descripcion |
|---------|-------------|
| `/help` | Mostrar todos los comandos con descripciones |
| `/status` | Conteos de habilidades, costo diario, tokens de sesion, conteo de mensajes |
| `/compact` | Activar manualmente la compresion del historial (asincrono) |
| `/clear` | Limpiar historial del chat en memoria (solo TUI) |
| `/quit` / `/exit` | Salir de la aplicacion |

### Coexistencia con Modo Servidor

En modo consola, el servidor se ejecuta en segundo plano:
- Los logs se redirigen a `iulita.log` (no stderr, para evitar corrupcion de la TUI)
- El panel de control sigue accesible en la direccion configurada
- Telegram y otros canales se ejecutan junto a la TUI

## Telegram

Bot de Telegram con funcionalidad completa, incluyendo streaming, debouncing y prompts interactivos.

### Configuracion

1. Crear un bot via [@BotFather](https://t.me/BotFather)
2. Establecer el token: `iulita init` (llavero) o variable de entorno `IULITA_TELEGRAM_TOKEN`
3. Opcional: establecer `telegram.allowed_ids` para restringir a IDs de usuario especificos de Telegram

### Caracteristicas

- **Lista blanca de usuarios**: `allowed_ids` restringe quien puede chatear con el bot. Vacio = permitir todos (se registra advertencia)
- **Debouncing de mensajes**: mensajes rapidos del mismo chat se fusionan (ventana configurable)
- **Ediciones de streaming**: las respuestas aparecen incrementalmente via `EditMessageText` (limitado a 1 edicion/1.5s)
- **Fragmentacion de mensajes**: mensajes de mas de 4000 caracteres se dividen en limites de parrafo/linea/palabra, preservando bloques de codigo
- **Hilos de respuesta**: el primer fragmento responde al mensaje del usuario; los siguientes son independientes
- **Indicador de escritura**: accion `ChatTyping` enviada cada 4 segundos durante el procesamiento
- **Monitor de salud**: `GetMe()` llamado cada 60 segundos para detectar problemas de conectividad
- **Prompts interactivos**: teclados inline para interacciones de habilidades (ubicacion del clima, etc.)
- **Soporte de medios**: fotos (tamano mas grande), documentos (limite de 30MB), voz/audio (con transcripcion)
- **Comandos integrados**: `/clear` (limpiar historial), comandos personalizados registrados

### Pipeline de Procesamiento de Mensajes

```
Actualizacion entrante de Telegram
    │
    ├── Callback query? → enrutar al manejador de prompts
    ├── No es un mensaje? → omitir
    ├── Usuario no esta en la lista blanca? → rechazar
    ├── Comando /clear? → manejar directamente
    ├── Comando registrado? → enrutar al manejador
    ├── Prompt activo? → enrutar texto al prompt
    │
    ▼
Construir IncomingMessage
    │ Caps = Streaming | Markdown | Typing | Buttons
    │
    ├── Resolver usuario (plataforma → UUID de iulita)
    ├── Buscar locale en BD
    ├── Descargar medios (foto/documento/voz)
    ├── Verificar limite de tasa
    │
    ▼
Debounce
    │ fusionar mensajes rapidos (texto unido con \n)
    │ temporizador se reinicia con cada nuevo mensaje
    │
    ▼
Handler (Assistant.HandleMessage)
```

### Debouncer

El debouncer fusiona mensajes rapidos del mismo chat para prevenir multiples llamadas LLM:

- Cada `chatID` tiene un buffer con un temporizador `time.AfterFunc`
- Agregar un mensaje reinicia el temporizador
- Cuando el temporizador se dispara, todos los mensajes en buffer se fusionan:
  - Texto unido con `"\n"`
  - Imagenes y documentos concatenados
  - Metadatos del primer mensaje preservados
- Si `debounce_window = 0`, los mensajes se procesan inmediatamente (no bloqueante)
- `flushAll()` procesa buffers restantes durante el apagado

### Fragmentacion de Mensajes

Las respuestas largas se dividen en fragmentos compatibles con Telegram (maximo 4000 caracteres):

1. Intentar dividir en limites de parrafo (`\n\n`)
2. Intentar dividir en limites de linea (`\n`)
3. Intentar dividir en limites de palabra (` `)
4. Division forzada como ultimo recurso
5. **Conciencia de bloques de codigo**: si se divide dentro de un bloque ``` , se cierra con ``` y se reabre en el siguiente fragmento

### Configuracion

| Clave | Predeterminado | Descripcion |
|-------|----------------|-------------|
| `telegram.token` | — | Token del bot (recargable en caliente) |
| `telegram.allowed_ids` | `[]` | Lista blanca de IDs de usuario (vacio = permitir todos) |
| `telegram.debounce_window` | 2s | Ventana de fusion de mensajes |

## WebChat

Web chat basado en WebSocket embebido en el panel de control.

### Protocolo

**Conexion**: WebSocket en `/ws/chat?user_id=<uuid>&username=<name>&chat_id=<optional>`

**Mensajes entrantes** (cliente → servidor):
```json
{
  "text": "user message",
  "chat_id": "web:abc123",
  "prompt_id": "prompt_123_1",       // solo para respuestas de prompts
  "prompt_answer": "option_id"       // solo para respuestas de prompts
}
```

**Mensajes salientes** (servidor → cliente):

| Tipo | Proposito | Campos Clave |
|------|-----------|--------------|
| `message` | Respuesta normal | `text`, `timestamp` |
| `stream_edit` | Actualizacion de streaming | `text`, `message_id`, `timestamp` |
| `stream_done` | Stream finalizado | `text`, `message_id`, `timestamp` |
| `status` | Eventos de procesamiento | `status`, `skill_name`, `success`, `duration_ms` |
| `prompt` | Pregunta interactiva | `text`, `prompt_id`, `options[]` |

### Autenticacion

WebChat **no** usa el UserResolver. El frontend obtiene un token JWT via `/api/auth/login`, extrae `user_id` del payload y lo pasa como parametro de consulta WebSocket. El canal confia en este `user_id` directamente.

### Serializacion de Escritura

Todas las escrituras WebSocket pasan por un `sync.Mutex` por conexion para prevenir panics por escritura concurrente. Cada conexion se rastrea en un mapa `clients[chatID]`.

### Prompts Interactivos

Los prompts usan IDs basados en contadores atomicos: `prompt_<timestamp>_<counter>`. El servidor envia un mensaje `prompt` con opciones; el cliente responde con `prompt_id` y `prompt_answer`. Los prompts pendientes se almacenan en un `sync.Map` con timeout.

## Gestor de Canales

El `channelmgr.Manager` orquesta todas las instancias de canal en tiempo de ejecucion.

### Ciclo de Vida

- **StartAll**: carga todas las instancias de canal desde la BD, inicia cada una en una goroutine
- **StopInstance**: cancela contexto, espera en canal done (timeout de 5s)
- **AddInstance / UpdateInstance**: para instancias creadas/modificadas desde el panel
- **Recarga en caliente**: `UpdateConfigToken(token)` reinicia las instancias de Telegram originadas por configuracion

### Enrutamiento de Mensajes

Cuando el asistente necesita enviar un mensaje proactivo (recordatorio, heartbeat):

1. Buscar que instancia de canal posee el `chatID` via BD
2. Si se encuentra y esta ejecutandose, usar el emisor de ese canal
3. Respaldo: usar el primer canal en ejecucion

### Tipos de Canal Soportados

| Tipo | Fuente | Recarga en Caliente |
|------|--------|---------------------|
| Telegram | Config o BD | Recarga de token en caliente |
| WebChat | BD (bootstrap) | — |
| Consola | Solo modo consola | — |

## Resolucion de Usuarios

El `DBUserResolver` mapea identidades de plataforma a UUIDs de iulita:

1. Buscar `user_channels` por `(channel_type, channel_user_id)`
2. Si se encuentra → devolver `user.ID` existente
3. Si no se encuentra y auto-registro habilitado:
   - Crear nuevo `User` con contrasena aleatoria y `MustChangePass: true`
   - Vincular canal al usuario
   - Devolver nuevo UUID
4. Si no se encuentra y auto-registro deshabilitado → rechazar

**Locale por canal**: despues de la resolucion, se llama `GetChannelLocale(ctx, channelType, channelUserID)` para establecer `msg.Locale` desde la preferencia almacenada en BD.

## Eventos de Estado

Los canales reciben notificaciones `StatusEvent` para retroalimentacion de UX:

| Tipo | Cuando | Uso |
|------|--------|-----|
| `processing` | Mensaje recibido, antes de la llamada LLM | Mostrar "pensando..." |
| `skill_start` | Antes de la ejecucion de la habilidad | Mostrar nombre de habilidad |
| `skill_done` | Despues de la ejecucion de la habilidad | Mostrar duracion, exito/fallo |
| `stream_start` | Antes de que comience el streaming | Preparar interfaz de streaming |
| `error` | En caso de error | Mostrar mensaje de error |
| `locale_changed` | Despues de la habilidad set_language | Actualizar locale de la interfaz |
