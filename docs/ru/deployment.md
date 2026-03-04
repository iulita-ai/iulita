# Развёртывание

## Локальная установка

### Бинарник

```bash
# Скачать и установить
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-linux-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# Настройка
iulita init        # интерактивный мастер
iulita             # запустить TUI (по умолчанию)
iulita --server    # headless-серверный режим
```

### Сборка из исходников

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build         # фронтенд + Go-бинарник → ./bin/iulita
make build-go      # только Go-бинарник (пропустить пересборку фронтенда)
```

**Требования**: Go 1.25+, Node.js 22+, npm

## Docker

### docker-compose.yml

```yaml
services:
  iulita:
    image: ghcr.io/iulita-ai/iulita:latest
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/app/data
      - ./config.toml:/app/config.toml:ro
      - ./skills:/app/skills:ro
    restart: unless-stopped
```

### Первый запуск (веб-мастер)

Без `config.toml` сервер стартует в **режиме настройки**:

1. Перейдите на `http://localhost:8080`
2. Завершите 5-шаговый мастер:
   - Приветствие / Импорт существующего TOML
   - Выбор LLM-провайдера
   - Конфигурация (API-ключи, модель)
   - Переключатели функций
   - Завершение
3. Мастер сохраняет конфигурацию в базу данных
4. Создаёт файл-маркер `db_managed` (отключает загрузку TOML)

### С файлом конфигурации

```bash
cp config.toml.example config.toml
# Отредактируйте config.toml — как минимум задайте claude.api_key
mkdir -p data
docker compose up -d
```

### Dockerfile (многоэтапный)

```
Этап 1 (ui-builder): node:22-alpine
    → npm ci + npm run build

Этап 2 (go-builder): golang:1.25-alpine
    → CGO_ENABLED=1 (требуется для SQLite)
    → Копирует UI dist перед Go-сборкой

Этап 3 (runtime): alpine:3.21
    → ca-certificates + tzdata
    → Непривилегированный пользователь "iulita" (UID 1000)
    → Экспонирует порт 8080
    → Entrypoint: iulita --server
```

**Volume**: `/app/data` для базы данных SQLite и кэша ONNX-моделей.

## Переменные окружения

Все ключи конфигурации маппятся в переменные окружения:

```bash
# Обязательные
IULITA_CLAUDE_API_KEY=sk-ant-...

# Необязательные
IULITA_TELEGRAM_TOKEN=123456:ABC...
IULITA_STORAGE_PATH=/app/data/iulita.db
IULITA_SERVER_ADDRESS=:8080
IULITA_PROXY_URL=socks5://proxy:1080
IULITA_JWT_SECRET=your-secret-here
IULITA_CLAUDE_MODEL=claude-sonnet-4-5-20250929
```

## Обратный прокси

### nginx

```nginx
server {
    listen 443 ssl;
    server_name iulita.example.com;

    ssl_certificate /etc/ssl/certs/iulita.crt;
    ssl_certificate_key /etc/ssl/private/iulita.key;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Поддержка WebSocket
    location /ws {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }

    location /ws/chat {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }
}
```

### Caddy

```caddyfile
iulita.example.com {
    reverse_proxy localhost:8080
}
```

Caddy обрабатывает WebSocket upgrade автоматически.

## Проверки здоровья

### Диагностика CLI

```bash
iulita --doctor
```

Проверяет:
- Доступность файла конфигурации
- Подключение к базе данных
- Доступность LLM-провайдера
- Доступность хранилища ключей
- Статус модели эмбеддингов

### Мониторинг здоровья Telegram

Канал Telegram вызывает `GetMe()` каждые 60 секунд. Последовательные сбои логируются. Это обнаруживает проблемы с сетью и отзыв токенов.

## Мониторинг

### Prometheus-метрики

Включите в конфигурации:

```toml
[metrics]
enabled = true
address = ":9090"
```

Ключевые метрики:
- `iulita_llm_requests_total` — объём вызовов LLM по провайдеру/статусу
- `iulita_llm_cost_usd_total` — накопленная стоимость
- `iulita_skill_executions_total` — паттерны использования навыков
- `iulita_messages_total` — объём сообщений (входящие/исходящие)
- `iulita_cache_hits_total` — эффективность кэша

### Контроль расходов

```toml
[cost]
daily_limit_usd = 10.0  # остановить вызовы LLM при достижении дневных расходов $10
```

Расходы отслеживаются в памяти (сбрасываются ежедневно) и персистятся в таблицу `usage_stats`.

## Резервное копирование

### База данных

База данных SQLite — единственный источник истины. Создайте резервную копию файла `{DataDir}/iulita.db`:

```bash
# Простое копирование (безопасно с WAL-режимом, когда нет записей)
cp ~/.local/share/iulita/iulita.db backup/

# С использованием SQLite backup API (безопасно во время записей)
sqlite3 ~/.local/share/iulita/iulita.db ".backup backup/iulita.db"
```

### Конфигурация

Если используется файловая конфигурация:
```bash
cp ~/.config/iulita/config.toml backup/
```

Если используется конфигурация, управляемая из БД (Docker-мастер):
- Конфигурация хранится в таблице `config_overrides` внутри базы данных
- Резервная копия БД включает конфигурацию

### Секреты

Секреты в хранилище ключей **не** включены в файловые резервные копии. Экспортируйте их:
```bash
export IULITA_CLAUDE_API_KEY=$(security find-generic-password -s iulita -a claude-api-key -w)  # macOS
```

## Makefile-цели

| Цель | Описание |
|------|----------|
| `make build` | Собрать фронтенд + Go-бинарник |
| `make build-go` | Только Go-бинарник |
| `make ui` | Собрать только Vue SPA |
| `make run` | Сборка + запуск консольного TUI |
| `make console` | Запуск TUI (go run, без сборки) |
| `make server` | Сборка + запуск headless-сервера |
| `make dev` | Dev-режим: Vue dev server + Go server |
| `make test` | Запуск всех тестов (Go + фронтенд) |
| `make test-go` | Только Go-тесты |
| `make test-ui` | Только фронтенд-тесты |
| `make test-coverage` | Покрытие для обоих |
| `make tidy` | go mod tidy |
| `make clean` | Удалить артефакты сборки |
| `make check-secrets` | Запустить сканирование gitleaks |
| `make setup-hooks` | Установить pre-commit hooks |
| `make release` | Создать тег и отправить релиз |

## Разработка

### Разработка с горячей перезагрузкой

```bash
make dev
```

Запускает:
1. Vue dev server с HMR на порту 5173
2. Go server с флагом `--server`

Vue dev server проксирует API-запросы к Go-серверу.

### Запуск тестов

```bash
make test              # все тесты
make test-go           # Go-тесты с race detector
make test-ui           # Vitest
make test-coverage     # отчёты о покрытии
```

### Pre-commit hooks

```bash
make setup-hooks
```

Устанавливает git pre-commit hook, который запускает `gitleaks detect` для предотвращения случайного коммита секретов.
