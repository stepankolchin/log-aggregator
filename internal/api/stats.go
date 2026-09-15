package api

import (
	"fmt"
	"net/http"
	"time"
)

// statsResponse — ответ на GET /api/v1/stats.
type statsResponse struct {
	Uptime   string `json:"uptime"`
	// Статистика приёма (от ingest.Handler)
	Accepted int64 `json:"accepted"`
	Dropped  int64 `json:"dropped"`
	Errors   int64 `json:"errors"`
	// Статистика хранилища (от storage.Storage)
	Total     int64            `json:"total_stored"`
	InMemory  int              `json:"in_memory"`
	ByService map[string]int64 `json:"by_service"`
	ByLevel   map[string]int64 `json:"by_level"`
}

// handleStats — GET /api/v1/stats
// Возвращает объединённую статистику: приём + хранилище + аптайм.
func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	accepted, dropped, errors := s.handler.Stats()
	storStats := s.storage.GetStats()

	resp := statsResponse{
		Uptime:    FormatUptime(time.Since(s.startTime)),
		Accepted:  accepted,
		Dropped:   dropped,
		Errors:    errors,
		Total:     storStats.Total,
		InMemory:  storStats.InMemory,
		ByService: storStats.ByService,
		ByLevel:   storStats.ByLevel,
	}
	writeJSON(w, http.StatusOK, resp)
}

// FormatUptime форматирует длительность в читаемую строку вида "2d 5h 23m", "5h 23m 15s", "23m 15s" или "15s".
func FormatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	sec := d / time.Second

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, sec)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

