package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// QueryArchive выполняет поиск по файлам архива (YYYY-MM-DD.jsonl) в logDir.
// Возвращает выборку логов (начиная с самых свежих), признак наличия следующих записей (hasMore) и ошибку.
func QueryArchive(logDir string, p QueryParams) ([]model.LogEntry, bool, error) {
	if logDir == "" {
		return nil, false, nil
	}

	limit := p.Limit
	if limit <= 0 {
		limit = DefaultQueryLimit
	} else if limit > MaxQueryLimit {
		limit = MaxQueryLimit
	}

	offset := p.Offset
	if offset < 0 {
		offset = 0
	}

	type dayFile struct {
		path string
		date time.Time
	}
	var matchedFiles []dayFile

	err := filepath.WalkDir(logDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		base := strings.TrimSuffix(d.Name(), ".jsonl")
		parent := filepath.Base(filepath.Dir(path))

		// Пробуем распарсить дату либо из имени файла (flat, by-service),
		// либо из имени родительской папки (by-date)
		dTime, err := time.Parse("2006-01-02", base)
		if err != nil {
			dTime, err = time.Parse("2006-01-02", parent)
			if err != nil {
				return nil
			}
		}

		// Фильтр по диапазону дат
		if !p.From.IsZero() {
			fromDay := time.Date(p.From.Year(), p.From.Month(), p.From.Day(), 0, 0, 0, 0, time.UTC)
			if dTime.Before(fromDay) {
				return nil
			}
		}
		if !p.To.IsZero() {
			toDay := time.Date(p.To.Year(), p.To.Month(), p.To.Day(), 23, 59, 59, 999999999, time.UTC)
			if dTime.After(toDay) {
				return nil
			}
		}

		matchedFiles = append(matchedFiles, dayFile{path: path, date: dTime})
		return nil
	})
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("чтение директории логов %q: %w", logDir, err)
	}

	// Сортируем файлы по дате убывания (самые свежие дни первыми)
	sort.Slice(matchedFiles, func(i, j int) bool {
		return matchedFiles[i].date.After(matchedFiles[j].date)
	})

	search := strings.ToLower(p.Search)
	result := make([]model.LogEntry, 0, limit)
	matched := 0
	hasMore := false

	// Обходим файлы день за днем от новых к старым
	for _, df := range matchedFiles {
		entries, err := readDayFile(df.path)
		if err != nil {
			continue
		}

		// Внутри файла записи идут хронологически от старых к новым.
		// Обходим с конца (самые новые записи дня первыми).
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]

			if p.Service != "" && e.Service != p.Service {
				continue
			}
			if p.Level != "" && e.Level != p.Level {
				continue
			}
			if search != "" && !strings.Contains(strings.ToLower(e.Message), search) {
				continue
			}
			t := e.EffectiveTime()
			if !p.From.IsZero() && t.Before(p.From) {
				continue
			}
			if !p.To.IsZero() && t.After(p.To) {
				continue
			}

			matched++
			if matched <= offset {
				continue
			}

			if len(result) < limit {
				result = append(result, e)
			} else {
				hasMore = true
				break
			}
		}

		if hasMore {
			break
		}
	}

	return result, hasMore, nil
}

// readDayFile вычитывает все валидные записи из одного файла .jsonl.
func readDayFile(path string) ([]model.LogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []model.LogEntry
	scanner := bufio.NewScanner(f)
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
	return entries, scanner.Err()
}
