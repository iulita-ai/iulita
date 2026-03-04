# Начало работы

## Обзор

Iulita — персональный AI-ассистент, который учится на ваших реальных данных, а не на галлюцинациях. Он хранит только проверенные факты, которые вы явно сообщаете, строит инсайты путём перекрёстного анализа ваших данных и никогда не выдумывает того, чего не знает.

**Консоль в первую очередь**: по умолчанию запускает полноэкранный TUI-чат. Также может работать как headless-сервер с Telegram, Web Chat и веб-дашбордом.

## Установка

### Вариант 1: Скачать готовый бинарник

Скачайте последний релиз со страницы [GitHub Releases](https://github.com/iulita-ai/iulita/releases/latest):

```bash
# macOS (Apple Silicon)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-darwin-arm64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-darwin-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/

# Linux (x86_64)
curl -L https://github.com/iulita-ai/iulita/releases/latest/download/iulita-linux-amd64 -o iulita
chmod +x iulita
sudo mv iulita /usr/local/bin/
```

### Вариант 2: Сборка из исходников

**Требования**: Go 1.25+, Node.js 22+, npm

```bash
git clone https://github.com/iulita-ai/iulita.git
cd iulita
make build
```

Эта команда собирает фронтенд Vue 3 и бинарник Go. Результат — `./bin/iulita`.

Чтобы собрать только бинарник Go (без фронтенда):

```bash
make build-go
```

### Вариант 3: Docker

```bash
cp config.toml.example config.toml
# Отредактируйте config.toml — как минимум укажите claude.api_key
mkdir -p data
docker compose up -d
```

Готовый образ:

```yaml
services:
  iulita:
    image: ghcr.io/iulita-ai/iulita:latest
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/app/data
      - ./config.toml:/app/config.toml:ro
```

При первом запуске без конфигурации сервер стартует в **режиме настройки** — веб-мастер на `http://localhost:8080` проведёт вас через выбор провайдера, настройку функций и импорт TOML.

## Первый запуск

### Интерактивный мастер настройки

```bash
iulita init
```

Мастер проведёт вас через:
1. **Выбор LLM-провайдера** — Claude (рекомендуется), OpenAI или Ollama
2. **Ввод API-ключа** — сохраняется в системном хранилище (macOS Keychain, Linux SecretService)
3. **Необязательные интеграции** — токен Telegram-бота, прокси, провайдер эмбеддингов
4. **Выбор модели** — динамически загружает доступные модели от выбранного провайдера

Секреты хранятся в системном хранилище ключей, с резервным вариантом — зашифрованный файл `~/.config/iulita/encryption.key`.

### Запуск консольного TUI (режим по умолчанию)

```bash
iulita
```

Запускает интерактивный полноэкранный TUI. Вводите сообщения, используйте `/help` для списка команд.

**Консольные команды:**
| Команда | Описание |
|---------|----------|
| `/help` | Показать доступные команды |
| `/status` | Показать количество навыков, дневные расходы, токены сессии |
| `/compact` | Вручную сжать историю чата |
| `/clear` | Очистить историю чата в памяти |
| `/quit` / `/exit` | Выйти из приложения |

**Горячие клавиши:**
- `Enter` — Отправить сообщение
- `Ctrl+C` — Выйти
- `Shift+Enter` — Новая строка в сообщении

### Запуск серверного режима

Для работы в фоне с Telegram, Web Chat и дашбордом:

```bash
iulita --server
```

Или эквивалентно:
```bash
iulita -d
```

Дашборд доступен по адресу `http://localhost:8080` (настраивается через `server.address`).

## Конфигурация

Все настройки хранятся в `config.toml` (необязателен — для локальной установки достаточно API-ключа в хранилище). Любой параметр можно переопределить через переменные окружения с префиксом `IULITA_`.

### Расположение файлов (XDG-совместимое)

| Платформа | Конфигурация | Данные | Кэш | Логи |
|-----------|-------------|--------|-----|------|
| **macOS** | `~/Library/Application Support/iulita/` | `~/Library/Application Support/iulita/` | `~/Library/Caches/iulita/` | `~/Library/Application Support/iulita/` |
| **Linux** | `~/.config/iulita/` | `~/.local/share/iulita/` | `~/.cache/iulita/` | `~/.local/state/iulita/` |

Переопределить все пути можно переменной окружения `IULITA_HOME`.

### Основные переменные окружения

| Переменная | Описание |
|-----------|----------|
| `IULITA_CLAUDE_API_KEY` | API-ключ Anthropic (обязателен для Claude) |
| `IULITA_TELEGRAM_TOKEN` | Токен Telegram-бота |
| `IULITA_CLAUDE_MODEL` | ID модели Claude |
| `IULITA_STORAGE_PATH` | Путь к базе данных SQLite |
| `IULITA_SERVER_ADDRESS` | Адрес прослушивания дашборда (`:8080`) |
| `IULITA_PROXY_URL` | HTTP/SOCKS5 прокси для всех запросов |
| `IULITA_JWT_SECRET` | Ключ подписи JWT (генерируется автоматически, если не задан) |
| `IULITA_HOME` | Переопределение всех XDG-путей |

Полный справочник со всеми настройками навыков см. в [`config.toml.example`](../../config.toml.example).

## Справочник CLI

| Команда / Флаг | Описание |
|----------------|----------|
| `iulita` | Запустить интерактивный консольный TUI (по умолчанию) |
| `iulita --server` / `-d` | Запустить как headless-сервер |
| `iulita init` | Интерактивный мастер настройки |
| `iulita init --print-defaults` | Вывести config.toml по умолчанию |
| `iulita --doctor` | Запустить диагностику |
| `iulita --version` / `-v` | Вывести версию и выйти |

## Быстрая проверка

После настройки проверьте, что всё работает:

```bash
# Запустить диагностику
iulita --doctor

# Запустить TUI
iulita

# Введите: "запомни, что мой любимый цвет — синий"
# Затем: "какой мой любимый цвет?"
```

Если ассистент правильно вспомнит "синий", память работает корректно.

## Дальнейшие шаги

- [Архитектура](architecture.md) — как устроена система
- [Память и инсайты](memory-and-insights.md) — хранение фактов и перекрёстные ссылки
- [Каналы](channels.md) — настройка Telegram, Web Chat или TUI
- [Навыки](skills.md) — все 20+ доступных инструментов
- [Конфигурация](configuration.md) — подробный разбор всех настроек
- [Развёртывание](deployment.md) — Docker, Kubernetes и продакшен
