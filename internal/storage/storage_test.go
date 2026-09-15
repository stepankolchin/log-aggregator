package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
	"github.com/stepankolchin/log-aggregator/internal/sink"
	"github.com/stepankolchin/log-aggregator/internal/storage"
)

func TestStorage_NewValidation(t *testing.T) {
	tests := []struct {
		name        string
		input       int
		expectError bool
	}{
		{"отрицательный лимит → ошибка", -5, true},
		{"нулевой лимит → ошибка", 0, true},
		{"лимит меньше минимума (1) → ошибка", 1, true},
		{"лимит меньше минимума (9) → ошибка", 9, true},
		{"минимальный лимит (10) → успех", 10, false},
		{"корректный лимит (500) → успех", 500, false},
		{"максимальный лимит (100 000) → успех", 100_000, false},
		{"превышение максимума (100 001) → ошибка", 100_001, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := storage.New(tt.input)
			if tt.expectError {
				if err == nil {
					t.Errorf("New(%d): ожидалась ошибка валидации, получен nil", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("New(%d): неожиданная ошибка: %v", tt.input, err)
				}
				if s.Limit() != tt.input {
					t.Errorf("New(%d).Limit() = %d, ожидалось %d", tt.input, s.Limit(), tt.input)
				}
			}
		})
	}
}

func TestStorage_StoreAndEviction(t *testing.T) {
	s, err := storage.New(10) // MinMemoryLimit = 10
	if err != nil {
		t.Fatalf("New(10): %v", err)
	}

	for i := 1; i <= 25; i++ {
		s.Store(model.LogEntry{
			Timestamp: time.Now().UTC(),
			Service:   "test-svc",
			Level:     "info",
			Message:   fmt.Sprintf("log-%d", i),
		})
	}

	stats := s.GetStats()
	if stats.Total != 25 {
		t.Errorf("Total = %d, ожидалось 25", stats.Total)
	}
	if stats.InMemory > 10 {
		t.Errorf("InMemory = %d, не должно превышать лимит 10", stats.InMemory)
	}

	// Проверяем, что Query возвращает самые свежие логи (log-25 первый)
	logs := s.Query(storage.QueryParams{Limit: 5})
	if len(logs) != 5 {
		t.Fatalf("Query вернул %d логов, ожидалось 5", len(logs))
	}
	if logs[0].Message != "log-25" {
		t.Errorf("Первый лог должен быть 'log-25', получен '%s'", logs[0].Message)
	}
}

func TestStorage_QueryMemorySafety(t *testing.T) {
	s, err := storage.New(50)
	if err != nil {
		t.Fatalf("New(50): %v", err)
	}

	// Заполняем всего 3 записи
	for i := 1; i <= 3; i++ {
		s.Store(model.LogEntry{
			Service: "order-service",
			Level:   "info",
			Message: fmt.Sprintf("order #%d", i),
		})
	}

	// Запрашиваем заведомо огромный limit (например, 100 000)
	logs := s.Query(storage.QueryParams{
		Limit: 100_000,
	})

	// Должно вернуть только 3 записи без паники и гигантских аллокаций
	if len(logs) != 3 {
		t.Errorf("Query вернул %d записей, ожидалось 3", len(logs))
	}

	// Запрашиваем отрицательный или нулевой limit
	logsZero := s.Query(storage.QueryParams{Limit: 0})
	if len(logsZero) != 3 {
		t.Errorf("Query с limit=0 вернул %d записей, ожидалось 3", len(logsZero))
	}
}

func TestStorage_QueryFilters(t *testing.T) {
	s, err := storage.New(50)
	if err != nil {
		t.Fatalf("New(50): %v", err)
	}

	s.Store(model.LogEntry{Service: "auth", Level: "info", Message: "user login"})
	s.Store(model.LogEntry{Service: "auth", Level: "error", Message: "invalid password"})
	s.Store(model.LogEntry{Service: "payment", Level: "info", Message: "charge success"})
	s.Store(model.LogEntry{Service: "payment", Level: "error", Message: "gateway timeout"})

	// Фильтр по сервису
	authLogs := s.Query(storage.QueryParams{Service: "auth"})
	if len(authLogs) != 2 {
		t.Errorf("auth logs: ожидалось 2, получено %d", len(authLogs))
	}

	// Фильтр по уровню
	errLogs := s.Query(storage.QueryParams{Level: "error"})
	if len(errLogs) != 2 {
		t.Errorf("error logs: ожидалось 2, получено %d", len(errLogs))
	}

	// Поиск по подстроке
	passLogs := s.Query(storage.QueryParams{Search: "password"})
	if len(passLogs) != 1 || passLogs[0].Message != "invalid password" {
		t.Errorf("search password: не найдено")
	}
}

func TestStorage_LoadFromFile_Success(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Создаем файловый синк и записываем 3 лога
	fileSink, err := sink.NewFile(tempDir, "flat")
	if err != nil {
		t.Fatalf("sink.NewFile: %v", err)
	}

	ctx := context.Background()
	_ = fileSink.Write(ctx, model.LogEntry{Service: "auth", Level: "info", Message: "login ok", Timestamp: time.Now().UTC()})
	_ = fileSink.Write(ctx, model.LogEntry{Service: "auth", Level: "error", Message: "db error", Timestamp: time.Now().UTC()})
	_ = fileSink.Write(ctx, model.LogEntry{Service: "order", Level: "info", Message: "order created", Timestamp: time.Now().UTC()})
	_ = fileSink.Close()

	// 2. Создаем новое хранилище (симуляция запуска нового процесса)
	s, err := storage.New(100)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}

	// До загрузки хранилище пустое
	if s.GetStats().InMemory != 0 {
		t.Fatalf("новое хранилище должно быть пустым, получено InMemory=%d", s.GetStats().InMemory)
	}

	// 3. Загружаем данные из директории файлового синка
	if err := s.LoadFromFile(tempDir); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	stats := s.GetStats()
	if stats.InMemory != 3 {
		t.Errorf("InMemory = %d, ожидалось 3", stats.InMemory)
	}
	if stats.ByService["auth"] != 2 {
		t.Errorf("ByService['auth'] = %d, ожидалось 2", stats.ByService["auth"])
	}
	if stats.ByService["order"] != 1 {
		t.Errorf("ByService['order'] = %d, ожидалось 1", stats.ByService["order"])
	}
	if stats.ByLevel["error"] != 1 {
		t.Errorf("ByLevel['error'] = %d, ожидалось 1", stats.ByLevel["error"])
	}

	// Проверяем Query
	logs := s.Query(storage.QueryParams{})
	if len(logs) != 3 {
		t.Errorf("Query вернул %d логов, ожидалось 3", len(logs))
	}
}

func TestStorage_LoadFromFile_NonExistentDir(t *testing.T) {
	s, err := storage.New(100)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}

	// Несуществующая директория не должна вызывать ошибку (штатная ситуация первого запуска)
	err = s.LoadFromFile(t.TempDir() + "/does_not_exist")
	if err != nil {
		t.Errorf("LoadFromFile для пустой/несуществующей папки вернул ошибку: %v", err)
	}

	if s.GetStats().InMemory != 0 {
		t.Errorf("InMemory = %d, ожидалось 0", s.GetStats().InMemory)
	}
}

func TestStorage_LoadFromFile_OnlyFileSinkIsRestored(t *testing.T) {
	tempDir := t.TempDir()

	// Эмулируем сессию до рестарта:
	// Лог 1 пишется в память и в файл (правило со сбросом в file)
	// Лог 2 пишется только в память (правило без file, например только stdout)
	fileSink, err := sink.NewFile(tempDir, "by-service")
	if err != nil {
		t.Fatalf("sink.NewFile: %v", err)
	}

	ctx := context.Background()
	_ = fileSink.Write(ctx, model.LogEntry{Service: "persisted-service", Level: "error", Message: "will survive", Timestamp: time.Now().UTC()})
	_ = fileSink.Close()

	// Запускаем второй процесс (после рестарта)
	sAfterRestart, err := storage.New(100)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}

	// Загружаем только то, что сохранил файловый синк
	if err := sAfterRestart.LoadFromFile(tempDir); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	// Проверяем, что восстановился только сохранённый лог
	logs := sAfterRestart.Query(storage.QueryParams{})
	if len(logs) != 1 {
		t.Fatalf("после рестарта восстановилось %d логов, ожидался 1", len(logs))
	}
	if logs[0].Service != "persisted-service" || logs[0].Message != "will survive" {
		t.Errorf("неожиданный лог после восстановления: %+v", logs[0])
	}
}

