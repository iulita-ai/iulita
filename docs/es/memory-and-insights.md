# Memoria y Perspectivas

## Filosofia

La mayoria de los asistentes de IA olvidan todo entre sesiones o alucinan "recuerdos" de los datos de entrenamiento. Iulita toma un enfoque fundamentalmente diferente:

- **Solo almacenamiento explicito** — los datos se almacenan solo cuando lo pides ("recuerda que...")
- **Datos verificados** — cada dato se traza hasta una solicitud especifica del usuario
- **Perspectivas por referencia cruzada** — los patrones se descubren analizando tus datos reales
- **Decaimiento temporal** — los recuerdos antiguos pierden relevancia naturalmente a menos que los accedas
- **Recuperacion hibrida** — busqueda de texto completo FTS5 combinada con embeddings de vectores ONNX

## Datos (Facts)

### Almacenamiento de Datos (Remember)

Cuando dices "remember that my dog's name is Max", ocurre lo siguiente:

1. **Deteccion de disparador** — el asistente detecta la palabra clave de memoria ("remember") y fuerza la herramienta `remember`
2. **Verificacion de duplicados** — busca datos existentes usando las primeras 3 palabras para detectar casi-duplicados
3. **INSERT en SQLite** — el dato se guarda con `user_id`, `content`, `source_type=user`, marcas de tiempo
4. **Indice FTS5** — un trigger `AFTER INSERT` agrega automaticamente el dato al indice de texto completo `facts_fts`
5. **Embedding vectorial** — una goroutine en segundo plano genera un embedding ONNX (384-dim all-MiniLM-L6-v2) y lo almacena en `fact_vectors`

```
domain.Fact {
    ID             int64
    ChatID         string     // canal de origen ("123456789", "console", "web:uuid")
    UserID         string     // UUID de iulita — compartido entre todos los canales
    Content        string     // el texto del dato
    SourceType     string     // "user" (explicito) o "system" (auto-extraido)
    CreatedAt      time.Time
    LastAccessedAt time.Time  // se reinicia en cada recuperacion
    AccessCount    int        // se incrementa en cada recuperacion
}
```

### Recuperacion de Datos (Recall)

Cuando preguntas "what is my dog's name?":

1. **Busqueda por usuario** — primero intenta `SearchFactsByUser(userID, query, limit)` para datos entre canales
2. **Coincidencia FTS5** — `SELECT * FROM facts WHERE id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)`
3. **Sobremuestreo** — obtiene `limit * 3` candidatos (minimo 20) para re-ranking posterior
4. **Decaimiento temporal** — cada candidato se puntua: `decay = exp(-ln(2) / halfLife * ageDays) * (1 + log(1 + accessCount))`
5. **Re-ranking MMR** — la Relevancia Marginal Maxima reduce casi-duplicados en el conjunto de resultados
6. **Refuerzo** — cada dato devuelto obtiene `access_count++` y `last_accessed_at = now`

El asistente tambien realiza una **busqueda hibrida** en cada mensaje (no solo en recuperacion explicita):

```
1. Sanitizar consulta (eliminar operadores FTS, palabras vacias, limitar a 5 palabras clave)
2. Generar vector de consulta via embedding ONNX
3. Resultados FTS: puntuacion basada en posicion (1 - i/(n+1))
4. Resultados vectoriales: similitud coseno contra todos los vectores almacenados
5. Combinado: (1-vectorWeight)*ftsScore + vectorWeight*vecScore
6. Union de ambos conjuntos, ordenados por puntuacion combinada
```

### Olvidar Datos

La herramienta `forget` elimina un dato por ID. El trigger FTS (`facts_ad`) lo elimina automaticamente del indice de texto completo. El `ON DELETE CASCADE` en `fact_vectors` elimina el embedding.

## Marcadores (Guardado Rapido)

Ademas del flujo de "recordar" basado en chat, los usuarios pueden marcar cualquier respuesta del asistente con un solo clic.

### Como Funciona

1. **Telegram**: un boton 💾 de teclado inline aparece debajo de cada respuesta del asistente (incluyendo mensajes de multiples fragmentos)
2. **WebChat**: un icono 💾 aparece al pasar el raton sobre los mensajes del asistente
3. Al hacer clic en el boton se guarda **inmediatamente** la respuesta completa como dato con `source_type="bookmark"`
4. Una tarea del planificador en segundo plano (`bookmark.refine`) envia el contenido a un LLM para resumirlo
5. Si el LLM produce una version significativamente mas corta (<90% de la longitud original), el contenido del dato se actualiza

### Marcador vs Recordar

| Aspecto | Marcador (boton 💾) | Recordar ("recuerda que...") |
|---------|---------------------|-------------------------------|
| Disparador | Clic en boton | Mensaje de chat |
| Contenido | Respuesta completa del asistente | Datos clave extraidos por LLM |
| Velocidad | Instantaneo (<5ms) | 2-5 segundos (llamada LLM) |
| Costo de tokens | Diferido (refinamiento en segundo plano) | Inmediato |
| Tipo de fuente | `bookmark` | `user` |
| Verificacion de duplicados | Ninguna (guarda tal cual) | Verificacion FTS de 3 primeras palabras |

### Refinamiento en Segundo Plano

La tarea del planificador `bookmark.refine`:
- **Capacidades**: `llm,storage`
- **Intentos maximos**: 2
- **Eliminar despues de ejecucion**: si (una sola vez)
- Extrae 1-3 oraciones concisas de la respuesta marcada
- Omite la actualizacion si el LLM devuelve vacio o el refinamiento no es mas corto
- Maneja correctamente datos eliminados (el usuario puede haberlos eliminado antes del refinamiento)

### Seguridad

- **Telegram**: el remitente del callback se verifica contra el destinatario del mensaje (verificacion de `tgUserID`)
- **WebChat**: el cache de mensajes almacena el propietario `chatID`; se valida la propiedad antes de guardar

## Decaimiento Temporal

Los datos y las perspectivas decaen con el tiempo usando decaimiento exponencial (radiactivo):

```
decay_factor = exp(-ln(2) / halfLifeDays * ageDays)
```

| Dias desde acceso | Vida media = 30 dias | Vida media = 90 dias |
|-------------------|----------------------|----------------------|
| 0 | 1.00 | 1.00 |
| 15 | 0.71 | 0.89 |
| 30 | 0.50 | 0.79 |
| 60 | 0.25 | 0.63 |
| 90 | 0.13 | 0.50 |

**Decisiones de diseno clave:**
- Los **datos** decaen desde `last_accessed_at` — cada recuperacion reinicia el reloj
- Las **perspectivas** decaen desde `created_at` — tienen un tiempo de vida fijo
- **Impulso por acceso**: `1 + log(1 + accessCount)` — un dato accedido 100 veces obtiene un impulso de 4.6x
- **Vida media predeterminada**: 30 dias (configurable via `skills.memory.half_life_days`)

## Re-ranking MMR

Despues de la puntuacion por decaimiento temporal, la Relevancia Marginal Maxima previene resultados casi-duplicados:

```
MMR(item) = lambda * relevance_score - (1 - lambda) * max_similarity_to_selected
```

- `lambda = 1.0` → relevancia pura, sin diversidad
- `lambda = 0.7` → recomendado: favorece la relevancia pero penaliza casi-duplicados
- `lambda = 0.0` → diversidad pura

La similitud se mide via similitud de Jaccard sobre tokens de palabras (aproximacion sin dependencias que funciona sin el embedder ONNX).

**Configuracion**: `skills.memory.mmr_lambda` (predeterminado 0, deshabilitado). Establecer en 0.7 para mejores resultados.

## Perspectivas (Insights)

Las perspectivas son referencias cruzadas generadas por IA entre tus datos, descubiertas por el planificador en segundo plano.

### Pipeline de Generacion

El trabajo de generacion de perspectivas se ejecuta cada 24 horas (configurable):

```
1. Cargar todos los datos del usuario
2. Verificar conteo minimo de datos (predeterminado 20)
3. Construir vectores TF-IDF
   - Tokenizar: minusculas, eliminar puntuacion, filtrar palabras vacias
   - Generar bigramas (pares de palabras adyacentes)
   - Calcular puntuaciones TF-IDF
4. Clustering K-means++
   - k = sqrt(numFacts / 3)
   - Metrica de distancia coseno
   - 20 iteraciones maximas
5. Muestrear pares entre clusters
   - Hasta 6 pares por ejecucion
   - Omitir pares de datos ya cubiertos
6. Para cada par:
   a. Enviar al LLM: "Genera una perspectiva creativa de estos dos clusters"
   b. Puntuar calidad (1-5) via llamada LLM separada
   c. Almacenar si calidad >= umbral
```

### Ciclo de Vida de las Perspectivas

```
domain.Insight {
    ID             int64
    ChatID         string
    UserID         string
    Content        string     // el texto de la perspectiva
    FactIDs        string     // IDs de datos fuente separados por coma
    Quality        int        // puntuacion de calidad LLM 1-5
    AccessCount    int
    LastAccessedAt time.Time
    CreatedAt      time.Time
    ExpiresAt      *time.Time // predeterminado: created + 30 dias
}
```

- **Creadas** por el planificador en segundo plano despues de clustering y sintesis LLM
- **Presentadas** en el prompt del sistema del asistente cuando son contextualmente relevantes (busqueda hibrida)
- **Reforzadas** cuando se acceden (se actualizan conteo de accesos y hora de ultimo acceso)
- **Promovidas** via la habilidad `promote_insight` (extiende o elimina la expiracion)
- **Descartadas** via la habilidad `dismiss_insight` (eliminacion inmediata)
- **Expiradas** — el trabajo de limpieza se ejecuta cada hora y elimina perspectivas que superan `expires_at`

### Puntuacion de Calidad de Perspectivas

Despues de generar una perspectiva, una segunda llamada LLM la califica:

```
System: "Rate the following insight on a scale of 1-5 for novelty and usefulness."
User: [texto de la perspectiva]
Response: digito unico 1-5
```

Si `quality_threshold > 0` y la puntuacion esta por debajo del umbral, la perspectiva se descarta. Esto evita que perspectivas de baja calidad saturen la memoria.

## Embeddings

### Proveedor ONNX

Iulita usa un runtime ONNX en Go puro (`knights-analytics/hugot`) para generar embeddings localmente — sin necesidad de llamadas a API externas.

- **Modelo**: `KnightsAnalytics/all-MiniLM-L6-v2` — transformador de oraciones, 384 dimensiones
- **Runtime**: Go puro (sin CGo, sin bibliotecas compartidas)
- **Seguridad de hilos**: Protegido por `sync.Mutex` (el pipeline de hugot no es seguro para hilos)
- **Cache de modelo**: Se descarga una vez a `~/.local/share/iulita/models/`, se reutiliza en ejecuciones posteriores

### Almacenamiento de Vectores

Los embeddings se almacenan como BLOBs binarios en SQLite:

- **Codificacion**: Cada `float32` → 4 bytes LittleEndian, empaquetados en `[]byte`
- **384 dimensiones** → 1536 bytes por vector
- **Tablas**: `fact_vectors` (fact_id PK), `insight_vectors` (insight_id PK)
- **Eliminacion en cascada**: eliminar un dato/perspectiva elimina automaticamente su vector

### Cache de Embeddings

La tabla `embedding_cache` evita recalcular embeddings para textos identicos:

- **Clave**: hash SHA-256 del texto de entrada
- **Expulsion LRU**: mantiene solo las N entradas accedidas mas recientemente (predeterminado 10,000)
- **Usado por**: wrapper `CachedEmbeddingProvider` alrededor de ONNX

### Algoritmo de Busqueda Hibrida

```python
# Pseudocodigo
def hybrid_search(query, user_id, limit):
    # 1. Resultados FTS5 (sobremuestreados)
    fts_results = FTS_MATCH(query, limit * 2)
    fts_scores = {r.id: 1 - i/(len+1) for i, r in enumerate(fts_results)}

    # 2. Similitud vectorial
    query_vec = onnx.embed(query)
    all_vecs = load_all_vectors(user_id)
    vec_scores = {id: cosine_similarity(query_vec, vec) for id, vec in all_vecs}

    # 3. Combinar
    all_ids = set(fts_scores) | set(vec_scores)
    combined = {}
    for id in all_ids:
        fts = fts_scores.get(id, 0)
        vec = vec_scores.get(id, 0)
        combined[id] = (1 - vectorWeight) * fts + vectorWeight * vec

    # 4. Top-N
    return sorted(combined, key=combined.get, reverse=True)[:limit]
```

**Configuracion**: `skills.memory.vector_weight` (predeterminado 0, solo FTS). Establecer en 0.3-0.5 para busqueda hibrida.

## Memoria en el Bucle del Asistente

Cada mensaje activa la inyeccion de memoria en el prompt del sistema:

1. **Datos recientes** (hasta 20): cargados de la BD, decaimiento + MMR aplicados, formateados como `## Remembered Facts`
2. **Perspectivas relevantes** (hasta 5): busqueda hibrida usando el texto del mensaje, formateadas como `## Insights`
3. **Perfil del usuario** (tech facts): metadatos de comportamiento agrupados por categoria, formateados como `## User Profile`
4. **Directiva del usuario**: instruccion personalizada persistente, formateada como `## User Directives`

Este contexto aparece en el **prompt dinamico del sistema** (por mensaje, no cacheado por Claude).

## Exportacion / Importacion de Memoria

### Exportacion

```go
memory.ExportFacts(ctx, store, chatID) // → cadena Markdown
memory.ExportAllFacts(ctx, store, dir) // → un archivo .md por chat
```

Formato:
```markdown
## Fact 42
The user prefers dark mode in all IDEs.

## Fact 43
User's favorite programming language is Go.
```

### Importacion

```go
memory.ImportFacts(ctx, store, chatID, markdownContent)
```

Parsea el markdown, crea nuevos datos (los IDs originales se descartan — SQLite autoincrement asigna nuevos IDs). Cada dato importado se embede automaticamente.

## Referencia de Configuracion

| Parametro | Predeterminado | Descripcion |
|-----------|----------------|-------------|
| `skills.memory.half_life_days` | 30 | Vida media del decaimiento temporal; 0 = deshabilitado |
| `skills.memory.mmr_lambda` | 0 | Diversidad MMR (0 = deshabilitado, 0.7 recomendado) |
| `skills.memory.vector_weight` | 0 | Mezcla de busqueda hibrida (0 = solo FTS, 0.5 = equilibrado) |
| `skills.insights.min_facts` | 20 | Datos minimos para activar generacion de perspectivas |
| `skills.insights.max_pairs` | 6 | Pares maximos entre clusters por ejecucion |
| `skills.insights.ttl` | 720h | TTL de expiracion de perspectivas (30 dias) |
| `skills.insights.interval` | 24h | Frecuencia de generacion de perspectivas |
| `skills.insights.quality_threshold` | 0 | Puntuacion minima de calidad (0 = aceptar todas) |
| `embedding.model` | `KnightsAnalytics/all-MiniLM-L6-v2` | Nombre del modelo ONNX |
| `embedding.enabled` | true | Habilitar embeddings ONNX |
