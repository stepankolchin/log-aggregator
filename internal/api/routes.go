package api

import "net/http"

// handleRoutes — GET /api/v1/routes
// Возвращает список активных правил маршрутизации из конфига.
func (s *Server) handleRoutes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.router.Routes())
}
