# Almacenamiento

Iulita usa SQLite como su unico backend de almacenamiento con modo WAL, busqueda de texto completo FTS5, busqueda vectorial ONNX y ORM bun.

## Configuracion de SQLite

### Conexion

- **Driver**: `modernc.org/sqlite` (Go puro, sin CGo)
- **ORM**: `uptrace/bun` con `sqlitedialect`
- **DSN**: `file:{path}?cache=shared&mode=rwc`

### PRAGMAs de Rendimiento

```sql
PRAGMA journal_mode = WAL;       -- Write-Ahead Logging (lecturas concurrentes)
PRAGMA synchronous = NORMAL;     -- Seguro con WAL, 2x velocidad de escritura vs FULL
PRAGMA mmap_size = 8388608;      -- 8MB de I/O mapeado en memoria
PRAGMA cache_size = -2000;       -- ~2MB de cache de paginas en proceso
PRAGMA temp_store = MEMORY;      -- Tablas temporales en RAM (acelera FTS5/ordenamientos)
```

## Esquema

### Tablas Principales

| Tabla | Proposito | Columnas Clave |
|-------|-----------|----------------|
| `users` | Cuentas de usuario | id (UUID), username, password_hash, role, timezone |
| `user_channels` | Vinculaciones de canal | user_id, channel_type, channel_user_id, locale |
| `channel_instances` | Configuraciones de bot/integracion | slug, type, enabled, config_json |
| `chat_messages` | Historial de conversacion | chat_id, user_id, role, content |
| `facts` | Memorias almacenadas | user_id, content, source_type, access_count, last_accessed_at |
| `insights` | Perspectivas de referencia cruzada | user_id, content, fact_ids, quality, expires_at |
| `directives` | Instrucciones personalizadas | user_id, content |
| `tech_facts` | Perfil de comportamiento | user_id, category, key, value, confidence |
| `reminders` | Recordatorios basados en tiempo | user_id, title, due_at, fired |
| `tasks` | Cola de tareas del planificador | type, status, payload, worker_id, capabilities |
| `scheduler_states` | Temporizacion de trabajos | job_name, last_run, next_run |
| `agent_jobs` | Tareas LLM programadas por el usuario | name, prompt, cron_expr, delivery_chat_id |
| `config_overrides` | Configuracion en tiempo de ejecucion | key, value, encrypted, updated_by |
| `google_accounts` | Tokens OAuth2 | user_id, account_email, tokens (cifrados) |
| `installed_skills` | Habilidades externas | slug, name, version, source |
| `todo_items` | Tareas unificadas | user_id, title, provider, external_id, due_date |
| `audit_log` | Registro de auditoria | user_id, action, details |
| `usage_stats` | Uso de tokens por hora | chat_id, hour, input_tokens, output_tokens, cost_usd |

### Tablas FTS5

```sql
CREATE VIRTUAL TABLE facts_fts USING fts5(content, content_rowid=id);
CREATE VIRTUAL TABLE insights_fts USING fts5(content, content_rowid=id);
```

Estas son tablas FTS5 de "contenido externo" — el indice refleja el contenido de las tablas base via triggers.

### Triggers FTS5

Seis triggers mantienen los indices FTS sincronizados:

```sql
-- Facts
CREATE TRIGGER facts_ai AFTER INSERT ON facts BEGIN
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER facts_ad AFTER DELETE ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
END;

-- CRITICO: "UPDATE OF content" — NO "UPDATE"
CREATE TRIGGER facts_au AFTER UPDATE OF content ON facts BEGIN
    DELETE FROM facts_fts WHERE rowid = old.id;
    INSERT INTO facts_fts(rowid, content) VALUES (new.id, new.content);
END;
```

**El problema del trigger**: Usar `AFTER UPDATE ON facts` (sin `OF content`) hace que el trigger se dispare en CUALQUIER actualizacion de columna (ej., `access_count++`). Con la implementacion FTS5 de modernc SQLite, esto causa `SQL logic error (1)`. La solucion es `AFTER UPDATE OF content` — los triggers solo se disparan cuando `content` cambia especificamente.

### Tablas de Vectores

```sql
CREATE TABLE IF NOT EXISTS fact_vectors (
    fact_id INTEGER PRIMARY KEY REFERENCES facts(id) ON DELETE CASCADE,
    embedding BLOB NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS insight_vectors (
    insight_id INTEGER PRIMARY KEY REFERENCES insights(id) ON DELETE CASCADE,
    embedding BLOB NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

**Codificacion**: Cada `float32` → 4 bytes LittleEndian. 384 dimensiones = 1536 bytes por vector.

**Auto-deteccion**: `decodeVector()` verifica si los datos comienzan con `[` (array JSON) para compatibilidad heredada; de lo contrario decodifica como binario.

### Tablas de Cache

```sql
CREATE TABLE IF NOT EXISTS embedding_cache (
    content_hash TEXT PRIMARY KEY,
    embedding BLOB NOT NULL,
    dimensions INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS response_cache (
    prompt_hash TEXT PRIMARY KEY,
    model TEXT NOT NULL,
    response TEXT NOT NULL,
    usage_json TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    hit_count INTEGER NOT NULL DEFAULT 0
);
```

Ambos usan expulsion LRU (el `accessed_at` mas antiguo se elimina al exceder las entradas maximas).

## Alcance de Datos Multi-Usuario

Todas las tablas de datos tienen una columna `user_id TEXT`. Las consultas usan variantes con alcance de usuario cuando estan disponibles:

```
SearchFacts(ctx, chatID, query, limit)        // heredado, canal unico
SearchFactsByUser(ctx, userID, query, limit)  // entre canales
```

La variante con alcance de usuario es preferida: los datos de un usuario de Telegram, WebChat y Consola son todos accesibles independientemente del canal actual.

### BackfillUserIDs

Migracion que asocia datos heredados (creados antes del soporte multi-usuario) con usuarios:

1. Eliminar triggers FTS (requerido — UPDATE los dispararia)
2. Join `chat_messages → user_channels` en `chat_id`
3. UPDATE masivo: establecer `user_id` en facts, insights, messages, etc.
4. Recrear triggers FTS

## Migraciones

Todas las migraciones se ejecutan en `RunMigrations(ctx)` como una unica funcion idempotente:

1. **Creacion de tablas** via `bun.CreateTableIfNotExists` para los 18 modelos de dominio
2. **Tablas SQL raw** para caches (no gestionadas por bun)
3. **Columnas aditivas** via `ALTER TABLE ADD COLUMN` (errores ignorados para idempotencia)
4. **Renombramientos heredados** (ej., `dreams → insights`)
5. **Creacion de indices** para consultas criticas de rendimiento
6. **Recreacion FTS5** — siempre eliminar y recrear triggers + tablas para corregir corrupcion de versiones anteriores

### Indices Clave

| Indice | Proposito |
|--------|-----------|
| `idx_tasks_status_scheduled` | Cola de tareas: tareas pendientes por scheduled_at |
| `idx_tasks_unique_key` | Creacion idempotente de tareas |
| `idx_facts_user` | Consultas de datos por usuario |
| `idx_insights_user` | Consultas de perspectivas por usuario |
| `idx_user_channels_binding` | Unico (channel_type, channel_user_id) |
| `idx_todos_user_due` | Panel de tareas: tareas por fecha de vencimiento |
| `idx_todos_external` | Dedup de tareas externas por provider+external_id |
| `idx_techfacts_unique` | Unico (chat_id, category, key) |
| `idx_google_accounts_user_email` | Unico (user_id, account_email) |

## Cola de Tareas

El planificador usa SQLite como cola de tareas con reclamacion atomica:

### Ciclo de Vida de Tareas

```
pending → claimed → running → completed/failed
```

### Reclamacion Atomica

`ClaimTask(ctx, workerID, capabilities)` usa una transaccion:
1. SELECT una tarea pendiente cuyas `Capabilities` son un subconjunto de las del worker
2. UPDATE estado a `claimed`, establecer `worker_id`
3. Devolver la tarea

Esto asegura que ningun par de workers reclame la misma tarea.

### Limpieza

- **Tareas obsoletas**: tareas en estado `running` por mas de 5 minutos se reinician a `pending`
- **Tareas antiguas**: tareas completadas/fallidas de mas de 7 dias se eliminan
- **De una sola vez**: tareas con `one_shot = true` y `delete_after_run = true` se eliminan despues de completarse

## Busqueda Hibrida

Consulta [Memoria y Perspectivas — Busqueda Hibrida](memory-and-insights.md#algoritmo-de-busqueda-hibrida) para el algoritmo completo.

### Patron de Consulta

```sql
-- Busqueda FTS5
SELECT * FROM facts
WHERE user_id = ?
  AND id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)
ORDER BY access_count DESC, last_accessed_at DESC
LIMIT ?

-- Similitud vectorial (en Go)
for _, vec := range allVectors {
    score := cosineSimilarity(queryVec, vec.Embedding)
}

-- Puntuacion combinada
combined = (1-vectorWeight)*ftsScore + vectorWeight*vecScore
```

## Interfaz del Repositorio

La interfaz `Repository` en `internal/storage/storage.go` tiene mas de 60 metodos organizados por dominio:

| Dominio | Metodos |
|---------|---------|
| Mensajes | Save, GetHistory, Clear, DeleteBefore |
| Datos | Save, Search (FTS/Hybrid), Update, Delete, Reinforce, GetAll, GetRecent |
| Perspectivas | Save, Search (FTS/Hybrid), Delete, Reinforce, GetRecent, DeleteExpired |
| Vectores | CreateTables, SaveFactVector, SaveInsightVector, GetWithoutEmbeddings |
| Usuarios | Create, Get, Update, Delete, List, BindChannel, GetByChannel |
| Tareas | Create, Claim, Start, Complete, Fail, List, Cleanup |
| Configuracion | Get, List, Save, Delete overrides |
| Caches | Embedding (get/save/evict), Response (get/save/evict/stats) |
| Locale | Update, Get (por tipo de canal o chat ID) |
| Todos | CRUD, sync, counts, consultas por proveedor |
| Agent Jobs | CRUD, GetDue |
| Google | Account CRUD, almacenamiento de tokens |
| Habilidades Externas | Install, List, Get, Delete |
