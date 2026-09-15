# Log Aggregator

Система агрегации и маршрутизации логов от микросервисов.  
Принимает логи по HTTP, валидирует, маршрутизирует по правилам, хранит и отдаёт через REST API и Web UI.

---

## Технологии

| Компонент | Технология |
|---|---|
| Язык | Go 1.22 |
| HTTP | `net/http` (стандартная библиотека) |
| Конфиг | `gopkg.in/yaml.v3` |
| Логирование | `log/slog` (стандартная библиотека, Go 1.21+) |
| Сериализация | `encoding/json` (стандартная библиотека) |
| Развёртывание | Docker + docker-compose |
| Web UI | HTML + JavaScript (без фреймворков) |
| Интеграция | Кастомный `slog.Handler` (`pkg/aggregatorslog`) |

---

## Архитектура

```
[auth-service]   ─┐
[order-service]  ─┼──► POST /api/v1/logs ──► validate ──► channel
[payment-service]─┘                                           │
                                                         worker pool
                                                              │
                                                        Router (fan-out)
                                                       /      │      \
                                                  [stdout] [file] [webhook]
                                                              │
                                                    Storage (memory + file)
                                                              │
                                              GET /api/v1/logs, /stats, /routes
                                              GET / (Web UI)
```

**Поток данных:**
1. Микросервис отправляет лог через `POST /api/v1/logs`
2. Хендлер валидирует и кладёт в буферный канал (неблокирующий)
3. Воркеры читают из канала и передают в Router
4. Router применяет правила из `config.yaml` (fan-out: лог идёт во все совпавшие правила)
5. Синки записывают лог: в stdout, файл и/или webhook
6. Storage сохраняет лог в памяти и на диск для последующего просмотра

---

## Структура проекта

```
log-aggregator/
├── cmd/
│   ├── aggregator/       # точка входа основного сервиса
│   └── demo-service/     # демо-микросервисы (auth, order, payment)
├── internal/
│   ├── model/            # структура LogEntry
│   ├── config/           # загрузка config.yaml
│   ├── ingest/           # HTTP-хендлеры и валидация
│   ├── worker/           # пул воркеров
│   ├── router/           # маршрутизация (fan-out)
│   ├── sink/             # назначения: stdout, file, webhook
│   ├── storage/          # хранилище логов
│   └── api/              # REST API и Web UI
├── web/
│   └── index.html        # дашборд (встроен в бинарь)
├── configs/
│   └── config.yaml       # пример конфигурации
├── deploy/
│   ├── Dockerfile.aggregator
│   ├── Dockerfile.demo
│   └── docker-compose.yml
└── README.md
```

---

## Формат лога

```json
{
  "timestamp":        "2026-09-12T17:00:00Z",
  "server_timestamp": "2026-09-12T17:00:00.123Z",
  "service":          "auth-service",
  "level":            "error",
  "message":          "authentication failed",
  "host":             "auth-1",
  "trace_id":         "abc-123",
  "fields":           { "user_id": "42", "ip": "1.2.3.4" }
}
```

- **Обязательные поля при отправке клиентом:** `service`, `level`, `message`
- **Автоматические поля сервера:** `server_timestamp` (время приёма и регистрации лога агрегатором)
- **Опциональные поля клиента:** `timestamp` (время возникновения события на клиенте; если не указано — остаётся пустым, а для сортировки и фильтров используется `server_timestamp`), `host`, `trace_id`, `fields`
- **Допустимые уровни:** `debug`, `info`, `warn`, `error`  
- **Нормализация уровней:** `warning`→`warn`, `fatal`/`critical`/`panic`→`error`, `trace`/`verbose`→`debug`

---

## Конфигурация

Файл: `configs/config.yaml`

```yaml
app:
  log_level: "info"     # уровень логирования агрегатора: debug, info, warn, error

server:
  port: 8080            # порт HTTP-сервера (1..65535)
  read_timeout: 10s
  write_timeout: 10s
  default_query_limit: 100 # число логов по умолчанию для GET /api/v1/logs
  max_query_limit: 1000    # максимальный limit для GET /api/v1/logs

worker:
  pool_size: 4        # число параллельных воркеров (1..256)
  buffer_size: 1024   # размер внутреннего канала (1..1 000 000)

storage:
  memory_limit: 5000  # последних N логов в памяти для API (10..100 000)

sinks:
  stdout:
    enabled: true
    format: "json"    # формат вывода: "json" или "text"
  file:
    enabled: true
    dir: "./logs"     # директория для сохранения файлов логов
    pattern: "flat"   # flat (YYYY-MM-DD.jsonl), by-service (service/YYYY-MM-DD.jsonl), by-date (YYYY-MM-DD/service.jsonl)
  webhook:
    enabled: false
    url: "http://localhost:9000/webhook"
    timeout: 5s
    retry_count: 3    # число повторных попыток при сбоях
    headers:
      Authorization: "Bearer your-token"

routes:
  - name: "auth-errors"
    match:
      service: "auth-service"
      level: "error"
    sinks: [file]

  - name: "warn-and-above"
    match:
      levels: [warn, error]
    sinks: [stdout]

  - name: "default"
    match: {}           # срабатывает, если ни одно правило не совпало
    sinks: [file, stdout] # по умолчанию сохраняет в файл и выводит в stdout
```

> **Гарантии сохранения и восстановления данных:**
> - `storage` хранит кольцевой буфер последних `storage.memory_limit` записей в оперативной памяти для быстрого поиска и Web UI.
> - При перезапуске агрегатор восстанавливает данные за текущую дату из директории файлового синка (`sinks.file.dir`).
> - Правило `default` из коробки направляет логи в `[file, stdout]`, гарантируя отсутствие потерь при перезапуске. Логи, явно исключённые правилами маршрутизации из синка `file` (например, направленные только в `discard` или `webhook`), доступны в API только во время текущей сессии процесса.


### Правила маршрутизации

Маршрутизация работает по принципу **fan-out**: лог направляется во **все** совпавшие правила одновременно.  
Правило `default` срабатывает только если ни одно другое правило не совпало.

**Условия совпадения** (`match`):

| Поле | Тип | Описание |
|---|---|---|
| `service` | string | Точное имя сервиса |
| `level` | string | Один уровень: `debug`, `info`, `warn`, `error` |
| `levels` | []string | Список уровней |
| `message_regex` | string | Регулярное выражение для поля `message` |

Пустой `match: {}` совпадает со всеми логами.

**Доступные синки:**

| Синк | Описание |
|---|---|
| `stdout` | JSON Lines в стандартный вывод |
| `file` | JSON Lines файл с ротацией по дате (`logs/YYYY-MM-DD.jsonl`) |
| `webhook` | HTTP POST на внешний URL |
| `discard` | Явно отбросить лог (без записи) |

### Как добавить новый синк

1. Создать файл `internal/sink/mysinc.go` — реализовать интерфейс:
   ```go
   type Sink interface {
       Name() string
       Write(ctx context.Context, entry model.LogEntry) error
   }
   ```
2. Добавить секцию конфига в `SinksConfig` (`internal/config/config.go`)
3. Зарегистрировать синк в `cmd/aggregator/main.go` — передать в `router.New()`

---

## Запуск

### Локально (без Docker)

```bash
# 1. Клонировать репозиторий и перейти в директорию
cd log-aggregator

# 2. Установить зависимости
go mod tidy

# 3. Запустить aggregator
go run ./cmd/aggregator -config configs/config.yaml

# 4. В отдельных терминалах — demo-сервисы
go run ./cmd/demo-service -service auth-service
go run ./cmd/demo-service -service order-service
go run ./cmd/demo-service -service payment-service

# 5. Открыть Web UI
# http://localhost:8080
```

### Docker Compose (рекомендуется)

```bash
# Из директории deploy/
cd deploy

# Собрать образы и запустить все сервисы
docker compose up --build

# В фоне:
docker compose up --build -d

# Остановить и удалить контейнеры
docker compose down

# Остановить и удалить вместе с volume (логи будут стёрты!)
docker compose down -v
```

После запуска:
- **Web UI**: [http://localhost:8080](http://localhost:8080)
- **API stats**: [http://localhost:8080/api/v1/stats](http://localhost:8080/api/v1/stats)
- **Healthcheck**: [http://localhost:8080/healthz](http://localhost:8080/healthz)

> Demo-сервисы стартуют только после того, как aggregator успешно пройдёт healthcheck (`GET /healthz`).  
> Это гарантирует, что логи не теряются при старте.

Посмотреть логи конкретного сервиса:
```bash
docker compose logs -f aggregator
docker compose logs -f auth-service
```

---

## API

### Документация OpenAPI / Swagger UI

Интерактивная документация и формальная спецификация API доступны прямо из работающего сервера:
- **Swagger UI (веб-интерфейс)**: [http://localhost:8080/docs](http://localhost:8080/docs) (или `/swagger`)
- **Спецификация OpenAPI 3.0 (YAML)**: [http://localhost:8080/docs/openapi.yaml](http://localhost:8080/docs/openapi.yaml) (или `docs/openapi.yaml` в репозитории)
- **Спецификация OpenAPI 3.0 (JSON)**: [http://localhost:8080/docs/openapi.json](http://localhost:8080/docs/openapi.json) (или `docs/openapi.json` в репозитории)

---

### Приём логов

```bash
# Один лог
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"service":"auth-service","level":"error","message":"auth failed","trace_id":"abc"}'

# Батч логов (POST /api/v1/logs/batch)
curl -X POST http://localhost:8080/api/v1/logs/batch \
  -H "Content-Type: application/json" \
  -d '{"logs":[
    {"service":"order-service","level":"info","message":"order created"},
    {"service":"order-service","level":"warn","message":"processing slow"}
  ]}'
```

**Контракт и семантика batch-приёма (`POST /api/v1/logs/batch`):**

| HTTP Статус | Условие | Описание |
|---|---|---|
| `202 Accepted` | `accepted > 0` | Полный или частичный успех. Записи приняты в очередь. При частичном успехе поле `failed` содержит список отклонённых элементов с признаком `retryable`. |
| `422 Unprocessable Entity` | `accepted == 0 && errors > 0` | Полный отказ из-за ошибок валидации данных. Клиенту **не следует** повторять отправку без исправления записей (`retryable: false`). |
| `503 Service Unavailable` | `accepted == 0 && dropped > 0` | Полный отказ из-за переполнения внутренней очереди сервера. Клиент **может** повторить отправку батча позже (`retryable: true`). |
| `400 Bad Request` | — | Некорректный JSON или пустой массив `logs`. |

**Пример ответа при частичном успехе (`202 Accepted`):**
```json
{
  "accepted": 2,
  "dropped": 1,
  "errors": 1,
  "failed": [
    {
      "index": 2,
      "status": "validation_error",
      "error": "валидация: поле service обязательно",
      "retryable": false
    },
    {
      "index": 3,
      "status": "queue_full",
      "error": "очередь переполнена, повторите попытку позже",
      "retryable": true
    }
  ]
}
```

### Просмотр логов

```bash
# Последние 100 логов
curl http://localhost:8080/api/v1/logs

# Фильтры: сервис + уровень + поиск + лимит
curl "http://localhost:8080/api/v1/logs?service=auth-service&level=error&search=failed&limit=50"
```

### Статистика и маршруты

```bash
# Статистика: принято/отброшено/ошибок, by_service, by_level, uptime
curl http://localhost:8080/api/v1/stats

# Активные правила маршрутизации
curl http://localhost:8080/api/v1/routes

# Health checks
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
```

### Пример ответа `/api/v1/stats`

```json
{
  "uptime": "5h 23m 15s",
  "accepted": 1240,
  "dropped": 3,
  "errors": 1,
  "total_stored": 1240,
  "in_memory": 1240,
  "by_service": {
    "auth-service": 410,
    "order-service": 430,
    "payment-service": 400
  },
  "by_level": {
    "debug": 400,
    "info": 600,
    "warn": 160,
    "error": 80
  }
}
```

---

## Интеграция с существующим Go-проектом

### Вариант A — просто HTTP (любой язык)

```bash
curl -X POST http://aggregator:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"service":"my-app","level":"error","message":"something broke"}'
```

### Вариант B — кастомный `slog.Handler` для Go-проектов

Пакет `pkg/aggregatorslog` реализует интерфейс `slog.Handler`.  
Подключается **одной строкой** — существующий код менять не нужно:

```go
import "github.com/stepankolchin/log-aggregator/pkg/aggregatorslog"

func main() {
    // Заменяем стандартный handler на наш:
    handler := aggregatorslog.New("http://aggregator:8080", "my-service")
    slog.SetDefault(slog.New(handler))

    // Все существующие вызовы slog теперь идут в aggregator:
    slog.Info("server started", "port", 8080)
    slog.Warn("slow query", "duration_ms", 1500)
    slog.Error("db connection failed", "err", err)
}
```

**Как работает `slog.Handler`:**
```
slog.Info("msg", "key", val)
       │
       ▼
  slog.Record  { Time, Level, Message, Attrs }
       │
       ▼
  Handler.Handle(ctx, record)
       │  конвертирует Record → LogEntry
       ▼
  POST /api/v1/logs  →  aggregator
```

Дополнительные опции:
```go
// Отправлять все уровни включая debug:
handler := aggregatorslog.New(url, "svc").WithMinLevel(slog.LevelDebug)

// По умолчанию при сбоях сети/503/422 логи автоматически дублируются в os.Stderr (fallback).
// Можно задать свой fallback (например, в файл) или отключить его:
handler := aggregatorslog.New(url, "svc").WithFallback(slog.NewJSONHandler(file, nil))
// или: handler := aggregatorslog.New(url, "svc").WithoutFallback()

// Отдельный обработчик ошибок доставки (для Sentry/алертов):
handler := aggregatorslog.New(url, "svc").
    WithErrorHandler(func(err error, r slog.Record) {
        fmt.Fprintf(os.Stderr, "ошибка доставки в aggregator: %v\n", err)
    })

// Счётчики успешных отправок, ошибок и реальных потерь:
// - sent: успешно ушло в aggregator
// - errors: сбои обращения к aggregator (сохранены через fallback)
// - dropped: потеряно насовсем (если fallback был отключен или упал)
sent, errors, dropped := handler.Stats()

// Добавить постоянные поля к каждому логу:
logger := slog.New(handler.WithAttrs([]slog.Attr{
    slog.String("component", "payment"),
    slog.String("version", "1.2.0"),
}))
logger.Info("started") // → fields: {"component":"payment","version":"1.2.0"}
```

## *Покрытие тестами*

### 1. Валидация и приём логов (`internal/ingest`)

| Тест | Описание |
| :--- | :--- |
| `TestNormalizeLevel` | Проверка приведения уровней логирования к каноническому виду. |
| `TestValidate` | Табличный тест валидации входящей записи: обязательность полей `service` и `message`, проверка корректности уровня, простановка серверного `server_timestamp`. |

---

### 2. Конфигурация (`internal/config`)

| Тест | Описание |
| :--- | :--- |
| `TestConfig_Validate` | Комплексная валидация `config.yaml`: проверка диапазонов портов, таймаутов, параметров воркер-пула, лимитов памяти хранилища, валидности regex-паттернов маршрутизации и доступности указанных синков. |
| `TestConfig_LoadInvalidYAML` | Проверка обработки синтаксических ошибок в YAML-файле конфигурации. |

---

### 3. Маршрутизация логов (`internal/router`)

| Тест | Описание |
| :--- | :--- |
| `TestRouter_FanOut` | Отправка одного лога сразу в несколько синков при совпадении правил маршрутизации. |
| `TestRouter_Default` | Срабатывание правила по умолчанию, если ни одно кастомное правило не подошло. |
| `TestRouter_DefaultNotFiredWhenMatched` | Гарантия того, что дефолтное правило не вызывается, если сработало хотя бы одно специфичное правило. |
| `TestRouter_LevelsFilter` | Фильтрация сообщений по списку разрешенных уровней. |
| `TestRouter_MessageRegex` | Фильтрация логов по регулярному выражению в тексте `message`. |
| `TestRouter_Discard` | Полное отбрасывание лога специальным правилом `discard`. |
| `TestRouter_InvalidRegex` | Обработка ошибок инициализации при некорректном синтаксисе регулярного выражения. |
| `TestRouter_UnregisteredSink` | Ошибка инициализации роутера при ссылке на незарегистрированный синк. |

---

### 4. Воркер-пул (`internal/worker`)

| Тест | Описание |
| :--- | :--- |
| `TestPool_DrainsQueueOnShutdown` | Проверка корректного дрейна очереди: при закрытии канала (`close(queue)`) воркеры вычитывают все накопленные в буфере сообщения без потерь. |
| `TestPool_ContextCancelStopsWorkers` | Корректная экстренная остановка горутин воркеров через отмену контекста (`context.Cancel`). |

---

### 5. Синки вывода (`internal/sink`)

| Тест | Описание |
| :--- | :--- |
| `TestStdout_Write` | Форматирование вывода в консоль (режимы `json` и человекочитаемый `text`). |
| `TestFile_Patterns` | Запись логов на диск с поддержкой структуры каталогов (`flat`, `by-service`, `by-date`) и автоматической ротацией файлов по суткам. |
| `TestWebhook_RetriesAndHeaders` | Отправка логов на внешний HTTP вебхук, передача кастомных заголовков и работа механизма повторных попыток при 5xx ответах. |
| `TestWebhook_NonRetryable4xx` | Отсутствие бессмысленных повторных попыток при клиентских ошибках 4xx от вебхука. |
| `TestWebhook_ContextCancel` | Корректное прерывание HTTP-запроса вебхука при отмене контекста. |

---

### 6. Хранилище и архив (`internal/storage`)

| Тест | Описание |
| :--- | :--- |
| `TestStorage_NewValidation` | Проверка граничных значений емкости кольцевого буфера памяти. |
| `TestStorage_StoreAndEviction` | Сохранение логов в память и корректное вытеснение старых записей по принципу очереди при достижении лимита. |
| `TestStorage_QueryMemorySafety` | Потокобезопасность выборки и возврат глубоких копий срезов из памяти. |
| `TestStorage_QueryFilters` | Фильтрация логов в оперативной памяти по сервису, уровню, подстроке и диапазону времени. |
| `TestStorage_LoadFromFile_Success` | Восстановление состояния хранилища из файла при перезапуске агрегатора. |
| `TestStorage_LoadFromFile_NonExistentDir` | Устойчивость к отсутствию директории с логами при первичном старте. |
| `TestStorage_LoadFromFile_OnlyFileSinkIsRestored` | Гарантия загрузки данных только для активного файлового синка. |
| `TestQueryArchive_PaginationAndFilters` | Постраничная выборка из архивных файлов на диске и фильтрация по датам. |

---

### 7. HTTP API, Web UI и Graceful Shutdown (`internal/api`)

| Тест | Описание |
| :--- | :--- |
| `TestHandleSingle_ValidLog` | Успешный приём одиночного лога (`POST /api/v1/logs` -> `202 Accepted`). |
| `TestHandleSingle_InvalidJSON` | Обработка синтаксически некорректного JSON (`400 Bad Request`). |
| `TestHandleSingle_MissingService` | Отклонение записи без обязательного поля `service` (`422 Unprocessable Entity`). |
| `TestHandleSingle_InvalidLevel` | Отклонение записи с неизвестным уровнем логирования (`422 Unprocessable Entity`). |
| `TestHandleBatch` | Пакетный приём логов (`POST /api/v1/logs/batch` -> `202 Accepted`). |
| `TestHandleBatch_AllValidationErrors` | Отклонение батча, где все записи содержат ошибки валидации (`422 Unprocessable Entity`). |
| `TestHandleBatch_QueueFull` | Поведение при переполнении входной очереди (`503 Service Unavailable` с флагом `retryable: true` в отчете). |
| `TestHandleBatch_Empty` | Отклонение пустого массива логов (`400 Bad Request`). |
| `TestHandleGetLogs` | Выборка логов через API (`GET /api/v1/logs`) с пагинацией и сортировкой от новых к старым. |
| `TestHandleGetLogs_FilterByService` | Фильтрация API-выборки по имени сервиса. |
| `TestHandleGetLogs_LimitValidation` | Валидация параметров `limit` и `offset` в запросе к API. |
| `TestHandleStats` | Получение агрегированной статистики (`GET /api/v1/stats`). |
| `TestFormatUptime` | Корректное форматирование времени непрерывной работы агрегатора. |
| `TestHandleRoutes` | Получение активной таблицы маршрутизации (`GET /api/v1/routes`). |
| `TestHandleHealthz` | Проверка liveness-пробы (`/healthz` -> `200 OK`). |
| `TestHandleReadyz` | Проверка readiness-пробы (`/readyz`) с переходом в `503` при остановке сервера. |
| `TestHandleUI` / `TestHandleCSS` / `TestHandleJS` | Раздача статических файлов веб-дашборда (`index.html`, `style.css`, `app.js`). |
| `TestHandleNotFound` | Обработка обращения к несуществующим маршрутам (`404 Not Found`). |
| `TestSwaggerUIAndOpenAPI` | Раздача интерактивной документации Swagger UI и спецификаций `openapi.yaml` / `openapi.json`. |
| `TestServer_Start_PortConflict` | Синхронный возврат ошибки при попытке занять уже используемый порт. |
| `TestServer_Start_SuccessAndShutdown` | Базовый жизненный цикл запуска и остановки HTTP-сервера на реальном сокете. |
| `TestServer_GracefulShutdown_UnderLoad` | Корректная остановка агрегатора под непрерывной параллельной нагрузкой от 10 клиентов с гарантией 100% сохранения подтвержденных логов. |

---

### 8. Клиентская библиотека (`pkg/aggregatorslog`)

| Тест | Описание |
| :--- | :--- |
| `TestHandler_Success` | Отправка структурированных логов из Go-приложений через стандартный `log/slog.Handler`. |
| `TestHandler_ErrorStatusCodes_SavedToFallback` | Аварийное сохранение в локальный `fallback` при недоступности агрегатора или ответах 422/503/500. |
| `TestHandler_WithoutFallback_Dropped` | Корректный подсчет метрики `dropped` при сбое сети и отсутствии fallback-хендлера. |
| `TestHandler_FallbackFails_Dropped` | Подсчет ошибок, если основной агрегатор и fallback одновременно недоступны. |
| `TestHandler_NetworkError` | Устойчивость к сетевым ошибкам соединения. |
| `TestHandler_SlogLoggerIntegration` | Полная совместимость с `slog.New(handler)` и вызовами `slog.Info`, `slog.Error` и др. |
| `TestHandler_CloningSharesStats` | Разделение атомарных счетчиков статистики между клонированными хендлерами (`WithAttrs`). |
| `TestHandler_Enabled` | Проверка фильтрации по минимальному уровню логирования (`Level.Level()`). |
| `TestHandler_WithGroup_PrefixesFields` | Префиксирование полей при группировке (`logger.WithGroup("http")`). |
| `TestHandler_NestedGroupsAndKindGroup` | Корректная обработка глубоко вложенных групп и составных атрибутов. |

```bash
go test ./...
```

<img width="563" height="219" alt="image" src="https://github.com/user-attachments/assets/a42f0e09-8223-4e66-9220-5e32282f159e" />

```bash
go test -race ./...
```

<img width="562" height="223" alt="image" src="https://github.com/user-attachments/assets/0ae7b801-4c07-499d-a3a9-265f53953328" />

***Demo***

![](log-aggregator/media/log-aggregator_demo.mp4)



#<video width="80%" src="https://github.com/stepankolchin/log-aggregator/media/log-aggregator_demo.mp4" controls></video>

