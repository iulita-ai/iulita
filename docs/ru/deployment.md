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

**Требования**: Go 1.26+ (toolchain 1.27.4), Node.js 22+, npm

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

### Проверки безопасности образов

Dockerfile в исходниках используют Alpine 3.24.4 и toolchain Go 1.27.4. Базовые образы закреплены по digest; установленные APK-пакеты обновляются при свежей сборке. Релизная сборка заново получает базу и не использует кеш слоёв, чтобы старый шаг установки пакетов не скрывал новые исправления. Пользователи без root и пути данных приложения и SSH-воркера сохранены. Отдельный `Dockerfile.mobile-gateway` обновляет nginx-шлюз и его системные пакеты.

Container Security запускается для pull request, main, еженедельно и вручную. Проверяется приложение на amd64, воркер и шлюз — на amd64 и arm64. Изолированные сценарии проверяют запись/чтение SQLite, офлайн-эмбеддинги из встроенной модели, извлечение PDF, OGG/Opus → MP3, TLS/HMAC воркера с тестовым SSH-сервером и запретом повтора после перезапуска, TLS/HTTP2 и маршруты шлюза. Данные и ключи — одноразовые тестовые.

Закреплённый Trivy читает архив каждого образа без Docker socket и прав публикации. Полный отчёт, CycloneDX SBOM и сведения о базе уязвимостей сохраняются в артефактах workflow. Исправимые HIGH/CRITICAL и ошибки сканера блокируют публикацию; неисправленные находки остаются видны в полном отчёте. Успех проверки не означает, что все возможные уязвимости известны или что каждая находка практически эксплуатируема.

При релизном теге отдельный этап сверяет все пять архивов, подтверждения проверок и контрольные суммы, затем публикует те же образы без пересборки. Манифесты воркера и шлюза публикуются раньше приложения. Проверка по расписанию не заменяет уже выпущенные версии. Закреплённые отдельно образы воркера и мобильного шлюза обновляются отдельно: обновления приложения для этого недостаточно. Исправления ядра хоста — самостоятельная операция.

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
Этап 1 (ui-builder): node:22-alpine3.24
    → npm ci + npm run build

Этап 2 (go-builder): golang:1.27.4-alpine3.24
    → CGO_ENABLED=1 (сборка образа; SQLite реализован на Go)
    → Копирует UI dist перед Go-сборкой

Этап 3 (runtime): alpine:3.24.4
    → ca-certificates + tzdata + ffmpeg + poppler-utils
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
IULITA_CLAUDE_MODEL=claude-sonnet-4-6
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

```txt
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


## Обновления и необязательный удалённый API

Прочитайте [обзор релизов](release-notes.md), включая требования v0.39 к откату
базы. Сохраняйте согласованную копию SQLite и ключ шифрования конфигурации.
ONNX-модель встроена в образ по `/opt/iulita/models`, вне writable data volume;
ffmpeg включён для конвертации аудио.

Бекенд mobile/CLI/service доступен с v0.48.0 и по умолчанию выключен. Ему нужны
собственные TLS listeners, ключи realm, OAuth DB и явная офлайн-инициализация.
UI/Vision flags закрыты до совместной проверки с нативным клиентом.
См. [руководство API](mobile-api.md). Не используйте старый WebChat и токены
дашборда/воркеров. Включите OAuth DB и ключи realm в резервное копирование;
восстановление требует явной инвалидации signing epoch.


## Обновление Telegram и readiness

Новая интеграция включает `day_widgets` и `day_actions`, даже если старая конфигурация их не содержит; явное false сохраняется. Сделайте backup БД, обновите бинарник/образ и перезапустите после изменения переключателей. Не переименовывайте маршруты и не выводите смену бота из alias: старый маршрут без привязки требует `/topic bind` перед автоматической разрушительной очисткой.

Команда из исходников `go run ./cmd/telegram-readiness` читает токен из переменной окружения и выводит публичную личность, активные имена и флаг тем. `-token-env` выбирает переменную секрета; `-test-environment` требует отдельных credentials тестового сервера. Команда не читает updates, не отправляет/редактирует сообщения и не доказывает отрисовку или право изменения задач. Только один polling-экземпляр должен получать updates одного бота. См. [эксплуатацию Telegram](telegram.md#эксплуатация).

Ручное удаление синхронизируется не мгновенно: проверяются известные маршруты, в том числе узко ограниченными validation-пробами наблюдаемых ответов Bot API. Документированного уведомления удаления или get-topic probe у Telegram нет. Неизвестное/изменённое поведение проверки не разрешает новую локальную очистку и может отключить пробу. Это отличается от явно подтверждённого удаления: его локальная очистка фиксируется до запроса Telegram и сохраняется при неизвестном результате запроса. Проверьте совместимость перед использованием синхронизации; сохраняйте явную привязку бота и backup для отката.
