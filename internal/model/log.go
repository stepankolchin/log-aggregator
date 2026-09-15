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
	// Основные поля
	Timestamp       time.Time `json:"timestamp,omitempty"`        // время события на клиенте (опционально)
	ServerTimestamp time.Time `json:"server_timestamp,omitempty"` // время приёма лога агрегатором (всегда заполняется сервером)
	Service         string    `json:"service"`
	Level           string    `json:"level"`
	Message         string    `json:"message"`

	// Опциональные поля обогащения
	Host    string            `json:"host,omitempty"`
	TraceID string            `json:"trace_id,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// EffectiveTime возвращает время события: клиентский Timestamp (если задан) либо ServerTimestamp.
func (e LogEntry) EffectiveTime() time.Time {
	if !e.Timestamp.IsZero() {
		return e.Timestamp
	}
	return e.ServerTimestamp
}

// BatchRequest — тело запроса на батч-приём логов.
type BatchRequest struct {
	Logs []LogEntry `json:"logs"`
}
