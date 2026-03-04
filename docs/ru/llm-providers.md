# LLM-провайдеры

Iulita поддерживает несколько LLM-провайдеров через архитектуру на основе декораторов. Провайдеры могут быть объединены в цепочки с уровнями retry, fallback, кэширования, маршрутизации и классификации.

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
| `claude.model` | `claude-sonnet-4-5-20250929` | ID модели |
| `claude.max_tokens` | 8192 | Максимум выходных токенов |
| `claude.base_url` | — | Переопределение базового URL API |
| `claude.thinking` | 0 | Бюджет расширенного мышления (0 = отключено) |

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
| `ollama.model` | `llama3` | Название модели |

## Провайдер OpenAI

OpenAI-совместимый REST-клиент. Работает с любым OpenAI-совместимым сервисом (Together AI, Azure и т.д.).

### Ограничения

- **Нет поддержки инструментов** — аналогично Ollama
- Использует `FullSystemPrompt()`

### Конфигурация

| Ключ | По умолчанию | Описание |
|------|-------------|----------|
| `openai.api_key` | — | API-ключ |
| `openai.model` | `gpt-4` | ID модели |
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

Кэширует ответы LLM по хешу входных данных:

- **Ключ**: SHA-256 от `systemPrefix[:200] + "|" + message`
- **TTL**: 60 минут (настраивается)
- **Максимум записей**: 1000 (LRU-вытеснение)
- **Пропуск**: запросы с инструментами или обменами инструментов (недетерминированные)
- **Хранилище**: таблица SQLite `response_cache`

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

## Сборка цепочки провайдеров

Цепочка собирается в `cmd/iulita/main.go`:

```
Claude Provider
    └→ Retry Provider
        └→ [Опционально] Fallback Provider (+ OpenAI)
            └→ [Опционально] Caching Provider
                └→ [Опционально] Routing Provider
                    └→ [Опционально] Classifying Provider (+ Ollama)
```

Каждый уровень добавляется условно на основе конфигурации.
