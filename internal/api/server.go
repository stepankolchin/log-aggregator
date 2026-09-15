package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/config"
	"github.com/stepankolchin/log-aggregator/internal/ingest"
	"github.com/stepankolchin/log-aggregator/internal/router"
	"github.com/stepankolchin/log-aggregator/internal/storage"
	web "github.com/stepankolchin/log-aggregator/web"
)

// Server — HTTP-сервер агрегатора со всеми зарегистрированными маршрутами.
type Server struct {
	cfg       config.ServerConfig
	http      *http.Server
	handler   *ingest.Handler
	storage   *storage.Storage
	router    *router.Router
	startTime time.Time
}

// New создаёт Server и регистрирует все HTTP-маршруты.
func New(
	cfg config.ServerConfig,
	handler *ingest.Handler,
	stor *storage.Storage,
	rtr *router.Router,
) *Server {
	s := &Server{
		cfg:       cfg,
		handler:   handler,
		storage:   stor,
		router:    rtr,
		startTime: time.Now(),
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.http = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}
	return s
}

// registerRoutes регистрирует все маршруты.
// Синтаксис "МЕТОД /путь" доступен начиная с Go 1.22.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Приём логов от микросервисов
	mux.HandleFunc("POST /api/v1/logs", s.handler.HandleSingle)
	mux.HandleFunc("POST /api/v1/logs/batch", s.handler.HandleBatch)

	// Просмотр данных
	mux.HandleFunc("GET /api/v1/logs", s.handleLogs)
	mux.HandleFunc("GET /api/v1/stats", s.handleStats)
	mux.HandleFunc("GET /api/v1/routes", s.handleRoutes)

	// Health-check: liveness и readiness
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	// Web UI
	mux.HandleFunc("GET /style.css", s.handleCSS)
	mux.HandleFunc("GET /app.js", s.handleJS)
	mux.HandleFunc("/", s.handleUI)
}

// Start запускает ListenAndServe в отдельной горутине.
func (s *Server) Start() {
	go func() {
		slog.Info("HTTP-сервер запущен", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP-сервер завершился с ошибкой", "err", err)
		}
	}()
}

// Shutdown корректно завершает HTTP-сервер с учётом контекста.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// HTTPHandler возвращает HTTP-обработчик — используется в интеграционных тестах
// вместо полного запуска сервера на реальном порту.
func (s *Server) HTTPHandler() http.Handler {
	return s.http.Handler
}

// handleHealthz — GET /healthz. Всегда 200, сигнал что процесс жив.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz — GET /readyz. 200 если сервис готов принимать трафик.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// handleUI — отдаёт встроенный HTML-дашборд.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(web.IndexHTML) //nolint:errcheck
}

// handleCSS — отдаёт встроенный файл стилей.
func (s *Server) handleCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(web.StyleCSS) //nolint:errcheck
}

// handleJS — отдаёт встроенный клиентский скрипт.
func (s *Server) handleJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(web.AppJS) //nolint:errcheck
}

// writeJSON сериализует v в JSON и записывает в ResponseWriter.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("ошибка сериализации ответа", "err", err)
	}
}

// writeError отправляет JSON-ответ с описанием ошибки.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
