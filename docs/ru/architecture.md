# Архитектура

## Общий обзор

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

## Основные принципы проектирования

1. **Фактологическая память** — хранятся только проверенные данные пользователя, никогда не галлюцинации
2. **Консоль в первую очередь** — TUI по умолчанию; серверный режим подключается отдельно
3. **Чистая архитектура** — доменные модели -> интерфейсы -> реализации -> оркестратор
4. **Мультиканальность, единая личность** — факты и инсайты разделяются между каналами через user_id
5. **Установка без конфигурации** — работает из коробки с одним API-ключом
6. **Горячая перезагрузка** — навыки, конфигурация и даже токен Telegram могут меняться без перезапуска

## Карта компонентов

| Компонент | Пакет | Описание |
|-----------|-------|----------|
| Точка входа | `cmd/iulita/` | Разбор CLI, внедрение зависимостей, graceful shutdown |
| Ассистент | `internal/assistant/` | Оркестратор: цикл LLM, память, сжатие, подтверждения, стриминг |
| Каналы | `internal/channel/` | Входные адаптеры: Console TUI, Telegram, WebChat |
| Менеджер каналов | `internal/channelmgr/` | Жизненный цикл каналов, маршрутизация, горячая перезагрузка |
| LLM-провайдеры | `internal/llm/` | Claude, Ollama, OpenAI, ONNX-эмбеддинги |
| Навыки | `internal/skill/` | 20+ реализаций инструментов |
| Менеджер навыков | `internal/skillmgr/` | Внешние навыки: маркетплейс ClawhHub, URL, локальные |
| Хранилище | `internal/storage/sqlite/` | SQLite с FTS5, векторами, WAL-режимом |
| Планировщик | `internal/scheduler/` | Очередь задач с поддержкой cron/interval |
| Дашборд | `internal/dashboard/` | GoFiber REST API + встроенный Vue 3 SPA |
| Конфигурация | `internal/config/` | Многослойная конфигурация: defaults -> TOML -> env -> keyring -> DB |
| Аутентификация | `internal/auth/` | JWT + bcrypt, middleware |
| i18n | `internal/i18n/` | 6 языков, TOML-каталоги, распространение через контекст |
| Веб-поиск | `internal/web/` | Brave + DuckDuckGo (резерв), защита от SSRF |
| Домен | `internal/domain/` | Чистые доменные модели |
| Память | `internal/memory/` | TF-IDF кластеризация, экспорт/импорт |
| Метрики | `internal/metrics/` | Prometheus-счётчики и гистограммы |
| События | `internal/eventbus/` | Шина событий publish/subscribe |
| Расходы | `internal/cost/` | Отслеживание расходов LLM с дневными лимитами |
| Ограничения | `internal/ratelimit/` | Ограничение частоты запросов: по чату и глобально |
| Фронтенд | `ui/` | Vue 3 + Naive UI + UnoCSS SPA |

## Порядок запуска

Последовательность запуска строго упорядочена для соблюдения зависимостей:

```
1. Разбор CLI-аргументов, определение XDG-путей, создание директорий
2. Обработка подкоманд: init, --version, --doctor (ранний выход)
3. Загрузка конфигурации: defaults → TOML → env → keyring
4. Создание логгера (в консольном режиме перенаправление в файл)
5. Открытие SQLite, выполнение миграций
6. Инициализация каталога i18n (после миграций, до навыков)
7. Создание администратора (до backfill)
8. BackfillUserIDs (привязка устаревших данных к пользователям)
9. Создание хранилища конфигурации, загрузка DB-переопределений
10. Проверка режима настройки (нет LLM + нет мастера = только setup)
11. Валидация конфигурации
12. Создание сервиса аутентификации
13. Инициализация экземпляров каналов
14. Создание ONNX-провайдера эмбеддингов (опционально)
15. Сборка цепочки LLM-провайдеров (Claude → retry → fallback → cache → router)
16. Регистрация всех навыков (безусловно — контроль через capability)
17. Создание ассистента
18. Подключение шины событий (config reload, метрики, расходы, уведомления)
19. Воспроизведение DB-переопределений конфигурации (горячая перезагрузка для учётных данных из дашборда)
20. Создание менеджера каналов, планировщика, воркера
21. Запуск планировщика, воркера, цикла ассистента
22. Запуск сервера дашборда
23. Запуск всех каналов
24. Ожидание сигнала завершения
```

## Graceful Shutdown (7 фаз)

```
1. Остановка всех каналов (прекращение приёма новых сообщений)
2. Ожидание фоновых горутин ассистента
3. Ожидание фонового заполнения эмбеддингов
4. Закрытие ONNX-провайдера
5. Остановка шины событий (ожидание асинхронных обработчиков)
6. Ожидание планировщика/воркера/дашборда (тайм-аут 10 с)
7. Закрытие соединения SQLite (последним)
```

## Поток обработки сообщений

Когда пользователь отправляет сообщение, вот полный путь выполнения:

```
Пользователь вводит "запомни, что я люблю Go"
    │
    ▼
Channel (Telegram/WebChat/Console)
    │ создаёт IncomingMessage с полями, специфичными для платформы
    │ устанавливает битовую маску ChannelCaps (streaming, markdown и т.д.)
    ▼
UserResolver (только Telegram/Console)
    │ сопоставляет идентификатор платформы → UUID iulita
    │ авто-регистрирует новых пользователей, если разрешено
    ▼
Channel Manager
    │ маршрутизирует к Assistant.HandleMessage
    ▼
Assistant — Фаза 1: Настройка контекста
    │ тайм-аут, роль пользователя, локаль, caps → контекст
    │ проверка ожидающего подтверждения → выполнение при одобрении
    ▼
Assistant — Фаза 2: Обогащение
    │ сохранение сообщения в БД
    │ фоново: TechFactAnalyzer (кириллица/латиница, длина сообщения)
    │ отправка события "processing"
    ▼
Assistant — Фаза 3: История и сжатие
    │ загрузка последних 50 сообщений
    │ если токены > 80% окна контекста → сжатие старшей половины
    ▼
Assistant — Фаза 4: Контекстные данные
    │ загрузка директивы, недавних фактов, релевантных инсайтов
    │ гибридный поиск: FTS5 + ONNX-векторы + MMR-ранжирование
    │ загрузка tech facts (профиль пользователя)
    │ определение часового пояса
    ▼
Assistant — Фаза 5: Сборка промпта
    │ статический промпт = базовый + системные промпты навыков (кэшируется Claude)
    │ динамический промпт = время + директивы + профиль + факты + инсайты + язык
    ▼
Assistant — Фаза 6: Определение принудительного инструмента
    │ ключевое слово "запомни" → ForceTool = "remember"
    ▼
Assistant — Фаза 7: Агентный цикл (максимум 10 итераций)
    │ Вызов LLM (стриминг если нет инструментов, иначе стандартный)
    │ При переполнении контекста → принудительное сжатие → повтор
    │ Если вызовы инструментов:
    │   ├── проверка уровня подтверждения
    │   ├── выполнение навыка
    │   ├── накопление в ToolExchanges
    │   └── следующая итерация
    │ Если нет вызовов инструментов → возврат ответа
    ▼
Выполнение навыка (напр., RememberSkill)
    │ проверка дубликатов через FTS-поиск
    │ сохранение в SQLite → срабатывание FTS-триггера
    │ фоново: ONNX-эмбеддинг → fact_vectors
    ▼
Ответ отправляется обратно через канал пользователю
```

## Ключевые интерфейсы

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
    InputSchema() json.RawMessage  // nil для текстовых навыков
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

Необязательные интерфейсы: `CapabilityAware`, `ConfigReloadable`, `ApprovalDeclarer`.

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
```

### Storage

```go
type Repository interface {
    // Сообщения
    SaveMessage(ctx, msg) error
    GetHistory(ctx, chatID, limit) ([]ChatMessage, error)

    // Память
    SaveFact(ctx, fact) error
    SearchFacts(ctx, chatID, query, limit) ([]Fact, error)
    SearchFactsByUser(ctx, userID, query, limit) ([]Fact, error)
    SearchFactsHybridByUser(ctx, userID, query, queryVec, limit) ([]Fact, error)

    // Задачи
    CreateTask(ctx, task) error
    ClaimTask(ctx, workerID, capabilities) (*Task, error)

    // ... 60+ методов всего
}
```

## Шина событий

Шина событий (`internal/eventbus/`) реализует типизированный паттерн publish/subscribe. События проходят между компонентами без прямой связности:

| Событие | Издатель | Подписчики |
|---------|----------|------------|
| `MessageReceived` | Assistant | Метрики, WebSocket hub |
| `ResponseSent` | Assistant | Метрики, WebSocket hub |
| `LLMUsage` | Assistant | Метрики, трекер расходов |
| `SkillExecuted` | Assistant | Метрики |
| `TaskCompleted` | Worker | WebSocket hub |
| `TaskFailed` | Worker | WebSocket hub |
| `FactSaved` | Storage | WebSocket hub |
| `InsightCreated` | Storage | WebSocket hub |
| `ConfigChanged` | Config store | Обработчик перезагрузки конфигурации -> навыки |

## Цепочка LLM-провайдеров

Провайдеры компонуются как декораторы:

```
Claude Provider
    └→ Retry Provider (3 попытки, экспоненциальный backoff, 429/5xx)
        └→ Fallback Provider (Claude → OpenAI)
            └→ Caching Provider (SHA-256 ключ, 60 мин TTL)
                └→ Routing Provider (маршрутизация по RouteHint)
                    └→ Classifying Provider (классификатор Ollama → выбор маршрута)
```

Для провайдеров, не поддерживающих нативный вызов инструментов (Ollama, OpenAI), обёртка `XMLToolProvider` внедряет определения инструментов как XML в системный промпт и разбирает XML-вызовы из ответа.

## Области данных

Все данные привязаны к `user_id` для кроссканального доступа:

```
User (iulita UUID)
    ├── user_channels (привязка Telegram, WebChat, ...)
    ├── chat_messages (из всех каналов)
    ├── facts (общие между каналами)
    ├── insights (общие между каналами)
    ├── directives (для каждого пользователя)
    ├── tech_facts (поведенческий профиль)
    ├── reminders
    └── todo_items
```

Пользователь в Telegram может вспомнить факты, сохранённые через консольный TUI, потому что оба канала ведут к одному `user_id`.

## Структура проекта

```
cmd/iulita/              # точка входа, внедрение зависимостей, graceful shutdown
internal/
  assistant/             # оркестратор (цикл LLM, память, сжатие, подтверждения)
  channel/
    console/             # bubbletea TUI
    telegram/            # Telegram-бот
    webchat/             # WebSocket веб-чат
  channelmgr/            # менеджер жизненного цикла каналов
  config/                # TOML + env + keyring конфигурация, мастер настройки
  domain/                # доменные модели
  auth/                  # JWT-аутентификация + bcrypt
  i18n/                  # интернационализация (6 языков, TOML-каталоги)
  llm/                   # LLM-провайдеры (Claude, Ollama, OpenAI, ONNX)
  scheduler/             # очередь задач (планировщик + воркер)
  skill/                 # реализации навыков
  skillmgr/              # менеджер внешних навыков (ClawhHub, URL, локальные)
  storage/sqlite/        # SQLite-репозиторий, FTS5, векторы, миграции
  dashboard/             # GoFiber REST API + Vue SPA
  web/                   # веб-поиск (Brave, DuckDuckGo, защита от SSRF)
  memory/                # TF-IDF кластеризация, экспорт/импорт
  eventbus/              # шина событий publish/subscribe
  cost/                  # отслеживание расходов LLM
  metrics/               # Prometheus-метрики
  ratelimit/             # ограничение частоты запросов
  notify/                # push-уведомления
ui/                      # Vue 3 + Naive UI + UnoCSS фронтенд
skills/                  # текстовые файлы навыков (Markdown)
docs/                    # документация
```
