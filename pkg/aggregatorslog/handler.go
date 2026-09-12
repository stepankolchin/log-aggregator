// Пакет aggregatorslog реализует slog.Handler для отправки логов в log-aggregator.
package aggregatorslog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
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

// Handler реализует интерфейс slog.Handler.
// Каждый вызов slog.Info/Warn/Error/Debug превращается в HTTP POST к aggregator.
type Handler struct {
	endpoint string      // URL aggregator + "/api/v1/logs"
	service  string      // имя текущего микросервиса
	host     string      // hostname машины (заполняется автоматически)
	minLevel slog.Level  // минимальный уровень для отправки
	attrs    []slog.Attr // постоянные атрибуты, добавленные через WithAttrs
	client   *http.Client
}

// New создаёт Handler для заданного aggregator URL и имени сервиса.
// По умолчанию отправляет логи уровня Info и выше.
func New(aggregatorURL, serviceName string) *Handler {
	hostname, _ := os.Hostname()
	return &Handler{
		endpoint: aggregatorURL + "/api/v1/logs",
		service:  serviceName,
		host:     hostname,
		minLevel: slog.LevelInfo,
		client:   &http.Client{Timeout: 3 * time.Second},
	}
}

// WithMinLevel возвращает копию Handler с изменённым минимальным уровнем.
func (h *Handler) WithMinLevel(level slog.Level) *Handler {
	clone := *h
	clone.minLevel = level
	return &clone
}

// Enabled сообщает пакету slog, нужно ли обрабатывать данный уровень.
// Если false — Handle() вызван не будет, что экономит ресурсы.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minLevel
}

// Handle вызывается пакетом slog для каждой записи лога.
// Конвертирует slog.Record в logPayload и отправляет POST в aggregator.
// Ошибка отправки не паникует — агрегатор может быть временно недоступен.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	// Собираем все атрибуты записи в map[string]string
	fields := make(map[string]string, r.NumAttrs()+len(h.attrs))

	// Сначала постоянные атрибуты (из WithAttrs)
	for _, a := range h.attrs {
		fields[a.Key] = a.Value.String()
	}

	// Затем атрибуты текущего вызова (slog.Info("msg", "key", val))
	r.Attrs(func(a slog.Attr) bool {
		fields[a.Key] = a.Value.String()
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
		return nil
	}

	resp, err := h.client.Post(h.endpoint, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil
	}
	resp.Body.Close()
	return nil
}

// WithAttrs возвращает новый Handler с добавленными постоянными атрибутами.
// Эти атрибуты будут присутствовать во всех последующих записях.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(clone.attrs, h.attrs)
	copy(clone.attrs[len(h.attrs):], attrs)
	return &clone
}

// WithGroup возвращает новый Handler с группировкой атрибутов под именем группы.
// В нашей реализации группа добавляется как суффикс к имени сервиса.
func (h *Handler) WithGroup(name string) slog.Handler {
	clone := *h
	if name != "" {
		clone.service = h.service + "." + name
	}
	return &clone
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
