package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

const (
	// MinMemoryLimit — минимально допустимый лимит записей в памяти.
	MinMemoryLimit = 10
	// DefaultMemoryLimit — лимит по умолчанию (5 000 записей).
	DefaultMemoryLimit = 5000
	// MaxMemoryLimit — абсолютная верхняя граница записей в памяти для предотвращения OOM (1 000 000 записей).
	MaxMemoryLimit = 1_000_000

	// MaxQueryLimit — максимальное количество записей, возвращаемых за один API-запрос (1 000).
	MaxQueryLimit = 1000
	// DefaultQueryLimit — количество записей по умолчанию при API-запросе (100).
	DefaultQueryLimit = 100
)

// QueryParams — параметры фильтрации при запросе логов через API.
type QueryParams struct {
	Service string
	Level   string
	Search  string // поиск по подстроке в message
	Limit   int
}

// Stats — снимок статистики хранилища.
type Stats struct {
	Total     int64            `json:"total"`
	InMemory  int              `json:"in_memory"`
	ByService map[string]int64 `json:"by_service"`
	ByLevel   map[string]int64 `json:"by_level"`
}

// Storage хранит логи в памяти и ведёт статистику по сервисам и уровням.
type Storage struct {
	mu    sync.RWMutex
	logs  []model.LogEntry
	limit int

	// Счётчики защищены тем же мьютексом — не нужны atomics
	total     int64
	byService map[string]int64
	byLevel   map[string]int64
}

// New создаёт хранилище с лимитом записей в памяти.
// Возвращает явную ошибку, если лимит выходит за допустимый диапазон [MinMemoryLimit, MaxMemoryLimit].
func New(limit int) (*Storage, error) {
	if limit < MinMemoryLimit || limit > MaxMemoryLimit {
		return nil, fmt.Errorf("storage: недопустимый memory_limit %d (допустимый диапазон: %d..%d)", limit, MinMemoryLimit, MaxMemoryLimit)
	}

	initCap := limit
	if initCap > 1024 {
		initCap = 1024
	}

	return &Storage{
		limit:     limit,
		logs:      make([]model.LogEntry, 0, initCap),
		byService: make(map[string]int64),
		byLevel:   make(map[string]int64),
	}, nil
}

// Limit возвращает установленный лимит записей в памяти.
func (s *Storage) Limit() int {
	return s.limit
}

// Store добавляет запись в память. Если превышен лимит — вытесняет самые старые.
func (s *Storage) Store(entry model.LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logs = append(s.logs, entry)
	if len(s.logs) > s.limit {
		// Отбрасываем минимум 1 запись или четверть лимита
		cutAt := s.limit / 4
		if cutAt < 1 {
			cutAt = 1
		}

		// Зануляем элементы перед срезкой, чтобы сборщик мусора (GC) освободил память
		clear(s.logs[:cutAt])
		s.logs = s.logs[cutAt:]
	}

	s.total++
	s.byService[entry.Service]++
	s.byLevel[entry.Level]++
}

// Query возвращает логи по фильтрам; новые записи идут первыми.
// Безопасно выделяет память с учётом фактического количества логов в хранилище.
func (s *Storage) Query(p QueryParams) []model.LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := p.Limit
	if limit <= 0 {
		limit = DefaultQueryLimit
	} else if limit > MaxQueryLimit {
		limit = MaxQueryLimit
	}

	// Выделяем емкость не больше, чем реально доступно в памяти хранилища
	initCap := limit
	if len(s.logs) < initCap {
		initCap = len(s.logs)
	}

	result := make([]model.LogEntry, 0, initCap)
	search := strings.ToLower(p.Search)

	// Обходим с конца — самые новые записи первыми
	for i := len(s.logs) - 1; i >= 0 && len(result) < limit; i-- {
		e := s.logs[i]
		if p.Service != "" && e.Service != p.Service {
			continue
		}
		if p.Level != "" && e.Level != p.Level {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(e.Message), search) {
			continue
		}
		result = append(result, e)
	}
	return result
}

// GetStats возвращает копию статистики (копия нужна, чтобы не держать блокировку).
func (s *Storage) GetStats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	byService := make(map[string]int64, len(s.byService))
	for k, v := range s.byService {
		byService[k] = v
	}
	byLevel := make(map[string]int64, len(s.byLevel))
	for k, v := range s.byLevel {
		byLevel[k] = v
	}

	return Stats{
		Total:     s.total,
		InMemory:  len(s.logs),
		ByService: byService,
		ByLevel:   byLevel,
	}
}

// LoadFromFile загружает последние записи из файла логов текущего дня.
// Вызывается при старте для восстановления данных после перезапуска.
func (s *Storage) LoadFromFile(logDir string) error {
	today := time.Now().Format("2006-01-02")
	path := filepath.Join(logDir, today+".jsonl")

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil // файла ещё нет — нормальная ситуация при первом запуске
	}
	if err != nil {
		return err
	}
	defer f.Close()

	var entries []model.LogEntry
	scanner := bufio.NewScanner(f)
	// Увеличиваем буфер — строки могут быть длинными из-за поля fields
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e model.LogEntry
		if json.Unmarshal(line, &e) == nil {
			entries = append(entries, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	// Берём только последние limit записей
	if len(entries) > s.limit {
		entries = entries[len(entries)-s.limit:]
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = entries
	for _, e := range entries {
		s.total++
		s.byService[e.Service]++
		s.byLevel[e.Level]++
	}
	return nil
}
