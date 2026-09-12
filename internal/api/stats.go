package api

import (
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
		Uptime:    formatUptime(time.Since(s.startTime)),
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

// formatUptime форматирует длительность в читаемую строку.
func formatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	sec := d / time.Second
	if h > 0 {
		return time.Duration(h).String()[0:] + "h " +
			time.Duration(m*time.Minute+sec*time.Second).String()
	}
	return time.Duration(m*time.Minute + sec*time.Second).String()
}
