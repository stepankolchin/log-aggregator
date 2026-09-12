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
  "timestamp": "2026-09-12T17:00:00Z",
  "service":   "auth-service",
  "level":     "error",
  "message":   "authentication failed",
  "host":      "auth-1",
  "trace_id":  "abc-123",
  "fields":    { "user_id": "42", "ip": "1.2.3.4" }
}
```

**Обязательные поля:** `timestamp`, `service`, `level`, `message`  
**Допустимые уровни:** `debug`, `info`, `warn`, `error`  
**Нормализация уровней:** `warning`→`warn`, `fatal`/`critical`/`panic`→`error`, `trace`/`verbose`→`debug`

---

## Конфигурация

Файл: `configs/config.yaml`

```yaml
server:
  port: 8080
  read_timeout: 10s
  write_timeout: 10s

worker:
  pool_size: 4        # число параллельных воркеров
  buffer_size: 1024   # размер внутреннего канала

storage:
  memory_limit: 5000  # последних N логов в памяти для API

sinks:
  stdout:
    enabled: true
  file:
    enabled: true
    dir: "./logs"     # файлы вида 2026-09-12.jsonl
  webhook:
    enabled: false
    url: "http://localhost:9000/webhook"
    timeout: 5s

routes:
  - name: "auth-errors"
    match:
      service: "auth-service"
      level: "error"
    sinks: [file, webhook]

  - name: "warn-and-above"
    match:
      levels: [warn, error]
    sinks: [stdout]

  - name: "default"
    match: {}           # срабатывает, если ни одно правило не совпало
    sinks: [stdout]
```

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

### Приём логов

```bash
# Один лог
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"service":"auth-service","level":"error","message":"auth failed","trace_id":"abc"}'

# Батч логов
curl -X POST http://localhost:8080/api/v1/logs/batch \
  -H "Content-Type: application/json" \
  -d '{"logs":[
    {"service":"order-service","level":"info","message":"order created"},
    {"service":"order-service","level":"warn","message":"processing slow"}
  ]}'
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
  "uptime": "5m30s",
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

// Добавить постоянные поля к каждому логу:
logger := slog.New(handler.WithAttrs([]slog.Attr{
    slog.String("component", "payment"),
    slog.String("version", "1.2.0"),
}))
logger.Info("started") // → fields: {"component":"payment","version":"1.2.0"}
```
