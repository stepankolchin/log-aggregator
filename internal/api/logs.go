package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/stepankolchin/log-aggregator/internal/storage"
)

// handleLogs — GET /api/v1/logs
//
// Параметры запроса:
//
//	service — фильтр по имени сервиса (точное совпадение)
//	level   — фильтр по уровню: debug, info, warn, error
//	search  — подстрока для поиска в поле message
//	limit   — число записей (по умолчанию server.default_query_limit, максимум server.max_query_limit)
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

	params := storage.QueryParams{
		Service: q.Get("service"),
		Level:   q.Get("level"),
		Search:  q.Get("search"),
		Limit:   limit,
	}

	logs := s.storage.Query(params)
	writeJSON(w, http.StatusOK, logs)
}
