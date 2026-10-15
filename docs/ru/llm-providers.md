# LLM-провайдеры

Iulita поддерживает несколько LLM-провайдеров через архитектуру на основе декораторов. Провайдеры могут быть объединены в цепочки с уровнями retry, fallback, кэширования, маршрутизации и классификации.


## Профили и роли задач

**Настройки → Модели** управляет зашифрованными подключениями и независимыми
профилями Claude, OpenAI-совместимых сервисов, Ollama, DeepSeek и Z.ai. Роли
Everyday, Complex, Vision и Background задаются версионируемой policy.
Новые профили требуют явных text/tool probes, для изображений — image probe.
Платный классификатор и резервные профили имеют отдельные проверки и активацию.
Probes расходуют лимит провайдера. См. [профили и маршрутизацию](../model-routing.md):
presets, eligibility, отзыв, миграция и учёт. Каталог и цены — снимки; доступ
конкретного аккаунта подтверждается его probes.

Старые адаптеры и декораторы ниже описывают компоненты, а не всю managed policy.
Сохранённый legacy Claude routing может использовать Haiku; явный новый профиль
остаётся закреплённым на весь tool loop.

## Интерфейс провайдера

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

## Запрос / Ответ

### Структура запроса

```go
Request {
    StaticSystemPrompt  string          // кэшируется Claude (базовый + промпты навыков)
    SystemPrompt        string          // для каждого сообщения (время, факты, директивы)
    History             []ChatMessage   // история разговора
    Message             string          // текущее сообщение пользователя
    Images              []ImageAttachment
    Documents           []DocumentAttachment
    Tools               []ToolDefinition
    ToolExchanges       []ToolExchange  // накопленные раунды инструментов за ход
    ThinkingBudget      int64           // токены расширенного мышления (0 = отключено)
    ForceTool           string          // принудительный вызов конкретного инструмента
    RouteHint           string          // подсказка для маршрутизирующего провайдера
}
```

**Ключевое решение**: системный промпт разделён на `StaticSystemPrompt` (стабильный, кэшируемый) и `SystemPrompt` (динамический, для каждого сообщения). Провайдеры, отличные от Claude, используют `FullSystemPrompt()`, который объединяет оба.

### Структура ответа

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

## Провайдер Claude

Основной провайдер, использующий официальный `anthropic-sdk-go`.

### Возможности

- **Кэширование промптов**: `StaticSystemPrompt` получает `cache_control: ephemeral` — Claude кэширует этот блок между запросами, снижая стоимость входных токенов
- **Стриминг**: `CompleteStream` использует streaming API с обработкой `ContentBlockDeltaEvent`
- **Расширенное мышление**: при `ThinkingBudget > 0` добавляется конфигурация thinking и увеличивается max tokens
- **ForceTool**: использует `ToolChoiceParamOfTool(name)` для принудительного вызова конкретного инструмента (отключает thinking — ограничение API)
- **Обнаружение переполнения контекста**: проверяет сообщения об ошибках на "prompt is too long" / "context_length_exceeded" и оборачивает в sentinel `ErrContextTooLarge`
- **Поддержка документов**: PDF через `Base64PDFSourceParam`, текстовые файлы через `PlainTextSourceParam`
- **Поддержка изображений**: base64-кодированные изображения с типом медиа
- **Горячая перезагрузка**: модель, max tokens и API-ключ могут обновляться во время выполнения через `sync.RWMutex`

### Кэширование промптов

Разделение на статический/динамический промпт — ключ к эффективному использованию Claude:

```
Блок 1: StaticSystemPrompt (cache_control: ephemeral)
  ├── Базовый системный промпт (персона, инструкции)
  └── Системные промпты навыков (от всех включённых навыков)

Блок 2: SystemPrompt (без cache control)
  ├── ## Current Time
  ├── ## User Directives
  ├── ## User Profile (tech facts)
  ├── ## Remembered Facts
  ├── ## Insights
  └── ## Language directive (если не английский)
```

Блок 1 кэшируется Claude между запросами (стоит `cache_creation_input_tokens` при первом использовании, `cache_read_input_tokens` при последующих попаданиях). Блок 2 меняется с каждым сообщением и не кэшируется.

### Стриминг

Стриминг используется только когда `len(req.Tools) == 0` (ассистент отключает стриминг во время агентного цикла использования инструментов). Цикл обработки событий стриминга:

- `ContentBlockDeltaEvent` с `type == "text_delta"` -> вызов `callback(chunk)` и накопление
- `MessageStartEvent` -> захват входных токенов + метрик кэша
- `MessageDeltaEvent` -> захват выходных токенов

### Восстановление при переполнении контекста

Когда Claude API возвращает ошибку переполнения контекста:

1. `isContextOverflowError(err)` оборачивает её как `llm.ErrContextTooLarge`
2. Агентный цикл ассистента перехватывает через `llm.IsContextTooLarge(err)`
3. Если ещё не сжималось на этом ходу: принудительное сжатие истории и повтор (`i--`)
4. Если уже сжималось: распространение ошибки

### Конфигурация

| Ключ | По умолчанию | Описание |
|------|-------------|----------|
| `claude.api_key` | — | API-ключ Anthropic (обязателен) |
| `claude.model` | `claude-sonnet-4-6` | ID модели |
| `claude.max_tokens` | 4096 | Максимум выходных токенов |
| `claude.base_url` | — | Переопределение базового URL API |
| `claude.thinking` | `off` | Режим thinking: `off`, `low`, `medium`, `high` |

## Провайдер Ollama

Локальный LLM-провайдер для разработки и фоновых задач.

### Ограничения

- **Нет поддержки инструментов** — возвращает ошибку при `len(req.Tools) > 0`
- **Нет стриминга** — `CompleteStream` не реализован
- Использует `FullSystemPrompt()` (без преимуществ кэширования)

### Сценарии использования

- Локальная разработка без затрат на API
- Фоновые делегированные задачи (переводы, резюмирование)
- Дешёвый классификатор для `ClassifyingProvider`

### API

Вызывает `POST /api/chat` с сообщениями в OpenAI-совместимом формате. `ListModels()` обращается к `GET /api/tags` для обнаружения моделей.

### Конфигурация

| Ключ | По умолчанию | Описание |
|------|-------------|----------|
| `ollama.url` | `http://localhost:11434` | URL сервера Ollama |
| `ollama.model` | — | Название модели |

## Провайдер OpenAI

OpenAI-совместимый REST-клиент. Работает с любым OpenAI-совместимым сервисом (Together AI, Azure и т.д.).

### Ограничения

- **Нет поддержки инструментов** — аналогично Ollama
- Использует `FullSystemPrompt()`

### Конфигурация

| Ключ | По умолчанию | Описание |
|------|-------------|----------|
| `openai.api_key` | — | API-ключ |
| `openai.model` | — | ID модели |
| `openai.base_url` | `https://api.openai.com/v1` | Базовый URL API |

## ONNX-провайдер эмбеддингов

Локальная модель эмбеддингов на чистом Go для векторного поиска.

- **Модель**: `KnightsAnalytics/all-MiniLM-L6-v2` (384 измерения)
- **Рантайм**: `knights-analytics/hugot` — чистый Go ONNX (без CGo)
- **Потокобезопасность**: `sync.Mutex` (пайплайн hugot не потокобезопасен)
- **Кэширование**: Загружается один раз в `~/.local/share/iulita/models/`
- **Нормализация**: L2-нормализованные выходные векторы (готовы для косинусного сходства)

Подробности использования эмбеддингов см. в [Память и инсайты](memory-and-insights.md#эмбеддинги).

## Декораторы провайдеров

### RetryProvider

Оборачивает любой провайдер экспоненциальным backoff-retry:

- **Максимум попыток**: 3
- **Базовая задержка**: 500 мс
- **Максимальная задержка**: 8 с
- **Джиттер**: случайный множитель 0.5-1.5x
- **Повторяемые коды**: 429, 500, 502, 503, 529 (Anthropic перегружен)
- **Неповторяемые**: 4xx (кроме 429), переполнение контекста

### FallbackProvider

Пробует провайдеры по порядку, возвращает первый успешный. Полезен для цепочек `Claude → OpenAI`.

### CachingProvider

Кэш ответа включается явно для stateless-запросов с полной identity. Ключ
учитывает модель/policy и содержимое запроса; диалоги, вложения, инструменты
и opaque reasoning обходят кэш. Cache hit не создаёт новую попытку провайдера.
Приватная обработка телефона не использует обычный постоянный кэш.

### CachedEmbeddingProvider

Кэширует эмбеддинги по тексту:

- **Ключ**: SHA-256 входного текста
- **Максимум записей**: 10 000 (LRU-вытеснение)
- **Пакетирование**: промахи кэша группируются для одного вызова провайдера
- **Хранилище**: таблица SQLite `embedding_cache`

### RoutingProvider

Маршрутизирует к именованным провайдерам по `req.RouteHint`. Также разбирает префикс `hint:<name> <message>` в сообщении пользователя. Делегирует `CompleteStream` разрешённому провайдеру, если тот является `StreamingProvider`.

### ClassifyingProvider

Оборачивает `RoutingProvider`. На каждый запрос:

1. Отправляет запрос классификации дешёвому провайдеру (Ollama): "Classify: simple/complex/creative"
2. Устанавливает `RouteHint` на основе классификации
3. Маршрутизирует к соответствующему провайдеру

При ошибке классификатора используется провайдер по умолчанию.

### XMLToolProvider

Для провайдеров без нативного вызова инструментов (Ollama, OpenAI):

1. Внедряет блок `<available_tools>` XML в системный промпт
2. Добавляет инструкции: "To use a tool, respond with `<tool_use name="..."><input>{...}</input></tool_use>`"
3. Удаляет `Tools` из запроса
4. Разбирает XML-вызовы инструментов из ответа с помощью regex

## Managed routing и обработка ошибок

Общий router разрешает профили и роли, закрепляет policy snapshot на tool loop
и сохраняет provider/profile identity в учёте попыток. Planner использует Complex,
фоновые задачи, compression и delegation — общую background policy.
Классификатор включается явно и получает только ограниченный текущий неоднозначный
текст, без истории и tools. Проверка качества и резервные профили описаны
в [руководстве](../model-routing.md#paid-selection-and-backup-profiles).

Credential admission действует и для закреплённых snapshots. Emergency revoke
блокирует новые вызовы и пытается отменить уже допущенные. Ошибки авторизации,
баланса, входа и конфигурации не вызывают availability failover. После видимого
вывода или начала tool round профиль не меняется. Отправки приватных данных
телефона используют точный согласованный профиль/endpoint без retry и failover.
Повторы внутри Claude SDK выключены; политикой повторов управляет внешний router.

## Старый порядок декораторов

Legacy setup может составлять adapter → retry → fallback → cache → routing/classifier.
Эта схема не описывает managed admission и приватную обработку телефона.
Базовые Ollama/OpenAI adapters принимают текст; XML tool emulation — отдельная
обёртка, а не native tool calling.

Цены являются оценками каталога, а не текущим счётом провайдера. Неизвестные usage
и цены остаются неизвестными. См. [учёт](../model-routing.md#usage-and-operations).
