package ingest

import (
	"fmt"
	"strings"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// Validate проверяет обязательные поля и нормализует запись лога.
// Мутирует переданный указатель — это сделано намеренно для экономии аллокаций.
func Validate(e *model.LogEntry) error {
	// Нормализуем уровень до одного из четырёх стандартных
	e.Level = normalizeLevel(e.Level)

	if e.Service == "" {
		return fmt.Errorf("отсутствует поле service")
	}
	if e.Message == "" {
		return fmt.Errorf("отсутствует поле message")
	}
	if _, ok := model.ValidLevels[e.Level]; !ok {
		return fmt.Errorf("неверный уровень %q: ожидается debug, info, warn или error", e.Level)
	}

	// Если клиент не прислал timestamp — ставим текущее время на сервере
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}

	return nil
}

// normalizeLevel приводит произвольные строки к одному из стандартных уровней.
// Это позволяет принимать логи от сервисов с разными соглашениями об именовании.
func normalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace", "verbose", "debug":
		return model.LevelDebug
	case "info", "information":
		return model.LevelInfo
	case "warn", "warning":
		return model.LevelWarn
	case "err", "error", "fatal", "critical", "panic":
		return model.LevelError
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
}
