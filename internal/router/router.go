package router

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stepankolchin/log-aggregator/internal/config"
	"github.com/stepankolchin/log-aggregator/internal/model"
)

// Sink — интерфейс назначения лога: stdout, file, webhook, discard.
type Sink interface {
	Name() string
	Write(ctx context.Context, entry model.LogEntry) error
}

// Router реализует интерфейс worker.Processor.
// Выполняет fan-out: лог направляется во все совпавшие правила.
type Router struct {
	rules []rule
	sinks map[string]Sink // имя → реализация
}

// New создаёт Router из конфига и списка синков.
func New(routes []config.RouteConfig, sinks []Sink) (*Router, error) {
	rules, err := buildRules(routes)
	if err != nil {
		return nil, fmt.Errorf("компиляция правил маршрутизации: %w", err)
	}

	sinkMap := make(map[string]Sink, len(sinks))
	for _, s := range sinks {
		sinkMap[s.Name()] = s
	}

	return &Router{rules: rules, sinks: sinkMap}, nil
}

// Process реализует worker.Processor — вызывается воркером для каждого лога.
// Fan-out: ищем ВСЕ совпавшие правила. Если ни одно не совпало — применяем default.
func (r *Router) Process(ctx context.Context, entry model.LogEntry) {
	matched := false

	for _, rule := range r.rules {
		// "default" — специальное имя; обрабатываем его отдельно в конце
		if rule.name == "default" {
			continue
		}
		if rule.matches(entry) {
			matched = true
			r.dispatch(ctx, entry, rule)
		}
	}

	// Если ни одно правило не сработало — применяем default
	if !matched {
		r.applyDefault(ctx, entry)
	}
}

// dispatch отправляет лог во все синки, указанные в правиле.
func (r *Router) dispatch(ctx context.Context, entry model.LogEntry, rl rule) {
	for _, sinkName := range rl.sinks {
		if sinkName == "discard" {
			// discard — явно отбросить лог, без записи
			continue
		}
		sink, ok := r.sinks[sinkName]
		if !ok {
			slog.Warn("неизвестный sink в правиле", "rule", rl.name, "sink", sinkName)
			continue
		}
		if err := sink.Write(ctx, entry); err != nil {
			slog.Error("ошибка записи в sink", "sink", sinkName, "rule", rl.name, "err", err)
		}
	}
}

// applyDefault применяет правило с именем "default", если оно задано в конфиге.
func (r *Router) applyDefault(ctx context.Context, entry model.LogEntry) {
	for _, rl := range r.rules {
		if rl.name == "default" {
			r.dispatch(ctx, entry, rl)
			return
		}
	}
}

// Routes возвращает список правил для отображения через API.
func (r *Router) Routes() []map[string]any {
	result := make([]map[string]any, 0, len(r.rules))
	for _, rl := range r.rules {
		result = append(result, map[string]any{
			"name":  rl.name,
			"sinks": rl.sinks,
		})
	}
	return result
}
