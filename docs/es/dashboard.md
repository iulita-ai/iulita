# Panel de Control

El panel de control es una API REST GoFiber que sirve una SPA Vue 3 embebida. Proporciona una interfaz web para gestionar todos los aspectos de Iulita.

## Arquitectura

```
GoFiber Server
    ├── /api/*          API REST (autenticada por JWT)
    ├── /ws             Hub WebSocket (actualizaciones en tiempo real)
    ├── /ws/chat        Canal WebChat (endpoint separado)
    └── /*              SPA Vue 3 (embebida, enrutamiento del lado del cliente)
```

La SPA Vue esta embebida en el binario Go via `//go:embed dist/*` y servida con respaldo `index.html` para todas las rutas desconocidas.

## Autenticacion

| Endpoint | Autenticacion | Descripcion |
|----------|---------------|-------------|
| `POST /api/auth/login` | Publico | Verificacion de credenciales bcrypt, devuelve tokens de acceso + refresco |
| `POST /api/auth/refresh` | Publico | Validar token de refresco, devolver nuevo token de acceso |
| `POST /api/auth/change-password` | JWT | Cambiar contrasena propia |
| `GET /api/auth/me` | JWT | Perfil del usuario actual |
| `PATCH /api/auth/locale` | JWT | Actualizar locale para todos los canales |

**Detalles de JWT:**
- Algoritmo: HMAC-SHA256
- TTL del token de acceso: 24 horas
- TTL del token de refresco: 7 dias
- Claims: `user_id`, `username`, `role`
- Secreto: auto-generado si no esta configurado

## API REST

### Endpoints Publicos

| Metodo | Ruta | Descripcion |
|--------|------|-------------|
| GET | `/api/system` | Info del sistema, version, tiempo activo, estado del asistente |

### Endpoints de Usuario (JWT Requerido)

| Metodo | Ruta | Descripcion |
|--------|------|-------------|
| GET | `/api/stats` | Conteos de mensajes, datos, perspectivas, recordatorios |
| GET | `/api/chats` | Listar todos los chat IDs con conteos de mensajes |
| GET | `/api/facts` | Listar/buscar datos (por chat_id, user_id, query) |
| PUT | `/api/facts/:id` | Actualizar contenido de dato |
| DELETE | `/api/facts/:id` | Eliminar dato |
| GET | `/api/facts/search` | Busqueda FTS de datos |
| GET | `/api/insights` | Listar perspectivas |
| GET | `/api/reminders` | Listar recordatorios |
| GET | `/api/directives` | Obtener directiva para un chat |
| GET | `/api/messages` | Historial del chat con paginacion |
| GET | `/api/skills` | Listar todas las habilidades con estado habilitado/config |
| PUT | `/api/skills/:name/toggle` | Habilitar/deshabilitar habilidad en tiempo de ejecucion |
| GET | `/api/skills/:name/config` | Esquema de config de habilidad + valores actuales |
| PUT | `/api/skills/:name/config/:key` | Establecer clave de config de habilidad (auto-cifra secretos) |
| GET | `/api/techfacts` | Perfil de comportamiento agrupado por categoria |
| GET | `/api/usage/summary` | Uso de tokens + estimacion de costo |
| GET | `/api/schedulers` | Estado de trabajos del planificador |
| POST | `/api/schedulers/:name/trigger` | Disparador manual de trabajo |

### Endpoints de Tareas (JWT Requerido)

| Metodo | Ruta | Descripcion |
|--------|------|-------------|
| GET | `/api/todos/providers` | Listar proveedores de tareas |
| GET | `/api/todos/today` | Tareas de hoy |
| GET | `/api/todos/overdue` | Tareas vencidas |
| GET | `/api/todos/upcoming` | Tareas proximas (predeterminado 7 dias) |
| GET | `/api/todos/all` | Todas las tareas incompletas |
| GET | `/api/todos/counts` | Conteos de hoy + vencidas |
| POST | `/api/todos/` | Crear tarea |
| POST | `/api/todos/sync` | Disparar sincronizacion manual de todos |
| POST | `/api/todos/:id/complete` | Completar tarea |
| DELETE | `/api/todos/:id` | Eliminar tarea integrada |

### Endpoints de Google Workspace (JWT Requerido)

| Metodo | Ruta | Descripcion |
|--------|------|-------------|
| GET | `/api/google/status` | Estado de la cuenta |
| POST | `/api/google/upload-credentials` | Subir archivo de credenciales OAuth |
| GET | `/api/google/auth` | Iniciar flujo OAuth2 |
| GET | `/api/google/callback` | Callback OAuth2 |
| GET | `/api/google/accounts` | Listar cuentas |
| DELETE | `/api/google/accounts/:id` | Eliminar cuenta |
| PUT | `/api/google/accounts/:id` | Actualizar cuenta |

### Endpoints de Administrador (Rol de Administrador Requerido)

| Metodo | Ruta | Descripcion |
|--------|------|-------------|
| GET/PUT/DELETE | `/api/config/*` | Sobreescrituras de config, esquema, depuracion |
| GET/POST/PUT/DELETE | `/api/users/*` | CRUD de usuarios + vinculaciones de canal |
| GET/POST/PUT/DELETE | `/api/channels/*` | CRUD de instancias de canal |
| GET/POST/PUT/DELETE | `/api/agent-jobs/*` | CRUD de agent jobs |
| GET/POST/DELETE | `/api/skills/external/*` | Gestion de habilidades externas |
| GET/POST | `/api/wizard/*` | Asistente de configuracion |
| PUT | `/api/todos/default-provider` | Establecer proveedor de tareas predeterminado |

### Endpoints del Worker (Token Bearer)

| Metodo | Ruta | Descripcion |
|--------|------|-------------|
| GET | `/api/tasks/` | Listar tareas del planificador |
| GET | `/api/tasks/counts` | Conteos por estado |
| POST | `/api/tasks/claim` | Reclamar una tarea (worker remoto) |
| POST | `/api/tasks/:id/start` | Marcar tarea como en ejecucion |
| POST | `/api/tasks/:id/complete` | Completar tarea |
| POST | `/api/tasks/:id/fail` | Fallar tarea |

## Hub WebSocket

El hub WebSocket en `/ws` proporciona actualizaciones en tiempo real a los clientes del panel conectados.

### Eventos

| Evento | Fuente | Payload |
|--------|--------|---------|
| `task.completed` | Worker | Detalles de la tarea |
| `task.failed` | Worker | Tarea + error |
| `message.received` | Assistant | Metadatos del mensaje |
| `response.sent` | Assistant | Metadatos de la respuesta |
| `fact.saved` | Storage | Detalles del dato |
| `insight.created` | Storage | Detalles de la perspectiva |
| `config.changed` | Config store | Clave + valor |

Los eventos se publican via el bus de eventos usando `SubscribeAsync` (no bloqueante).

### Protocolo

```json
// Servidor → Cliente
{"type": "task.completed", "payload": {...}}
{"type": "fact.saved", "payload": {...}}
```

## SPA Vue 3

### Stack Tecnologico

- **Vue 3** — Composition API
- **Naive UI** — biblioteca de componentes
- **UnoCSS** — CSS utility-first
- **vue-i18n** — internacionalizacion (6 idiomas)
- **vue-router** — enrutamiento del lado del cliente

### Vistas

| Ruta | Componente | Autenticacion | Descripcion |
|------|------------|---------------|-------------|
| `/` | Dashboard | JWT | Vision general de estadisticas, estado del planificador |
| `/facts` | Facts | JWT | Navegador de datos con busqueda, edicion, eliminacion |
| `/insights` | Insights | JWT | Lista de perspectivas |
| `/reminders` | Reminders | JWT | Lista de recordatorios |
| `/profile` | TechFacts | JWT | Metadatos del perfil de comportamiento |
| `/settings` | Settings | JWT | Gestion de habilidades, editor de configuracion |
| `/tasks` | Tasks | JWT | Pestanas Hoy/Vencidas/Proximas/Todas |
| `/chat` | Chat | JWT | Web chat WebSocket |
| `/users` | Users | Admin | CRUD de usuarios + vinculaciones de canal |
| `/channels` | Channels | Admin | CRUD de instancias de canal |
| `/agent-jobs` | AgentJobs | Admin | CRUD de agent jobs |
| `/skills` | ExternalSkills | Admin | Marketplace + habilidades instaladas |
| `/setup` | Setup | Admin | Asistente de configuracion web |
| `/config-debug` | ConfigDebug | Admin | Visor de sobreescrituras de config en bruto |
| `/login` | Login | Publico | Formulario de inicio de sesion |

### Guardias del Router

```javascript
router.beforeEach((to, from, next) => {
    if (to.meta.public) { next(); return }
    if (!isLoggedIn()) { next({ name: 'login' }); return }
    if (to.meta.admin && !isAdmin()) { next({ name: 'dashboard' }); return }
    next()
})
```

### Composables Clave

- `useWebSocket` — WebSocket con reconexion automatica y eventos tipados
- `useLocale` — estado reactivo de locale, deteccion RTL, sincronizacion con backend
- `useSkillStatus` — controla elementos del sidebar basandose en disponibilidad de habilidades

### Interfaz de Gestion de Habilidades

La vista de Settings proporciona:

1. **Toggle de habilidad** — habilitar/deshabilitar cada habilidad en tiempo de ejecucion
2. **Editor de configuracion** — configuracion por habilidad con:
   - Campos de formulario impulsados por esquema
   - Proteccion de claves secretas (valores nunca filtrados en la API)
   - Auto-cifrado para valores sensibles
   - Recarga en caliente al guardar

### Panel de Tareas

La vista de Tasks agrega tareas de todos los proveedores:

- **Pestana Hoy** — tareas que vencen hoy
- **Pestana Vencidas** — tareas pasadas de fecha
- **Pestana Proximas** — proximos 7 dias
- **Pestana Todas** — todas las tareas incompletas
- **Boton de sincronizacion** — dispara tarea del planificador de una sola vez
- **Boton de crear** — nueva tarea con seleccion de proveedor

## Metricas Prometheus

Cuando estan habilitadas (`metrics.enabled = true`), las metricas se exponen en un puerto separado:

| Metrica | Tipo | Etiquetas |
|---------|------|-----------|
| `iulita_llm_requests_total` | Counter | provider, model, status |
| `iulita_llm_tokens_input_total` | Counter | provider |
| `iulita_llm_tokens_output_total` | Counter | provider |
| `iulita_llm_request_duration_seconds` | Histogram | provider |
| `iulita_llm_cost_usd_total` | Counter | — |
| `iulita_skill_executions_total` | Counter | skill, status |
| `iulita_task_total` | Counter | type, status |
| `iulita_messages_total` | Counter | direction |
| `iulita_cache_hits_total` | Counter | cache_type |
| `iulita_cache_misses_total` | Counter | cache_type |
| `iulita_active_sessions` | Gauge | — |

Las metricas se pueblan suscribiendose al bus de eventos (no bloqueante).
