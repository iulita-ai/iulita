# Дашборд

Дашборд — это GoFiber REST API, обслуживающий встроенный Vue 3 SPA. Он предоставляет веб-интерфейс для управления всеми аспектами Iulita.

## Архитектура

```
GoFiber Server
    ├── /api/*          REST API (JWT-аутентификация)
    ├── /ws             WebSocket hub (обновления в реальном времени)
    ├── /ws/chat        Канал WebChat (отдельный эндпоинт)
    └── /*              Vue 3 SPA (встроенный, клиентская маршрутизация)
```

Vue SPA встроен в Go-бинарник через `//go:embed dist/*` и обслуживается с fallback на `index.html` для всех неизвестных путей.

## Аутентификация

| Эндпоинт | Аутентификация | Описание |
|----------|---------------|----------|
| `POST /api/auth/login` | Публичный | Проверка bcrypt-учётных данных, возврат access + refresh токенов |
| `POST /api/auth/refresh` | Публичный | Валидация refresh-токена, возврат нового access-токена |
| `POST /api/auth/change-password` | JWT | Смена собственного пароля |
| `GET /api/auth/me` | JWT | Профиль текущего пользователя |
| `PATCH /api/auth/locale` | JWT | Обновление локали для всех каналов |

**Детали JWT:**
- Алгоритм: HMAC-SHA256
- TTL access-токена: 24 часа
- TTL refresh-токена: 7 дней
- Claims: `user_id`, `username`, `role`
- Секрет: автогенерируется, если не задан

## REST API

### Публичные эндпоинты

| Метод | Путь | Описание |
|-------|------|----------|
| GET | `/api/system` | Информация о системе, версия, uptime, статус мастера |

### Пользовательские эндпоинты (требуется JWT)

| Метод | Путь | Описание |
|-------|------|----------|
| GET | `/api/stats` | Количество сообщений, фактов, инсайтов, напоминаний |
| GET | `/api/chats` | Список всех chat ID с количеством сообщений |
| GET | `/api/facts` | Список/поиск фактов (по chat_id, user_id, query) |
| PUT | `/api/facts/:id` | Обновить содержимое факта |
| DELETE | `/api/facts/:id` | Удалить факт |
| GET | `/api/facts/search` | FTS-поиск фактов |
| GET | `/api/insights` | Список инсайтов |
| GET | `/api/reminders` | Список напоминаний |
| GET | `/api/directives` | Получить директиву для чата |
| GET | `/api/messages` | История чата с пагинацией |
| GET | `/api/skills` | Список всех навыков со статусом включения/конфигурации |
| PUT | `/api/skills/:name/toggle` | Включить/отключить навык в рантайме |
| GET | `/api/skills/:name/config` | Схема конфигурации навыка + текущие значения |
| PUT | `/api/skills/:name/config/:key` | Установить ключ конфигурации навыка (авто-шифрование секретов) |
| GET | `/api/techfacts` | Поведенческий профиль по категориям |
| GET | `/api/usage/summary` | Использование токенов + оценка стоимости |
| GET | `/api/schedulers` | Статус задач планировщика |
| POST | `/api/schedulers/:name/trigger` | Ручной запуск задачи |

### Эндпоинты задач (требуется JWT)

| Метод | Путь | Описание |
|-------|------|----------|
| GET | `/api/todos/providers` | Список провайдеров задач |
| GET | `/api/todos/today` | Задачи на сегодня |
| GET | `/api/todos/overdue` | Просроченные задачи |
| GET | `/api/todos/upcoming` | Предстоящие задачи (по умолчанию 7 дней) |
| GET | `/api/todos/all` | Все незавершённые задачи |
| GET | `/api/todos/counts` | Количество сегодня + просроченных |
| POST | `/api/todos/` | Создать задачу |
| POST | `/api/todos/sync` | Запустить ручную синхронизацию задач |
| POST | `/api/todos/:id/complete` | Завершить задачу |
| DELETE | `/api/todos/:id` | Удалить встроенную задачу |

### Эндпоинты Google Workspace (требуется JWT)

| Метод | Путь | Описание |
|-------|------|----------|
| GET | `/api/google/status` | Статус аккаунта |
| POST | `/api/google/upload-credentials` | Загрузить файл OAuth-учётных данных |
| GET | `/api/google/auth` | Начать OAuth2-поток |
| GET | `/api/google/callback` | OAuth2-callback |
| GET | `/api/google/accounts` | Список аккаунтов |
| DELETE | `/api/google/accounts/:id` | Удалить аккаунт |
| PUT | `/api/google/accounts/:id` | Обновить аккаунт |

### Административные эндпоинты (требуется роль Admin)

| Метод | Путь | Описание |
|-------|------|----------|
| GET/PUT/DELETE | `/api/config/*` | Переопределения конфигурации, схема, отладка |
| GET/POST/PUT/DELETE | `/api/users/*` | CRUD пользователей + привязки каналов |
| GET/POST/PUT/DELETE | `/api/channels/*` | CRUD экземпляров каналов |
| GET/POST/PUT/DELETE | `/api/agent-jobs/*` | CRUD Agent Jobs |
| GET/POST/DELETE | `/api/skills/external/*` | Управление внешними навыками |
| GET/POST | `/api/wizard/*` | Мастер настройки |
| PUT | `/api/todos/default-provider` | Установить провайдер задач по умолчанию |

### Эндпоинты воркера (Bearer-токен)

| Метод | Путь | Описание |
|-------|------|----------|
| GET | `/api/tasks/` | Список задач планировщика |
| GET | `/api/tasks/counts` | Количество по статусам |
| POST | `/api/tasks/claim` | Захватить задачу (удалённый воркер) |
| POST | `/api/tasks/:id/start` | Отметить задачу как running |
| POST | `/api/tasks/:id/complete` | Завершить задачу |
| POST | `/api/tasks/:id/fail` | Отметить задачу как failed |

## WebSocket Hub

WebSocket hub по `/ws` обеспечивает обновления в реальном времени для подключённых клиентов дашборда.

### События

| Событие | Источник | Payload |
|---------|----------|---------|
| `task.completed` | Worker | Детали задачи |
| `task.failed` | Worker | Задача + ошибка |
| `message.received` | Assistant | Метаданные сообщения |
| `response.sent` | Assistant | Метаданные ответа |
| `fact.saved` | Storage | Детали факта |
| `insight.created` | Storage | Детали инсайта |
| `config.changed` | Config store | Ключ + значение |

События публикуются через шину событий с использованием `SubscribeAsync` (неблокирующе).

### Протокол

```json
// Сервер → Клиент
{"type": "task.completed", "payload": {...}}
{"type": "fact.saved", "payload": {...}}
```

## Vue 3 SPA

### Технологический стек

- **Vue 3** — Composition API
- **Naive UI** — библиотека компонентов
- **UnoCSS** — utility-first CSS
- **vue-i18n** — интернационализация (6 языков)
- **vue-router** — клиентская маршрутизация

### Представления

| Путь | Компонент | Аутентификация | Описание |
|------|-----------|---------------|----------|
| `/` | Dashboard | JWT | Обзор статистики, статус планировщика |
| `/facts` | Facts | JWT | Браузер фактов с поиском, редактированием, удалением |
| `/insights` | Insights | JWT | Список инсайтов |
| `/reminders` | Reminders | JWT | Список напоминаний |
| `/profile` | TechFacts | JWT | Метаданные поведенческого профиля |
| `/settings` | Settings | JWT | Управление навыками, редактор конфигурации |
| `/tasks` | Tasks | JWT | Вкладки Сегодня/Просроченные/Предстоящие/Все |
| `/chat` | Chat | JWT | WebSocket веб-чат |
| `/users` | Users | Admin | CRUD пользователей + привязки каналов |
| `/channels` | Channels | Admin | CRUD экземпляров каналов |
| `/agent-jobs` | AgentJobs | Admin | CRUD Agent Jobs |
| `/skills` | ExternalSkills | Admin | Маркетплейс + установленные навыки |
| `/setup` | Setup | Admin | Мастер настройки |
| `/config-debug` | ConfigDebug | Admin | Просмотр raw-переопределений конфигурации |
| `/login` | Login | Публичный | Форма входа |

### Guards маршрутизатора

```javascript
router.beforeEach((to, from, next) => {
    if (to.meta.public) { next(); return }
    if (!isLoggedIn()) { next({ name: 'login' }); return }
    if (to.meta.admin && !isAdmin()) { next({ name: 'dashboard' }); return }
    next()
})
```

### Ключевые composables

- `useWebSocket` — WebSocket с авто-переподключением и типизированными событиями
- `useLocale` — реактивное состояние локали, определение RTL, синхронизация с бэкендом
- `useSkillStatus` — управление видимостью элементов боковой панели на основе доступности навыков

### UI управления навыками

Представление Settings предоставляет:

1. **Переключатель навыков** — включение/отключение каждого навыка в рантайме
2. **Редактор конфигурации** — конфигурация для каждого навыка с:
   - Полями формы на основе схемы
   - Защитой секретных ключей (значения не утекают через API)
   - Авто-шифрованием чувствительных значений
   - Горячей перезагрузкой при сохранении

### Дашборд задач

Представление Tasks агрегирует задачи из всех провайдеров:

- **Вкладка Сегодня** — задачи на сегодня
- **Вкладка Просроченные** — просроченные задачи
- **Вкладка Предстоящие** — следующие 7 дней
- **Вкладка Все** — все незавершённые задачи
- **Кнопка Синхронизация** — запускает одноразовую задачу планировщика
- **Кнопка Создать** — новая задача с выбором провайдера

## Prometheus-метрики

При включении (`metrics.enabled = true`) метрики экспонируются на отдельном порту:

| Метрика | Тип | Labels |
|---------|-----|--------|
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

Метрики заполняются путём подписки на шину событий (неблокирующе).
