# API для мобильных, консольных и серверных клиентов

::: warning Доступность
Бекенд доступен начиная с v0.48.0. Все мобильные флаги по умолчанию выключены.
Готового Android-, iOS- или удалённого CLI-приложения пока нет. Серверные
симуляторы не подтверждают совместимость с Pixel/GrapheneOS.
:::

API использует native gRPC по HTTP/2 и TLS, пакет `iulita.mobile.v1`. OAuth
работает на отдельном HTTPS listener. JWT дашборда, его WebSocket и токены
воркеров не дают доступа к этому API.

## Виды регистраций и права

| Вид | Вход | Максимальные права | Назначение |
| --- | --- | --- | --- |
| `native` | Браузер, Authorization Code + PKCE S256 | Чаты, управление устройствами, receive/sync, данные и UI телефона | Нативное приложение и исполнитель на устройстве |
| `cli` | Device Authorization Grant, затем refresh | Чаты, управление устройствами, данные и UI; без receive/sync | Интерактивная удалённая консоль |
| `service` | Client Credentials, HTTP Basic | `chats:read`, при явном разрешении `chats:write` | Автоматическая интеграция одного владельца |

У каждой регистрации свой предел scopes и состояние отзыва. Запрашивайте только
нужное подмножество. OAuth-права не заменяют разрешения Android, локальное
согласие и lease исполнения. CLI не может выдавать себя за исполнитель телефона.
Сервис не получает данные телефона, UI и управление устройствами, даже если
владелец имеет такие права. Телефонные инструменты недоступны и внутри чата,
запущенного сервисом.

Сервис имеет отдельный principal ID и привязан к существующему активному
владельцу со сменённым начальным паролем. Его access token действует 300 секунд,
refresh token не выдаётся. Без указания scope запрашивается `chats:read`.
`chats:write` разрешает изменения чатов и расходы на модель; это отдельное
решение оператора.

## Подготовка сервера

Нужны существующая база аккаунтов и настроенные модели. Инициализация realm
выполняется офлайн, до включения функции:

```sh
iulita mobile-init --settings /etc/iulita/mobile-runtime.json
```

Команда создаёт закрытые ключи realm и OAuth-хранилище, не запускает listener.
Используйте
[пример runtime JSON](https://github.com/iulita-ai/iulita/blob/main/deploy/mobile/runtime.example.json).
Он задаёт точный HTTPS origin issuer, audience, абсолютные нормализованные пути
к существующей account DB, отдельной OAuth DB, каталогу ключей и TLS-файлам,
а также разные адреса IP:port. Сертификат должен покрывать issuer, TLS-ключ —
быть обычным файлом с закрытыми правами. При старте отсутствующие ключи не
генерируются, realm автоматически не сбрасывается.

```toml
[mobile]
chat_enabled = true
device_enabled = false
ui_enabled = false
vision_enabled = false
runtime_file = "/etc/iulita/mobile-runtime.json"
```

`ui_enabled` требует `device_enabled`, `vision_enabled` требует `ui_enabled`.
Кроме того, UI и Vision закрыты отдельным gate совместной проверки с нативным
клиентом: эта сборка отклоняет их включение при старте. Серверные механизмы
проверяются симуляторами, но один флаг не открывает их реальным клиентам.
Переменные окружения: `IULITA_MOBILE_CHAT_ENABLED`,
`IULITA_MOBILE_DEVICE_ENABLED`, `IULITA_MOBILE_UI_ENABLED`,
`IULITA_MOBILE_VISION_ENABLED`, `IULITA_MOBILE_RUNTIME_FILE`.

Оба listener требуют TLS 1.3. Ingress должен сохранить HTTP/2 к gRPC и
настроенный OAuth Host. Forwarded-заголовки не подтверждают TLS и личность.
[Примеры Traefik](https://github.com/iulita-ai/iulita/tree/main/deploy/mobile)
нужно адаптировать к своей сети. Health и metrics следует публиковать только
в подходящем административном контуре.

## Регистрация и администрирование

Регистрация — офлайн-действие оператора. Сначала остановите мобильный runtime:
работающий процесс удерживает эксклюзивную блокировку realm. Публичного
динамического endpoint регистрации нет.

Минимальный файл регистрации CLI:

```json
{"client_id":"personal-cli","name":"Personal console","kind":"cli","scopes":["chats:read","chats:write"]}
```

```sh
iulita mobile-client register --settings /etc/iulita/mobile-runtime.json --registration /etc/iulita/cli-registration.json
iulita mobile-client list --settings /etc/iulita/mobile-runtime.json
```

Для сервиса укажите `kind: "service"`, UUID существующего `owner_id` и явный
список scopes. Не задавайте `principal_id`, `epoch` и `disabled`: ими управляет
сервер. У CLI и service нет redirect URI; для native нужны явные HTTPS URI.

```sh
iulita mobile-client register --settings /etc/iulita/mobile-runtime.json --registration /etc/iulita/service-registration.json --secret-out /secure/service-secret
iulita mobile-client rotate-secret --settings /etc/iulita/mobile-runtime.json --client-id reporting-service --secret-out /secure/service-secret-next
iulita mobile-client disable --settings /etc/iulita/mobile-runtime.json --client-id reporting-service
```

Секрет генерируется в **новый** файл по абсолютному пути с правами `0600`.
Файл и запись каталога синхронизируются до изменения credential. Существующие
файлы не перезаписываются; секрет не выводится в stdout и `list`. Ротация сразу
инвалидирует старый секрет и выданные сервису токены, без периода перекрытия.
Отключение регистрации также отзывает её доступ. Основную native-регистрацию
этой командой отключить нельзя.

## OAuth-интеграция

Метаданные: `/.well-known/oauth-authorization-server`, публичные ключи:
`/oauth2/jwks` на issuer. Поля запросов и расширения описаны в
[OAuth OpenAPI](https://github.com/iulita-ai/iulita/blob/main/api/mobile-oauth.openapi.json).
Это OAuth2 с расширениями Iulita для привязки установки. OpenID Connect и
ID token не реализованы.

### Вход нативного приложения

Внешний браузер получает `state`, S256 PKCE и
`iulita_installation_key_thumbprint`. Redirect должен точно совпадать с
регистрацией. Перед обменом кода проверьте возвращённые issuer и state;
обмен включает verifier и `iulita_installation_jwk` — публичный P-256 JWK.
Для Android App Links можно настроить package и fingerprints релизного
сертификата: тогда сервер публикует `/.well-known/assetlinks.json`.
Без fingerprints endpoint возвращает 404.

Для AppAuth задайте basic OAuth config из RFC8414 metadata или закреплённых
endpoints вместо OIDC discovery. Отключите default nonce через
`setNonce(null)`; параметры `prompt`, `max_age`, `openid` и `offline_access`
не поддержаны. Свежая проверка учётных данных — `iulita_fresh_auth=true`,
для известной установки укажите `iulita_device_id`. До отправки сохраняйте
точный proof request с DER-подписью и label: повторное подписание того же
challenge может дать другие байты и конфликт после потерянного ACK.

### Измерение времени runtime (v0.49.0+)

GET `/oauth2/time?nonce=…` на точном TLS issuer с новым случайным32-byte
base64url nonce (43 символа). Token не требуется; endpoint не выдаёт прав,
ответ не кешируется. Сверьте nonce/issuer и `clock_id` с `ServerInfo.clock_id`.
В ответе есть `received_at_unix_ms`, `sent_at_unix_ms`, `resolution_ms` (1),
`clock_error_ms` (50), `max_sample_age_ms` (30000).

Привяжите интервал времени сервера к локальному монотонному времени через
измеренный RTT; учитывайте разрешение, погрешность, дрейф и возраст sample.
Получение lease не создаёт новые десять секунд. Неопределённый, старый или
повторённый ответ отклоняется. `SessionMirror.clock_id` должен совпадать с
runtime; тот же ID есть в commands/lease. Перезапуск или обнаруженный скачок
часов меняет process-local epoch и закрывает старые native sessions. Sample
не доказывает универсальную точность UTC и не делает отзыв мгновенным.

### Device Flow для CLI

1. Создать ключ установки P-256 и сохранить закрытую часть локально.
2. Отправить форму на `/oauth2/device_authorization`: `client_id`, нужный `scope`
   и `iulita_installation_key_thumbprint`.
3. Показать пользователю `verification_uri` и `user_code`. Пользователь вводит
   код, входит и подтверждает в браузере. Страница предупреждает о чужих кодах.
4. Опросить `/oauth2/token` с grant type
   `urn:ietf:params:oauth:grant-type:device_code`, полями `client_id`,
   `device_code`, `iulita_installation_jwk`. Client secret не используется.
5. Соблюдать interval, изначально 5 секунд. `authorization_pending` требует
   ожидания, `slow_down` увеличивает интервал на 5 секунд. При `access_denied`
   или `expired_token` остановиться. Запрос действует 600 секунд.

Начальные native/CLI-токены могут иметь stage `pending_activation` или
`provisional_recovery`; обычные active RPC им недоступны. Подпишите выданный
challenge ключом установки, вызовите `ActivateDevice` либо `CompleteRecovery`,
затем refresh для получения активного токена. Старый provisional JWT не
становится активным сам. При неопределённом ответе сохраняйте operation ID.
Refresh token ротируется; повторное использование может отозвать семейство.

### Серверные токены

POST `/oauth2/token`: HTTP Basic с ID и секретом регистрации, форма
`grant_type=client_credentials` и необязательный `scope`. Credentials в теле
формы отклоняются. Ответ содержит `iulita_principal_type: "service"`, без
challenge и refresh token. Передавайте access token в gRPC metadata
`authorization: Bearer …`. После истечения запросите новый. Для отзыва одного
токена используйте `/oauth2/revoke`, HTTP Basic того же клиента и поле `token`.

## Чаты и задачи телефона

[Protobuf](https://github.com/iulita-ai/iulita/tree/main/api/proto/iulita/mobile/v1)
задаёт RPC-контракт. Указывайте `ClientContext.protocol_version = 1`.
Изменения используют `Operation` с UUID и временем. Если ответ потерян,
повторите тот же ID с тем же смысловым payload; изменение payload под тем же
ID вызывает конфликт.

Чаты разделены по владельцу и conversation ID, имеют свои настройки, поколения
истории, turns и возобновляемые события. Очистка, отмена и отзыв блокируют
устаревшую работу; устаревший cursor может потребовать новый snapshot.
Обычный transcript не содержит сырых наблюдений и изображений телефона.
Старые dashboard history endpoints не дают обходного доступа к этим чатам.

Задачи требуют действующей связи owner/device/session и Control stream.
Серверный контракт покрывает статус, геолокацию, уведомления, фонарик/навигацию
и ограниченные UI-действия. Без будущего исполнителя на устройстве действия не
выполняются. UI дополнительно требует разрешённого сценария, локального
подтверждения, текущего snapshot, generation fencing и короткого lease.
Пустой список сценариев не разрешает ни одного UI-приложения. Потеря lease или
неопределённый исход дают `unknown_outcome`; мутацию нельзя повторять автоматически.

`PrepareProcessing` готовит точный вход, вопрос, получателя/profile/endpoint
и digest согласия. Будущий клиент должен показать именно этот preview и
подтвердить его digest через `ProcessResult`. Изменение получателя, данных или
прав инвалидирует согласие. Приватная обработка не повторяет отправку и не
переключает провайдера после неё. Сырые данные, изображения и приватный ответ
не сохраняются в обычную историю, события, факты и кэши. Ephemeral-результаты
теряются при истечении срока и перезапуске процесса. Явные временные связи
с внешними чатами отправляют фиксированные уведомления о состоянии, без сырых
данных телефона.

## Явный выбор телефона (v0.49.0+)

Поле `SendMessage.target_device_id` owner-checked и фиксируется с turn,
возвращается в `Turn`. Аргумент модели или CreateUITask не могут заменить его
другим owned device. Без поля фиксируется исходное устройство отправителя;
legacy turn без поля ограничен сохранённым caller, не клиентом повтора.
Service principal не может задать цель телефона; явный выбор требует
`devices:read`. Разрешения ОС, согласия и живая device authority проверяются
дополнительно. Смена target при повторе client_message_id — конфликт.

## Отзыв, резервные копии и проверка

Текущие account/client/device/family/realm epochs проверяются и при работе
stream, и перед исполнением. Отключение аккаунта, смена пароля, отзыв устройства
или семейства, отключение/ротация клиента и ротация realm блокируют зависимые
права. Отзыв не отменяет уже выполненное действие и не возвращает данные,
ранее отправленные провайдеру.

Сохраняйте согласованные копии account/OAuth DB и закрытые ключи realm.
На остановленном runtime команда `iulita mobile-rotate --settings …` ротирует
realm credentials; `--restore` инвалидирует восстановленные права новым signing
epoch. `--recover` применяется при прерванной офлайн-ротации. Простое возвращение
старых файлов и обычный запуск не являются безопасным сбросом отзыва.
Откат схем требует соответствующей резервной копии.

Серверные тесты проверяют TLS/gRPC, OAuth, ownership, конкурентную
идемпотентность, симуляцию устройства, приватность, lease и отзыв. Полный прогон:
`go test -race ./...`. Проверка контракта: `scripts/check_mobile_oauth.py`
с зависимостями из `scripts/requirements-mobile-contract.txt`. Разрешения,
Accessibility, фоновая работа и screenshots на Android/GrapheneOS требуют
проверки с клиентом на устройстве. Для iOS нужен отдельный дизайн возможностей.
