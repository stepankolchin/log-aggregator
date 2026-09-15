package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
	"github.com/stepankolchin/log-aggregator/internal/storage"
)

// handleLogs — GET /api/v1/logs
//
// Параметры запроса:
//
//	service — фильтр по имени сервиса (точное совпадение)
//	level   — фильтр по уровню: debug, info, warn, error
//	search  — подстрока для поиска в поле message
//	from    — начало диапазона времени (RFC3339 или YYYY-MM-DD[ HH:MM:SS])
//	to      — конец диапазона времени (RFC3339 или YYYY-MM-DD[ HH:MM:SS])
//	limit   — число записей на странице (по умолчанию server.default_query_limit, максимум server.max_query_limit)
//	offset  — смещение от начала выборки (по умолчанию 0)
//	source  — источник данных: "memory" (горячий буфер RAM) или "file" (файловый архив)
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := s.cfg.DefaultQueryLimit
	if limit <= 0 {
		limit = 100
	}

	maxLimit := s.cfg.MaxQueryLimit
	if maxLimit <= 0 {
		maxLimit = 1000
	}

	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, "параметр limit должен быть положительным числом")
			return
		}
		if v > maxLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("параметр limit (%d) превышает допустимый максимум (%d), установленный в конфигурации сервера (server.max_query_limit)", v, maxLimit))
			return
		}
		limit = v
	}

	offset := 0
	if raw := q.Get("offset"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, "параметр offset должен быть неотрицательным числом")
			return
		}
		offset = v
	}

	var fromTime, toTime time.Time
	if raw := q.Get("from"); raw != "" {
		t, err := parseTimeFlexible(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "некорректный формат параметра from (ожидается RFC3339 или YYYY-MM-DD[ HH:MM:SS])")
			return
		}
		fromTime = t
	}

	if raw := q.Get("to"); raw != "" {
		t, err := parseTimeFlexible(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "некорректный формат параметра to (ожидается RFC3339 или YYYY-MM-DD[ HH:MM:SS])")
			return
		}
		toTime = t
	}

	params := storage.QueryParams{
		Service: q.Get("service"),
		Level:   q.Get("level"),
		Search:  q.Get("search"),
		From:    fromTime,
		To:      toTime,
		Limit:   limit,
		Offset:  offset,
	}

	source := q.Get("source")
	var logs []model.LogEntry
	var hasMore bool

	// Если явно запрошен файл или указаны исторические даты — читаем из дискового архива
	if source == "file" || source == "archive" || !fromTime.IsZero() || !toTime.IsZero() {
		var err error
		logs, hasMore, err = storage.QueryArchive(s.logDir, params)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("ошибка чтения архива логов: %v", err))
			return
		}
	} else {
		logs = s.storage.Query(params)
		hasMore = len(logs) == limit
	}

	w.Header().Set("X-Limit", strconv.Itoa(limit))
	w.Header().Set("X-Offset", strconv.Itoa(offset))
	w.Header().Set("X-Has-More", strconv.FormatBool(hasMore))

	writeJSON(w, http.StatusOK, logs)
}

func parseTimeFlexible(raw string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("неизвестный формат времени: %q", raw)
}

