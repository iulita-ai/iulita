# Canales

Iulita soporta multiples canales de comunicacion. Cada canal convierte mensajes especificos de la plataforma en un formato universal `IncomingMessage` y los enruta a traves del asistente.

## Capacidades de los Canales

Cada canal declara sus capacidades mediante un bitmask en cada mensaje:

| Capacidad | Consola | Telegram | WebChat |
|-----------|---------|----------|---------|
| Streaming | via bubbletea | Respuesta final en búfer | Si (WebSocket) |
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

La [guía completa de Telegram (EN)](../en/telegram.md) describe los cambios del código aún no publicados: temas privados, tarjetas de día y tiempo, alias, citas y callbacks personales.

Configure el token y `telegram.allowed_ids`; active los temas en BotFather. Un mensaje normal en el chat raíz inicia un tema; `/topic` lista los conocidos. `/clear` y `/compact` afectan al contexto actual. `/topic pause` pausa Iulita, no cierra el tema; `/topic delete` requiere confirmación y cancela trabajo local asociado sin borrar tareas externas.

Use `/day`, `/day text`, `/weather London 4` o `/weather text London 4`. `telegram.day_widgets` y `telegram.day_actions` están activados por defecto, también al actualizar configuraciones sin estas claves; un valor false explícito se conserva y los cambios requieren reinicio. Las acciones necesitan confirmación del propietario.

Las respuestas se almacenan temporalmente hasta la comprobación final de privacidad; no se publican fragmentos sin comprobar. Un fallo del envío privado de datos sensibles no causa un envío ordinario de esos datos. La combinación de mensajes separa usuarios y temas. Los widgets en producción son privados; inline/shared requieren el servidor de pruebas de Telegram. Consulte la guía EN para voz, rich quotes, disponibilidad, configuración y recuperación.

## WebChat

Web chat basado en WebSocket embebido en el panel de control.

### Protocolo

**Conexion**: WebSocket en `/ws/chat?token=<access-token>`

**Mensajes entrantes** (cliente → servidor):
```json
{
  "text": "user message",
  "chat_id": "web:abc123",
  "prompt_id": "prompt_123_1",       // solo para respuestas de prompts
  "prompt_answer": "option_id",      // solo para respuestas de prompts
  "remember_message_id": "nano_ts"   // solo para solicitudes de marcadores
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
| `remember_ack` | Confirmacion de marcador | `remember_ack.message_id`, `remember_ack.fact_id`, `remember_ack.status` |

### Protocolo de Marcadores

La funcion de marcadores permite a los usuarios guardar respuestas del asistente como datos mediante un boton de la interfaz.

**Flujo:**
1. El servidor envia `message` o `stream_done` con `message_id` (marca de tiempo en nanosegundos Unix)
2. El servidor almacena en cache el contenido con clave `(message_id, chatID)` durante 10 minutos
3. El frontend muestra el icono 💾 al pasar el raton sobre los mensajes del asistente
4. El usuario hace clic → envia `{"remember_message_id": "<message_id>"}`
5. El servidor valida la propiedad (`chatID` debe coincidir con la entrada en cache), guarda como dato con `source_type="bookmark"`, encola refinamiento LLM en segundo plano
6. El servidor envia `{"type": "remember_ack", "remember_ack": {"message_id": "...", "status": "saved", "fact_id": 42}}`
7. El frontend actualiza el icono a ✅

**Valores de status**: `saved`, `error`, `expired` (mensaje ya no esta en cache)

### Autenticacion

El servidor valida el token de acceso y la cuenta actual antes del upgrade. Deriva la identidad y `web:<user_id>` en el servidor; los campos del cliente no permiten suplantar otra cuenta. El Origin enviado debe coincidir con el host y el esquema. La caducidad o sustitución de la conexión cancela el trabajo pendiente. WebChat mantiene un chat por usuario; la API multichat usa OAuth independiente.

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
| `orchestration_started` | Antes de lanzar sub-agentes | Mostrar cantidad de agentes |
| `agent_started` | Por agente, antes de ejecutar | Mostrar nombre + tipo de agente |
| `agent_progress` | Por agente, despues de cada turno LLM | Actualizar contador de turnos |
| `agent_completed` | Por agente, en exito | Mostrar duracion + tokens |
| `agent_failed` | Por agente, en error | Mostrar mensaje de error |
| `orchestration_done` | Despues de que todos terminen | Mostrar estadisticas resumen |
