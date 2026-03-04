# Планировщик

Планировщик — это двухкомпонентная система: **Координатор**, который создаёт задачи по расписанию, и **Воркер**, который захватывает и выполняет их. Оба используют SQLite как очередь задач.

## Архитектура

```
Scheduler (Координатор)
    │ опрос каждые 30 с
    │ проверяет тайминг задач по scheduler_states
    │
    ├── InsightJob (24ч) → insight.generate задачи
    ├── InsightCleanupJob (1ч) → insight.cleanup задачи
    ├── TechFactsJob (6ч) → techfact.analyze задачи
    ├── HeartbeatJob (6ч) → heartbeat.check задачи
    ├── RemindersJob (30с) → reminder.fire задачи
    ├── AgentJobsJob (30с) → agent.job задачи
    └── TodoSyncJob (ежечасный cron) → todo.sync задачи
           │
           ▼
    таблица tasks (SQLite)
           │
           ▼
Worker (Воркер)
    │ опрос каждые 5 с
    │ захватывает задачи атомарно
    │ передаёт зарегистрированным обработчикам
    │
    ├── InsightGenerateHandler
    ├── InsightCleanupHandler
    ├── TechFactAnalyzeHandler
    ├── HeartbeatHandler
    ├── ReminderFireHandler
    ├── AgentJobHandler
    └── TodoSyncHandler
```

## Координатор

### Определение задачи

```go
type JobDefinition struct {
    Name        string
    Interval    time.Duration
    CronExpr    string           // стандартный cron (5 полей)
    Timezone    string           // IANA-часовой пояс для cron
    Enabled     bool
    CreateTasks func(ctx) ([]domain.Task, error)
}
```

Каждая задача объявляет либо фиксированный `Interval`, либо `CronExpr`. Cron использует `robfig/cron/v3` с поддержкой часовых поясов.

### Цикл планирования

1. **Прогрев**: при первой загрузке `NextRun = now + 1 минута` (льготный период)
2. **Тик** каждые 30 секунд:
   - Обслуживание: возврат зависших задач (running > 5 мин), удаление старых задач (> 7 дней)
   - Для каждой включённой задачи: если `now >= state.NextRun`, вызвать `CreateTasks`
   - Вставка задач через `CreateTaskIfNotExists` (идемпотентно по `UniqueKey`)
   - Обновление состояния: `LastRun = now`, `NextRun = computeNextRun()`

### Ручной запуск

`TriggerJob(name)`:
- Находит именованную задачу
- Вызывает `CreateTasks` с `Priority = 1` (высокий)
- Вставляет задачи немедленно
- НЕ обновляет состояние расписания (следующий регулярный запуск произойдёт как обычно)

Доступно через дашборд: `POST /api/schedulers/:name/trigger`

## Воркер

### Захват задач

```
Каждые 5 секунд:
    для каждого доступного слота конкурентности:
        ClaimTask(ctx, workerID, capabilities)  // атомарная транзакция SQLite
        если задача захвачена:
            go executeTask(task)
        иначе:
            break  // больше нет доступных задач
```

`workerID = hostname-pid` (уникален для каждого процесса).

### Маршрутизация на основе capability

Задачи объявляют требуемые capabilities как строку через запятую (напр., `"llm,storage"`). Список capabilities воркера должен быть надмножеством.

**Capabilities локального воркера**: `["storage", "llm", "telegram"]`

**Удалённый воркер**: любой набор capabilities, аутентификация через `Scheduler.WorkerToken`.

### Жизненный цикл задачи

```
pending → claimed (воркером) → running → completed / failed
```

- `ClaimTask`: атомарные SELECT + UPDATE в транзакции
- `StartTask`: установка статуса `running`, запись времени начала
- `CompleteTask`: сохранение результата, публикация события `TaskCompleted`
- `FailTask`: сохранение ошибки, публикация события `TaskFailed`

### API удалённого воркера

Для распределённых развёртываний дашборд предоставляет REST API:

| Эндпоинт | Метод | Описание |
|----------|-------|----------|
| `/api/tasks/` | GET | Список задач |
| `/api/tasks/counts` | GET | Количество по статусам |
| `/api/tasks/claim` | POST | Захватить задачу |
| `/api/tasks/:id/start` | POST | Отметить как running |
| `/api/tasks/:id/complete` | POST | Завершить с результатом |
| `/api/tasks/:id/fail` | POST | Отметить как failed с ошибкой |

Аутентификация через статический bearer-токен (`scheduler.worker_token`).

## Встроенные задачи

### Генерация инсайтов (`insights`)

- **Интервал**: 24 часа (настраивается через `skills.insights.interval`)
- **Тип задачи**: `insight.generate`
- **Capabilities**: `llm,storage`
- **Условие**: у чата/пользователя должно быть >= `minFacts` (по умолчанию 20) фактов

**Конвейер обработчика:**
1. Загрузка всех фактов пользователя
2. Построение TF-IDF-векторов (токенизация, биграммы, TF-IDF-оценки)
3. K-means++ кластеризация: `k = sqrt(numFacts / 3)`, косинусное расстояние, 20 итераций
4. Выборка до 6 межкластерных пар (пропуск уже обработанных)
5. Для каждой пары: LLM генерирует инсайт + оценивает качество (1-5)
6. Сохранение инсайтов с качеством >= порога

### Очистка инсайтов (`insight_cleanup`)

- **Интервал**: 1 час
- **Тип задачи**: `insight.cleanup`
- **Capabilities**: `storage`

Удаляет инсайты, у которых `expires_at < now`. TTL по умолчанию — 30 дней.

### Анализ техфактов (`techfacts`)

- **Интервал**: 6 часов (настраивается)
- **Тип задачи**: `techfact.analyze`
- **Capabilities**: `llm,storage`
- **Условие**: 10+ сообщений, из которых 5+ от пользователя

**Обработчик**: Отправляет сообщения пользователя в LLM с запросом структурированного JSON: `[{category, key, value, confidence}]`. Категории включают темы, стиль общения и поведенческие паттерны. Выполняет upsert в таблицу `tech_facts`.

### Heartbeat (`heartbeat`)

- **Интервал**: 6 часов (настраивается)
- **Тип задачи**: `heartbeat.check`
- **Capabilities**: `llm,storage,telegram`

**Обработчик**: Собирает недавние факты, инсайты и ожидающие напоминания. Спрашивает LLM, нужно ли отправить check-in сообщение. Если ответ не `HEARTBEAT_OK`, отправляет сообщение пользователю.

### Напоминания (`reminders`)

- **Интервал**: 30 секунд
- **Тип задачи**: `reminder.fire`
- **Capabilities**: `telegram,storage`

**Обработчик**: Форматирует напоминание с локальным временем, отправляет через `MessageSender`, помечает как fired.

### Agent Jobs (`agent_jobs`)

- **Интервал**: 30 секунд
- **Тип задачи**: `agent.job`
- **Capabilities**: `llm`

Опрашивает `GetDueAgentJobs(now)` для пользовательских запланированных LLM-задач. Обновляет `next_run` немедленно (до выполнения) для предотвращения дублирования.

**Обработчик**: Вызывает `provider.Complete` с пользовательским промптом. Опционально доставляет результат в настроенный чат.

### Синхронизация задач (`todo_sync`)

- **Cron**: `0 * * * *` (ежечасно)
- **Тип задачи**: `todo.sync`
- **Capabilities**: `storage`

**Обработчик**: Перебирает все доступные экземпляры `TodoProvider` (Todoist, Google Tasks, Craft). Для каждого: `FetchAll` -> upsert в `todo_items` -> удаление устаревших записей.

## Agent Jobs (пользовательские)

Пользователи могут создавать запланированные LLM-задачи через дашборд:

```json
{
  "name": "Daily Summary",
  "prompt": "Summarize my recent facts and insights",
  "cron_expr": "0 9 * * *",
  "delivery_chat_id": "123456789"
}
```

Поля:
- `name` — отображаемое имя
- `prompt` — промпт для LLM
- `cron_expr` или `interval` — расписание
- `delivery_chat_id` — куда отправить результат (необязательно)

Управляется через дашборд: `GET/POST/PUT/DELETE /api/agent-jobs/`
