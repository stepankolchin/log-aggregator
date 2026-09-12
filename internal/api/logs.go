package api

import (
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
//	limit   — максимальное число записей (по умолчанию 100)
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := 100
	if raw := q.Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
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
