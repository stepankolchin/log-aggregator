package sink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// File пишет логи в JSON Lines файлы с ежедневной ротацией и поддержкой различных паттернов структуры директорий.
type File struct {
	dir         string
	pattern     string
	mu          sync.Mutex
	files       map[string]*os.File // открытые файлы: путь -> *os.File
	currentDate string              // текущая дата (YYYY-MM-DD) для отслеживания ротации
}

// NewFile создаёт файловый синк с указанной директорией и паттерном организации файлов.
func NewFile(dir, pattern string) (*File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("создание директории логов %q: %w", dir, err)
	}
	if pattern == "" {
		pattern = "flat"
	}
	return &File{
		dir:     dir,
		pattern: strings.ToLower(pattern),
		files:   make(map[string]*os.File),
	}, nil
}

func (f *File) Name() string { return "file" }

// Write записывает лог в файл текущего дня в соответствии с выбранным паттерном.
func (f *File) Write(_ context.Context, entry model.LogEntry) error {
	today := time.Now().Format("2006-01-02")
	service := entry.Service
	if service == "" {
		service = "unknown"
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// При смене дня закрываем все файлы предыдущего дня (ротация)
	if f.currentDate != "" && f.currentDate != today {
		for _, file := range f.files {
			if file != nil {
				_ = file.Close()
			}
		}
		f.files = make(map[string]*os.File)
	}
	f.currentDate = today

	var targetPath string
	switch f.pattern {
	case "by-service":
		targetPath = filepath.Join(f.dir, service, today+".jsonl")
	case "by-date":
		targetPath = filepath.Join(f.dir, today, service+".jsonl")
	case "flat":
		fallthrough
	default:
		targetPath = filepath.Join(f.dir, today+".jsonl")
	}

	file, ok := f.files[targetPath]
	if !ok || file == nil {
		dir := filepath.Dir(targetPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("создание поддиректории логов %q: %w", dir, err)
		}
		newFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("открытие файла лога %q: %w", targetPath, err)
		}
		file = newFile
		f.files[targetPath] = file
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("сериализация лога: %w", err)
	}
	_, err = fmt.Fprintf(file, "%s\n", data)
	return err
}

// Close корректно закрывает все открытые файлы при завершении работы.
func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	var firstErr error
	for path, file := range f.files {
		if file != nil {
			if err := file.Close(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("закрытие файла %q: %w", path, err)
			}
		}
	}
	f.files = make(map[string]*os.File)
	return firstErr
}
