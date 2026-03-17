# Planificador

El planificador es un sistema de dos componentes: un **Coordinador** que produce tareas segun un horario, y un **Worker** que las reclama y ejecuta. Ambos usan SQLite como cola de tareas.

## Arquitectura

```
Scheduler (Coordinador)
    │ consulta cada 30s
    │ verifica temporizacion de trabajos contra scheduler_states
    │
    ├── InsightJob (24h) → tareas insight.generate
    ├── InsightCleanupJob (1h) → tareas insight.cleanup
    ├── TechFactsJob (6h) → tareas techfact.analyze
    ├── HeartbeatJob (6h) → tareas heartbeat.check
    ├── RemindersJob (30s) → tareas reminder.fire
    ├── AgentJobsJob (30s) → tareas agent.job
    └── TodoSyncJob (cron horario) → tareas todo.sync
           │
           ▼
    tabla tasks (SQLite)
           │
           ▼
Worker
    │ consulta cada 5s
    │ reclama tareas atomicamente
    │ despacha a manejadores registrados
    │
    ├── InsightGenerateHandler
    ├── InsightCleanupHandler
    ├── TechFactAnalyzeHandler
    ├── HeartbeatHandler
    ├── ReminderFireHandler
    ├── AgentJobHandler
    ├── RefineBookmarkHandler
    └── TodoSyncHandler
```

## Coordinador

### Definicion de Trabajo

```go
type JobDefinition struct {
    Name        string
    Interval    time.Duration
    CronExpr    string           // cron estandar (5 campos)
    Timezone    string           // zona horaria IANA para cron
    Enabled     bool
    CreateTasks func(ctx) ([]domain.Task, error)
}
```

Cada trabajo declara un `Interval` fijo o un `CronExpr`. Cron usa `robfig/cron/v3` con soporte de zona horaria.

### Bucle de Planificacion

1. **Calentamiento**: en el primer arranque, `NextRun = now + 1 minute` (periodo de gracia)
2. **Tick** cada 30 segundos:
   - Mantenimiento: reclamar tareas obsoletas (en ejecucion > 5 min), eliminar tareas antiguas (> 7 dias)
   - Para cada trabajo habilitado: si `now >= state.NextRun`, llamar `CreateTasks`
   - Insertar tareas via `CreateTaskIfNotExists` (idempotente por `UniqueKey`)
   - Actualizar estado: `LastRun = now`, `NextRun = computeNextRun()`

### Disparador Manual

`TriggerJob(name)`:
- Encuentra el trabajo por nombre
- Llama `CreateTasks` con `Priority = 1` (alta)
- Inserta tareas inmediatamente
- NO actualiza el estado del horario (la siguiente ejecucion regular sigue ocurriendo)

Disponible via panel de control: `POST /api/schedulers/:name/trigger`

## Worker

### Reclamacion de Tareas

```
Cada 5 segundos:
    para cada espacio de concurrencia disponible:
        ClaimTask(ctx, workerID, capabilities)  // transaccion SQLite atomica
        si se reclama tarea:
            go executeTask(task)
        sino:
            break  // no hay mas tareas disponibles
```

`workerID = hostname-pid` (unico por proceso).

### Enrutamiento Basado en Capacidades

Las tareas declaran capacidades requeridas como cadena separada por comas (ej., `"llm,storage"`). La lista de capacidades del worker debe ser un superconjunto.

**Capacidades del worker local**: `["storage", "llm", "telegram"]`

**Worker remoto**: cualquier conjunto de capacidades, autenticado via `Scheduler.WorkerToken`.

### Ciclo de Vida de Tareas

```
pending → claimed (por worker) → running → completed / failed
```

- `ClaimTask`: SELECT + UPDATE atomico en una transaccion
- `StartTask`: establecer estado a `running`, registrar hora de inicio
- `CompleteTask`: almacenar resultado, publicar evento `TaskCompleted`
- `FailTask`: almacenar error, publicar evento `TaskFailed`

### API del Worker Remoto

Para despliegues distribuidos, el panel de control expone una API REST:

| Endpoint | Metodo | Descripcion |
|----------|--------|-------------|
| `/api/tasks/` | GET | Listar tareas |
| `/api/tasks/counts` | GET | Conteos por estado |
| `/api/tasks/claim` | POST | Reclamar una tarea |
| `/api/tasks/:id/start` | POST | Marcar como en ejecucion |
| `/api/tasks/:id/complete` | POST | Completar con resultado |
| `/api/tasks/:id/fail` | POST | Fallar con error |

Autenticado via token bearer estatico (`scheduler.worker_token`).

## Trabajos Integrados

### Generacion de Perspectivas (`insights`)

- **Intervalo**: 24 horas (configurable via `skills.insights.interval`)
- **Tipo de tarea**: `insight.generate`
- **Capacidades**: `llm,storage`
- **Condicion**: el chat/usuario debe tener >= `minFacts` (predeterminado 20) datos

**Pipeline del manejador:**
1. Cargar todos los datos del usuario
2. Construir vectores TF-IDF (tokenizar, bigramas, puntuaciones TF-IDF)
3. Clustering K-means++: `k = sqrt(numFacts / 3)`, distancia coseno, 20 iteraciones
4. Muestrear hasta 6 pares entre clusters (omitir pares ya cubiertos)
5. Para cada par: LLM genera perspectiva + puntua calidad (1-5)
6. Almacenar perspectivas con calidad >= umbral

### Limpieza de Perspectivas (`insight_cleanup`)

- **Intervalo**: 1 hora
- **Tipo de tarea**: `insight.cleanup`
- **Capacidades**: `storage`

Elimina perspectivas donde `expires_at < now`. TTL predeterminado es 30 dias.

### Analisis de Tech Facts (`techfacts`)

- **Intervalo**: 6 horas (configurable)
- **Tipo de tarea**: `techfact.analyze`
- **Capacidades**: `llm,storage`
- **Condicion**: 10+ mensajes con 5+ del usuario

**Manejador**: Envia mensajes del usuario al LLM solicitando JSON estructurado: `[{category, key, value, confidence}]`. Las categorias incluyen temas, estilo de comunicacion y patrones de comportamiento. Upsert en la tabla `tech_facts`.

### Heartbeat (`heartbeat`)

- **Intervalo**: 6 horas (configurable)
- **Tipo de tarea**: `heartbeat.check`
- **Capacidades**: `llm,storage,telegram`

**Manejador**: Recopila datos recientes, perspectivas y recordatorios pendientes. Pregunta al LLM si un mensaje de verificacion esta justificado. Si la respuesta no es `HEARTBEAT_OK`, envia el mensaje al usuario.

### Recordatorios (`reminders`)

- **Intervalo**: 30 segundos
- **Tipo de tarea**: `reminder.fire`
- **Capacidades**: `telegram,storage`

**Manejador**: Formatea el recordatorio con hora local, envia via `MessageSender`, marca como disparado.

### Agent Jobs (`agent_jobs`)

- **Intervalo**: 30 segundos
- **Tipo de tarea**: `agent.job`
- **Capacidades**: `llm`

Consulta `GetDueAgentJobs(now)` para tareas LLM programadas por el usuario. Actualiza `next_run` inmediatamente (antes de la ejecucion) para prevenir duplicados.

**Manejador**: Llama `provider.Complete` con el prompt definido por el usuario. Opcionalmente entrega el resultado a un chat configurado.

### Refinamiento de Marcadores (`bookmark.refine`)

- **Disparador**: bajo demanda (creado por `bookmark.Service.Save`)
- **Tipo de tarea**: `bookmark.refine`
- **Capacidades**: `llm,storage`
- **Intentos maximos**: 2
- **Eliminar despues de ejecucion**: si

**Manejador**: Recibe `{fact_id, content, chat_id, user_id}`. Llama al LLM con un prompt de resumen para extraer 1-3 oraciones concisas. Actualiza el contenido del dato si el refinamiento es significativamente mas corto (<90% del original). Maneja correctamente datos ya eliminados.

**No es un trabajo programado** — las tareas se crean bajo demanda cuando los usuarios hacen clic en el boton de marcador. El worker las recoge en el siguiente ciclo de consulta (cada 5 segundos).

### Sincronizacion de Todos (`todo_sync`)

- **Cron**: `0 * * * *` (cada hora)
- **Tipo de tarea**: `todo.sync`
- **Capacidades**: `storage`

**Manejador**: Itera todas las instancias `TodoProvider` disponibles (Todoist, Google Tasks, Craft). Para cada una: `FetchAll` → upsert en `todo_items` → eliminar entradas obsoletas.

## Agent Jobs (Definidos por el Usuario)

Los usuarios pueden crear tareas LLM programadas via el panel de control:

```json
{
  "name": "Daily Summary",
  "prompt": "Summarize my recent facts and insights",
  "cron_expr": "0 9 * * *",
  "delivery_chat_id": "123456789"
}
```

Campos:
- `name` — nombre para mostrar
- `prompt` — prompt LLM a ejecutar
- `cron_expr` o `interval` — programacion
- `delivery_chat_id` — donde enviar el resultado (opcional)

Gestionado via panel de control: `GET/POST/PUT/DELETE /api/agent-jobs/`
