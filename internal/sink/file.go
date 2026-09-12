package sink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// File пишет логи в JSON Lines файлы с ежедневной ротацией.
// Файлы называются по дате: 2026-09-12.jsonl
type File struct {
	dir     string
	mu      sync.Mutex
	file    *os.File   // текущий открытый файл
	dateKey string     // дата открытого файла (YYYY-MM-DD)
}

// NewFile создаёт файловый синк с указанной директорией.
func NewFile(dir string) (*File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("создание директории логов %q: %w", dir, err)
	}
	return &File{dir: dir}, nil
}

func (f *File) Name() string { return "file" }

// Write записывает лог в файл текущего дня.
// При смене даты автоматически открывает новый файл (ротация).
func (f *File) Write(_ context.Context, entry model.LogEntry) error {
	today := time.Now().Format("2006-01-02")

	f.mu.Lock()
	defer f.mu.Unlock()

	// Открываем новый файл если он ещё не открыт или день сменился
	if f.file == nil || f.dateKey != today {
		if err := f.rotate(today); err != nil {
			return err
		}
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("сериализация лога: %w", err)
	}
	_, err = fmt.Fprintf(f.file, "%s\n", data)
	return err
}

// rotate закрывает старый файл и открывает новый для указанной даты.
func (f *File) rotate(date string) error {
	if f.file != nil {
		_ = f.file.Close()
	}
	path := filepath.Join(f.dir, date+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("открытие файла лога %q: %w", path, err)
	}
	f.file = file
	f.dateKey = date
	return nil
}

// Close корректно закрывает текущий файл при завершении работы.
func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file != nil {
		return f.file.Close()
	}
	return nil
}
