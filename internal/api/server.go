package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
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
	logDir    string
	http      *http.Server
	handler   *ingest.Handler
	storage   *storage.Storage
	router    *router.Router
	startTime time.Time
	listener  net.Listener
	isReady   atomic.Bool
	errCh     chan error
}

// New создаёт Server и регистрирует все HTTP-маршруты.
func New(
	cfg config.ServerConfig,
	handler *ingest.Handler,
	stor *storage.Storage,
	rtr *router.Router,
	logDir string,
) *Server {
	s := &Server{
		cfg:       cfg,
		logDir:    logDir,
		handler:   handler,
		storage:   stor,
		router:    rtr,
		startTime: time.Now(),
		errCh:     make(chan error, 1),
	}
	s.isReady.Store(true)

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

	// Swagger UI & OpenAPI 3.0 Документация
	mux.HandleFunc("GET /docs", s.handleSwaggerUI)
	mux.HandleFunc("GET /docs/", s.handleSwaggerUI)
	mux.HandleFunc("GET /swagger", s.handleSwaggerUI)
	mux.HandleFunc("GET /swagger/", s.handleSwaggerUI)
	mux.HandleFunc("GET /docs/openapi.yaml", s.handleOpenAPIYAML)
	mux.HandleFunc("GET /swagger/openapi.yaml", s.handleOpenAPIYAML)
	mux.HandleFunc("GET /docs/openapi.json", s.handleOpenAPIJSON)
	mux.HandleFunc("GET /swagger/openapi.json", s.handleOpenAPIJSON)

	// Web UI
	mux.HandleFunc("GET /style.css", s.handleCSS)
	mux.HandleFunc("GET /app.js", s.handleJS)
	mux.HandleFunc("/", s.handleUI)
}

// Start биндится на сетевой порт и запускает обслуживание запросов в фоновой горутине.
// Возвращает ошибку синхронно, если не удалось открыть сокет (например, порт занят).
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("открытие порта %s: %w", s.http.Addr, err)
	}
	s.listener = ln
	s.isReady.Store(true)

	go func() {
		slog.Info("HTTP-сервер запущен", "addr", s.http.Addr)
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.isReady.Store(false)
			slog.Error("HTTP-сервер завершился с ошибкой", "err", err)
			select {
			case s.errCh <- err:
			default:
			}
		}
	}()

	return nil
}

// Errors возвращает канал для получения критических ошибок сервера в runtime.
func (s *Server) Errors() <-chan error {
	return s.errCh
}

// SetReady позволяет вручную переключать состояние готовности сервера (например, в тестах или graceful drain).
func (s *Server) SetReady(ready bool) {
	s.isReady.Store(ready)
}

// Shutdown корректно завершает HTTP-сервер с учётом контекста.
func (s *Server) Shutdown(ctx context.Context) error {
	s.isReady.Store(false)
	return s.http.Shutdown(ctx)
}

// HTTPHandler возвращает HTTP-обработчик — используется в интеграционных тестах
// вместо полного запуска сервера на реальном порту.
func (s *Server) HTTPHandler() http.Handler {
	return s.http.Handler
}

// handleHealthz — GET /healthz. Всегда 200, сигнал что процесс жив (Liveness).
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz — GET /readyz. Проверяет критерии готовности сервиса к приёму трафика (Readiness):
// 1. Сервер не находится в режиме завершения работы (shutdown).
// 2. Инициализированы хранилище (storage) и хендлер приёма (handler).
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if !s.isReady.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not_ready",
			"reason": "сервер выключается или не готов к приёму запросов",
		})
		return
	}

	if s.storage == nil || s.handler == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not_ready",
			"reason": "внутренние компоненты не инициализированы",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// handleUI — отдаёт встроенный HTML-дашборд.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(web.IndexHTML)
}

// handleCSS — отдаёт встроенный файл стилей.
func (s *Server) handleCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(web.StyleCSS)
}

// handleJS — отдаёт встроенный клиентский скрипт.
func (s *Server) handleJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(web.AppJS)
}

// handleSwaggerUI — отдаёт интерактивный интерфейс Swagger UI.
func (s *Server) handleSwaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(web.SwaggerHTML)
}

// handleOpenAPIYAML — отдаёт спецификацию OpenAPI 3.0 в формате YAML.
func (s *Server) handleOpenAPIYAML(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Write(web.OpenAPIYAML)
}

// handleOpenAPIJSON — отдаёт спецификацию OpenAPI 3.0 в формате JSON.
func (s *Server) handleOpenAPIJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(web.OpenAPIJSON)
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
