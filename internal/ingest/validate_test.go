package ingest

import (
	"testing"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// TestNormalizeLevel проверяет что все синонимы уровней приводятся к стандарту.
func TestNormalizeLevel(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// debug и его синонимы
		{"debug", model.LevelDebug},
		{"DEBUG", model.LevelDebug},
		{"trace", model.LevelDebug},
		{"verbose", model.LevelDebug},
		// info
		{"info", model.LevelInfo},
		{"INFO", model.LevelInfo},
		{"information", model.LevelInfo},
		// warn
		{"warn", model.LevelWarn},
		{"warning", model.LevelWarn},
		{"WARNING", model.LevelWarn},
		{"WARN", model.LevelWarn},
		// error и его синонимы
		{"error", model.LevelError},
		{"ERROR", model.LevelError},
		{"err", model.LevelError},
		{"fatal", model.LevelError},
		{"FATAL", model.LevelError},
		{"critical", model.LevelError},
		{"panic", model.LevelError},
	}

	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := normalizeLevel(c.input)
			if got != c.want {
				t.Errorf("normalizeLevel(%q) = %q, хотели %q", c.input, got, c.want)
			}
		})
	}
}

// TestValidate проверяет валидацию полей LogEntry.
func TestValidate(t *testing.T) {
	// Вспомогательная функция: создаёт корректную запись-шаблон.
	validEntry := func() model.LogEntry {
		return model.LogEntry{
			Timestamp: time.Now(),
			Service:   "test-service",
			Level:     "info",
			Message:   "test message",
		}
	}

	t.Run("валидная запись проходит без ошибок", func(t *testing.T) {
		e := validEntry()
		if err := Validate(&e); err != nil {
			t.Errorf("неожиданная ошибка: %v", err)
		}
	})

	t.Run("отсутствует service → ошибка", func(t *testing.T) {
		e := validEntry()
		e.Service = ""
		if err := Validate(&e); err == nil {
			t.Error("ожидалась ошибка при пустом service")
		}
	})

	t.Run("отсутствует message → ошибка", func(t *testing.T) {
		e := validEntry()
		e.Message = ""
		if err := Validate(&e); err == nil {
			t.Error("ожидалась ошибка при пустом message")
		}
	})

	t.Run("неизвестный уровень → ошибка", func(t *testing.T) {
		e := validEntry()
		e.Level = "unknown-level"
		if err := Validate(&e); err == nil {
			t.Error("ожидалась ошибка при неизвестном уровне")
		}
	})

	t.Run("нулевой timestamp клиента сохраняется, но ServerTimestamp заполняется", func(t *testing.T) {
		e := validEntry()
		e.Timestamp = time.Time{} // нулевое время
		before := time.Now().UTC()
		if err := Validate(&e); err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
		if !e.Timestamp.IsZero() {
			t.Error("клиентский timestamp должен был остаться нулевым")
		}
		if e.ServerTimestamp.IsZero() || e.ServerTimestamp.Before(before.Add(-time.Second)) {
			t.Error("server_timestamp не был корректно установлен")
		}
		if e.EffectiveTime().IsZero() {
			t.Error("EffectiveTime() не должен быть нулевым")
		}
	})

	t.Run("уровень нормализуется: WARNING → warn", func(t *testing.T) {
		e := validEntry()
		e.Level = "WARNING"
		if err := Validate(&e); err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
		if e.Level != model.LevelWarn {
			t.Errorf("уровень не нормализован: получили %q, хотели %q", e.Level, model.LevelWarn)
		}
	})

	t.Run("уровень нормализуется: FATAL → error", func(t *testing.T) {
		e := validEntry()
		e.Level = "FATAL"
		if err := Validate(&e); err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
		if e.Level != model.LevelError {
			t.Errorf("уровень не нормализован: получили %q, хотели %q", e.Level, model.LevelError)
		}
	})
}
