package model

import "time"

// Уровни логирования
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// ValidLevels — допустимые уровни и их числовые приоритеты (для сравнения).
var ValidLevels = map[string]int{
	LevelDebug: 0,
	LevelInfo:  1,
	LevelWarn:  2,
	LevelError: 3,
}

// LogEntry — одна запись лога, поступающая от микросервиса.
type LogEntry struct {
	// Обязательные поля
	Timestamp time.Time `json:"timestamp"`
	Service   string    `json:"service"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`

	// Опциональные поля обогащения
	Host    string            `json:"host,omitempty"`
	TraceID string            `json:"trace_id,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// BatchRequest — тело запроса на батч-приём логов.
type BatchRequest struct {
	Logs []LogEntry `json:"logs"`
}
