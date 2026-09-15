// Пакет aggregatorslog реализует slog.Handler для отправки логов в log-aggregator.
package aggregatorslog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// logPayload — тело POST-запроса к aggregator (совпадает с model.LogEntry).
type logPayload struct {
	Timestamp time.Time         `json:"timestamp"`
	Service   string            `json:"service"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Host      string            `json:"host,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// ErrorHandler — функция обратного вызова при ошибке доставки лога в aggregator.
type ErrorHandler func(err error, r slog.Record)

// Stats хранит счётчики отправленных и потерянных записей лога.
type Stats struct {
	Sent    atomic.Int64 // успешно доставлено в aggregator (2xx)
	Errors  atomic.Int64 // сбои отправки в aggregator (сеть, 4xx, 5xx, marshal)
	Dropped atomic.Int64 // безвозвратно потеряно (сбой агрегатора И отсутствие/ошибка fallback)
}

// Handler реализует интерфейс slog.Handler.
// Каждый вызов slog.Info/Warn/Error/Debug превращается в HTTP POST к aggregator.
type Handler struct {
	endpoint string       // URL aggregator + "/api/v1/logs"
	service  string       // имя текущего микросервиса
	host     string       // hostname машины (заполняется автоматически)
	minLevel slog.Level   // минимальный уровень для отправки
	attrs    []slog.Attr  // постоянные атрибуты, добавленные через WithAttrs
	groups   []string     // активные группы, добавленные через WithGroup
	client   *http.Client // HTTP-клиент для отправки логов
	fallback slog.Handler // резервный handler на случай сбоя отправки (по умолчанию os.Stderr)
	onError  ErrorHandler // обработчик ошибок доставки
	stats    *Stats       // общие счётчики отправки и потерь
}

// New создаёт Handler для заданного aggregator URL и имени сервиса.
// По умолчанию:
// - отправляет логи уровня Info и выше;
// - на случай сбоев настроен резервный вывод в os.Stderr (slog.NewTextHandler),
//   чтобы логи не терялись даже при падении агрегатора.
func New(aggregatorURL, serviceName string) *Handler {
	hostname, _ := os.Hostname()
	return &Handler{
		endpoint: aggregatorURL + "/api/v1/logs",
		service:  serviceName,
		host:     hostname,
		minLevel: slog.LevelInfo,
		client:   &http.Client{Timeout: 3 * time.Second},
		fallback: slog.NewTextHandler(os.Stderr, nil),
		stats:    &Stats{},
	}
}

// WithMinLevel возвращает копию Handler с изменённым минимальным уровнем.
func (h *Handler) WithMinLevel(level slog.Level) *Handler {
	clone := *h
	clone.minLevel = level
	return &clone
}

// WithFallback задаёт кастомный резервный slog.Handler (например, вывод в файл или stdout).
// Вызывается при любых ошибках отправки в aggregator (сеть, не-2xx статус, маршалинг).
func (h *Handler) WithFallback(fallback slog.Handler) *Handler {
	clone := *h
	clone.fallback = fallback
	return &clone
}

// WithoutFallback отключает резервный вывод в os.Stderr.
// В этом случае при сбое отправки в агрегатор лог будет помечен как Dropped.
func (h *Handler) WithoutFallback() *Handler {
	clone := *h
	clone.fallback = nil
	return &clone
}

// WithErrorHandler задаёт функцию обратного вызова при ошибках отправки.
func (h *Handler) WithErrorHandler(fn ErrorHandler) *Handler {
	clone := *h
	clone.onError = fn
	return &clone
}

// WithClient переопределяет HTTP-клиент (например, для настройки таймаутов, пулов или тестов).
func (h *Handler) WithClient(client *http.Client) *Handler {
	clone := *h
	if client != nil {
		clone.client = client
	} else {
		clone.client = &http.Client{Timeout: 3 * time.Second}
	}
	return &clone
}

// Stats возвращает текущие значения счётчиков:
// - sent: успешно отправлено в aggregator (HTTP 2xx);
// - errors: возникли ошибки отправки в aggregator (сеть, 422, 503 и т.д.);
// - dropped: лог потерян безвозвратно (сбой агрегатора И отсутствие/ошибка резервного вывода).
func (h *Handler) Stats() (sent, errors, dropped int64) {
	if h.stats == nil {
		return 0, 0, 0
	}
	return h.stats.Sent.Load(), h.stats.Errors.Load(), h.stats.Dropped.Load()
}

// ResetStats сбрасывает счётчики статистики.
func (h *Handler) ResetStats() {
	if h.stats != nil {
		h.stats.Sent.Store(0)
		h.stats.Errors.Store(0)
		h.stats.Dropped.Store(0)
	}
}

// Enabled сообщает пакету slog, нужно ли обрабатывать данный уровень.
// Если false — Handle() вызван не будет, что экономит ресурсы.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minLevel
}

// Handle вызывается пакетом slog для каждой записи лога.
// Конвертирует slog.Record в logPayload и отправляет POST в aggregator.
// При возникновении ошибки:
// 1. Инкрементируется счётчик errors.
// 2. Если задан onError, вызывается обработчик ошибки.
// 3. Если задан fallback, запись передаётся в резервный вывод.
// 4. Если fallback отсутствует или вернул ошибку, инкрементируется счётчик dropped.
// 5. Ошибка возвращается вызывающему коду.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	// Собираем все атрибуты записи в map[string]string
	fields := make(map[string]string, r.NumAttrs()+len(h.attrs))

	// Сначала постоянные атрибуты (из WithAttrs, они уже префиксированы)
	for _, a := range h.attrs {
		fields[a.Key] = a.Value.String()
	}

	// Затем атрибуты текущей записи с учётом активного стека групп
	prefix := strings.Join(h.groups, ".")
	r.Attrs(func(a slog.Attr) bool {
		flattenAttrs(prefix, a, fields)
		return true
	})

	p := logPayload{
		Timestamp: r.Time.UTC(),
		Service:   h.service,
		Level:     slogLevelToString(r.Level),
		Message:   r.Message,
		Host:      h.host,
		Fields:    fields,
	}

	data, err := json.Marshal(p)
	if err != nil {
		return h.handleFailure(ctx, r, fmt.Errorf("aggregatorslog: marshal log payload: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint, bytes.NewReader(data))
	if err != nil {
		return h.handleFailure(ctx, r, fmt.Errorf("aggregatorslog: create request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return h.handleFailure(ctx, r, fmt.Errorf("aggregatorslog: send log to aggregator: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		errMsg := strings.TrimSpace(string(body))
		var statusErr error
		if errMsg != "" {
			statusErr = fmt.Errorf("aggregatorslog: aggregator returned status %d: %s", resp.StatusCode, errMsg)
		} else {
			statusErr = fmt.Errorf("aggregatorslog: aggregator returned status %d", resp.StatusCode)
		}
		return h.handleFailure(ctx, r, statusErr)
	}

	// Считываем остаток тела для поддержки reuse соединений
	_, _ = io.Copy(io.Discard, resp.Body)

	if h.stats != nil {
		h.stats.Sent.Add(1)
	}
	return nil
}

// handleFailure централизованно обновляет счётчики, оповещает onError и fallback handler.
func (h *Handler) handleFailure(ctx context.Context, r slog.Record, err error) error {
	if h.stats != nil {
		h.stats.Errors.Add(1)
	}

	if h.onError != nil {
		h.onError(err, r)
	}

	var fallbackErr error
	if h.fallback != nil {
		fallbackErr = h.fallback.Handle(ctx, r)
	}

	// Если резервного вывода нет или он завершился с ошибкой — лог потерян безвозвратно
	if h.fallback == nil || fallbackErr != nil {
		if h.stats != nil {
			h.stats.Dropped.Add(1)
		}
	}

	return err
}

// WithAttrs возвращает новый Handler с добавленными постоянными атрибутами.
// Атрибуты квалифицируются текущим префиксом групп.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := *h
	prefix := strings.Join(h.groups, ".")

	attrMap := make(map[string]string, len(attrs))
	for _, a := range attrs {
		flattenAttrs(prefix, a, attrMap)
	}

	newAttrs := make([]slog.Attr, 0, len(attrMap))
	for k, v := range attrMap {
		newAttrs = append(newAttrs, slog.String(k, v))
	}

	clone.attrs = make([]slog.Attr, len(h.attrs)+len(newAttrs))
	copy(clone.attrs, h.attrs)
	copy(clone.attrs[len(h.attrs):], newAttrs)

	if clone.fallback != nil {
		clone.fallback = clone.fallback.WithAttrs(attrs)
	}
	return &clone
}

// WithGroup возвращает новый Handler с группировкой атрибутов под именем группы (namespace префикс).
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = make([]string, len(h.groups)+1)
	copy(clone.groups, h.groups)
	clone.groups[len(h.groups)] = name

	if clone.fallback != nil {
		clone.fallback = clone.fallback.WithGroup(name)
	}
	return &clone
}

// flattenAttrs рекурсивно извлекает и форматирует атрибуты, учитывая префикс групп и вложенные slog.KindGroup.
func flattenAttrs(prefix string, a slog.Attr, out map[string]string) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		groupAttrs := a.Value.Group()
		if len(groupAttrs) == 0 {
			return
		}
		newPrefix := prefix
		if a.Key != "" {
			if prefix != "" {
				newPrefix = prefix + "." + a.Key
			} else {
				newPrefix = a.Key
			}
		}
		for _, ga := range groupAttrs {
			flattenAttrs(newPrefix, ga, out)
		}
		return
	}

	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	out[key] = a.Value.String()
}

// slogLevelToString конвертирует slog.Level в строку, понятную aggregator.
func slogLevelToString(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}
