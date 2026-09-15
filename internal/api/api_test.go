// Интеграционный тест HTTP API aggregator'а.
// Использует httptest.NewServer — поднимает реальный HTTP-сервер на случайном порту,
// без моков, с полной цепочкой: хендлер → канал → воркер → хранилище.
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/api"
	"github.com/stepankolchin/log-aggregator/internal/config"
	"github.com/stepankolchin/log-aggregator/internal/ingest"
	"github.com/stepankolchin/log-aggregator/internal/model"
	"github.com/stepankolchin/log-aggregator/internal/router"
	"github.com/stepankolchin/log-aggregator/internal/sink"
	"github.com/stepankolchin/log-aggregator/internal/storage"
)

// testEnv — окружение для интеграционных тестов.
type testEnv struct {
	server    *httptest.Server
	apiServer *api.Server
	stor      *storage.Storage
	cancel    context.CancelFunc
}

// newTestEnv поднимает полный стек aggregator'а на случайном порту.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	stor, err := storage.New(500)
	if err != nil {
		t.Fatalf("ошибка создания хранилища: %v", err)
	}
	queue := make(chan model.LogEntry, 64)

	rtr, err := router.New([]config.RouteConfig{
		{Name: "default", Match: config.MatchConfig{}, Sinks: []string{"stdout"}},
	}, []router.Sink{sink.NewStdout("json")})
	if err != nil {
		t.Fatalf("ошибка создания роутера: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Горутина-воркер: вычитывает из канала, роутит и сохраняет в storage
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-queue:
				rtr.Process(ctx, e)
				stor.Store(e)
			}
		}
	}()

	handler := ingest.NewHandler(queue)
	cfg := config.ServerConfig{
		Port:              0, // порт не нужен — используем httptest
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		DefaultQueryLimit: 100,
		MaxQueryLimit:     1000,
	}
	logDir := t.TempDir()
	srv := api.New(cfg, handler, stor, rtr, logDir)
	ts := httptest.NewServer(srv.HTTPHandler())

	t.Cleanup(func() {
		cancel()
		ts.Close()
	})

	return &testEnv{server: ts, apiServer: srv, stor: stor, cancel: cancel}
}

// post — вспомогательная функция для HTTP POST запроса.
func post(t *testing.T, ts *httptest.Server, path, body string) *http.Response {
	t.Helper()
	resp, err := ts.Client().Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// get — вспомогательная функция для HTTP GET запроса.
func get(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// ── Тесты приёма логов ────────────────────────────────────────────────────────

func TestHandleSingle_ValidLog(t *testing.T) {
	env := newTestEnv(t)

	body := `{"service":"auth","level":"info","message":"user logged in"}`
	resp := post(t, env.server, "/api/v1/logs", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("статус: ожидался 202, получен %d", resp.StatusCode)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "accepted" {
		t.Errorf("тело ответа: ожидался status=accepted, получен %v", result)
	}
}

func TestHandleSingle_InvalidJSON(t *testing.T) {
	env := newTestEnv(t)

	resp := post(t, env.server, "/api/v1/logs", `{not valid json`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("статус: ожидался 400, получен %d", resp.StatusCode)
	}
}

func TestHandleSingle_MissingService(t *testing.T) {
	env := newTestEnv(t)

	// service отсутствует — должно вернуть 422 Unprocessable Entity
	resp := post(t, env.server, "/api/v1/logs", `{"level":"info","message":"hello"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("статус: ожидался 422, получен %d", resp.StatusCode)
	}
}

func TestHandleSingle_InvalidLevel(t *testing.T) {
	env := newTestEnv(t)

	resp := post(t, env.server, "/api/v1/logs",
		`{"service":"svc","level":"MEGAWARN","message":"hello"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("статус: ожидался 422, получен %d", resp.StatusCode)
	}
}

func TestHandleBatch(t *testing.T) {
	env := newTestEnv(t)

	body := `{"logs":[
		{"service":"svc","level":"info","message":"first"},
		{"service":"svc","level":"warn","message":"second"},
		{"service":"svc","level":"INVALID","message":"bad level"}
	]}`
	resp := post(t, env.server, "/api/v1/logs/batch", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("статус: ожидался 202, получен %d", resp.StatusCode)
	}

	var result model.BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if result.Accepted != 2 {
		t.Errorf("accepted: ожидалось 2, получено %d", result.Accepted)
	}
	if result.Errors != 1 {
		t.Errorf("errors: ожидалась 1 ошибка валидации, получено %d", result.Errors)
	}
	if len(result.Failed) != 1 {
		t.Fatalf("failed items: ожидался 1 элемент, получено %d", len(result.Failed))
	}
	if result.Failed[0].Index != 2 {
		t.Errorf("failed index: ожидался 2, получен %d", result.Failed[0].Index)
	}
	if result.Failed[0].Status != "validation_error" {
		t.Errorf("failed status: ожидался validation_error, получен %s", result.Failed[0].Status)
	}
	if result.Failed[0].Retryable != false {
		t.Errorf("failed retryable: ожидался false для ошибки валидации")
	}
}

func TestHandleBatch_AllValidationErrors(t *testing.T) {
	env := newTestEnv(t)

	body := `{"logs":[
		{"service":"svc","level":"INVALID_1","message":"first"},
		{"service":"svc","level":"INVALID_2","message":"second"}
	]}`
	resp := post(t, env.server, "/api/v1/logs/batch", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("статус: ожидался 422, получен %d", resp.StatusCode)
	}

	var result model.BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if result.Accepted != 0 || result.Errors != 2 {
		t.Errorf("accepted=%d, errors=%d, ожидалось (0, 2)", result.Accepted, result.Errors)
	}
	if len(result.Failed) != 2 {
		t.Fatalf("failed items: ожидалось 2 элемента, получено %d", len(result.Failed))
	}
	if result.Failed[0].Retryable || result.Failed[1].Retryable {
		t.Errorf("ошибки валидации не должны быть retryable")
	}
}

func TestHandleBatch_QueueFull(t *testing.T) {
	// Создаем хендлер с полностью заполненной очередью (емкость 0)
	fullQueue := make(chan model.LogEntry) // небуферизованный канал без читателей
	handler := ingest.NewHandler(fullQueue)

	cfg := config.ServerConfig{
		Port:         0,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}
	srv := api.New(cfg, handler, nil, nil, t.TempDir())
	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	body := `{"logs":[
		{"service":"svc","level":"info","message":"first"},
		{"service":"svc","level":"warn","message":"second"}
	]}`
	resp := post(t, ts, "/api/v1/logs/batch", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("статус: ожидался 503, получен %d", resp.StatusCode)
	}

	var result model.BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if result.Accepted != 0 || result.Dropped != 2 {
		t.Errorf("accepted=%d, dropped=%d, ожидалось (0, 2)", result.Accepted, result.Dropped)
	}
	if len(result.Failed) != 2 {
		t.Fatalf("failed items: ожидалось 2 элемента, получено %d", len(result.Failed))
	}
	if !result.Failed[0].Retryable || !result.Failed[1].Retryable {
		t.Errorf("ошибки переполнения очереди должны быть retryable=true")
	}
	if result.Failed[0].Status != "queue_full" {
		t.Errorf("status: expected queue_full, got %s", result.Failed[0].Status)
	}
}

func TestHandleBatch_Empty(t *testing.T) {
	env := newTestEnv(t)

	resp := post(t, env.server, "/api/v1/logs/batch", `{"logs":[]}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("статус: ожидался 400, получен %d", resp.StatusCode)
	}
}

// ── Тесты GET-эндпоинтов ─────────────────────────────────────────────────────

func TestHandleGetLogs(t *testing.T) {
	env := newTestEnv(t)

	// Заполняем хранилище напрямую (без HTTP) — избегаем гонки воркера
	env.stor.Store(model.LogEntry{Service: "auth", Level: "info", Message: "login ok", Timestamp: time.Now()})
	env.stor.Store(model.LogEntry{Service: "order", Level: "error", Message: "failed", Timestamp: time.Now()})

	resp := get(t, env.server, "/api/v1/logs")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("статус: ожидался 200, получен %d", resp.StatusCode)
	}

	if resp.Header.Get("X-Limit") != "100" {
		t.Errorf("X-Limit header = %s, ожидался 100", resp.Header.Get("X-Limit"))
	}

	var logs []model.LogEntry
	if err := json.NewDecoder(resp.Body).Decode(&logs); err != nil {
		t.Fatalf("не удалось декодировать ответ: %v", err)
	}
	if len(logs) != 2 {
		t.Errorf("ожидалось 2 лога, получено %d", len(logs))
	}
}

func TestHandleGetLogs_FilterByService(t *testing.T) {
	env := newTestEnv(t)

	env.stor.Store(model.LogEntry{Service: "auth", Level: "info", Message: "login", Timestamp: time.Now()})
	env.stor.Store(model.LogEntry{Service: "order", Level: "info", Message: "order", Timestamp: time.Now()})
	env.stor.Store(model.LogEntry{Service: "auth", Level: "error", Message: "fail", Timestamp: time.Now()})

	resp := get(t, env.server, "/api/v1/logs?service=auth")
	defer resp.Body.Close()

	var logs []model.LogEntry
	json.NewDecoder(resp.Body).Decode(&logs)

	if len(logs) != 2 {
		t.Errorf("фильтр service=auth: ожидалось 2, получено %d", len(logs))
	}
	for _, l := range logs {
		if l.Service != "auth" {
			t.Errorf("фильтр не сработал: получили лог от %q", l.Service)
		}
	}
}

func TestHandleGetLogs_LimitValidation(t *testing.T) {
	env := newTestEnv(t)

	// Отрицательный limit -> 400 Bad Request
	resp1 := get(t, env.server, "/api/v1/logs?limit=-5")
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusBadRequest {
		t.Errorf("limit=-5: ожидался 400, получен %d", resp1.StatusCode)
	}

	// Не число -> 400 Bad Request
	resp2 := get(t, env.server, "/api/v1/logs?limit=abc")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("limit=abc: ожидался 400, получен %d", resp2.StatusCode)
	}

	// Превышение максимума (например 5000 при max 1000) -> 400 Bad Request с текстом про конфиг
	resp3 := get(t, env.server, "/api/v1/logs?limit=5000")
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Errorf("limit=5000: ожидался 400, получен %d", resp3.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp3.Body)
	if !strings.Contains(string(bodyBytes), "server.max_query_limit") {
		t.Errorf("ожидалось упоминание server.max_query_limit в ответе, получено: %s", string(bodyBytes))
	}

	// Отрицательный offset -> 400 Bad Request
	resp4 := get(t, env.server, "/api/v1/logs?offset=-1")
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusBadRequest {
		t.Errorf("offset=-1: ожидался 400, получен %d", resp4.StatusCode)
	}

	// Некорректная дата -> 400 Bad Request
	resp5 := get(t, env.server, "/api/v1/logs?from=not-a-date")
	resp5.Body.Close()
	if resp5.StatusCode != http.StatusBadRequest {
		t.Errorf("from=not-a-date: ожидался 400, получен %d", resp5.StatusCode)
	}
}

func TestHandleStats(t *testing.T) {
	env := newTestEnv(t)

	resp := get(t, env.server, "/api/v1/stats")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("статус: ожидался 200, получен %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	// Проверяем наличие обязательных полей в JSON
	for _, field := range []string{"accepted", "dropped", "uptime", "by_service", "by_level"} {
		if !bytes.Contains(body, []byte(field)) {
			t.Errorf("в ответе stats отсутствует поле %q", field)
		}
	}
}

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d        time.Duration
		expected string
	}{
		{5 * time.Second, "5s"},
		{45*time.Minute + 12*time.Second, "45m 12s"},
		{5*time.Hour + 23*time.Minute + 15*time.Second, "5h 23m 15s"},
		{2*24*time.Hour + 4*time.Hour + 10*time.Minute, "2d 4h 10m"},
	}

	for _, tc := range cases {
		t.Run(tc.expected, func(t *testing.T) {
			res := api.FormatUptime(tc.d)
			if res != tc.expected {
				t.Errorf("FormatUptime(%v) = %q, ожидалось %q", tc.d, res, tc.expected)
			}
		})
	}
}

func TestHandleRoutes(t *testing.T) {
	env := newTestEnv(t)

	resp := get(t, env.server, "/api/v1/routes")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("статус: ожидался 200, получен %d", resp.StatusCode)
	}

	var routes []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&routes); err != nil {
		t.Fatalf("не удалось декодировать ответ: %v", err)
	}
	if len(routes) == 0 {
		t.Error("список правил не должен быть пустым")
	}
}

func TestHandleHealthz(t *testing.T) {
	env := newTestEnv(t)

	resp := get(t, env.server, "/healthz")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: ожидался 200, получен %d", resp.StatusCode)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "ok" {
		t.Errorf("healthz: ожидался status=ok, получен %v", result)
	}
}

func TestHandleUI(t *testing.T) {
	env := newTestEnv(t)

	resp := get(t, env.server, "/")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /: ожидался 200, получен %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("неверный Content-Type: %s", contentType)
	}
}

func TestHandleCSS(t *testing.T) {
	env := newTestEnv(t)

	resp := get(t, env.server, "/style.css")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /style.css: ожидался 200, получен %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/css") {
		t.Errorf("неверный Content-Type: %s", contentType)
	}
}

func TestHandleJS(t *testing.T) {
	env := newTestEnv(t)

	resp := get(t, env.server, "/app.js")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /app.js: ожидался 200, получен %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "javascript") {
		t.Errorf("неверный Content-Type: %s", contentType)
	}
}

func TestHandleNotFound(t *testing.T) {
	env := newTestEnv(t)

	resp := get(t, env.server, "/nonexistent-path")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("несуществующий путь: ожидался 404, получен %d", resp.StatusCode)
	}
}

func TestHandleReadyz(t *testing.T) {
	env := newTestEnv(t)

	// 1. Исходно сервер готов -> 200 OK
	resp := get(t, env.server, "/readyz")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("readyz: ожидался 200, получен %d", resp.StatusCode)
	}
	var res map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	if res["status"] != "ready" {
		t.Errorf("readyz: ожидался status=ready, получен %v", res)
	}

	// 2. При ручном сбросе готовности (или входе в shutdown) -> 503 Service Unavailable
	env.apiServer.SetReady(false)
	respNotReady := get(t, env.server, "/readyz")
	if respNotReady.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readyz (not ready): ожидался 503, получен %d", respNotReady.StatusCode)
	}
	_ = json.NewDecoder(respNotReady.Body).Decode(&res)
	respNotReady.Body.Close()
	if res["status"] != "not_ready" {
		t.Errorf("readyz: ожидался status=not_ready, получен %v", res)
	}

	// 3. Возвращаем готовность -> снова 200 OK
	env.apiServer.SetReady(true)
	respReadyAgain := get(t, env.server, "/readyz")
	if respReadyAgain.StatusCode != http.StatusOK {
		t.Errorf("readyz: ожидался 200, получен %d", respReadyAgain.StatusCode)
	}
	respReadyAgain.Body.Close()
}

func TestServer_Start_PortConflict(t *testing.T) {
	// Занимаем локальный порт с помощью net.Listen
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	cfg := config.ServerConfig{
		Port:         port,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}
	srv := api.New(cfg, nil, nil, nil, t.TempDir())

	// Start должен синхронно вернуть ошибку о занятом порте
	err = srv.Start()
	if err == nil {
		_ = srv.Shutdown(context.Background())
		t.Fatal("Start на занятом порту должен был вернуть ошибку")
	}
}

func TestServer_Start_SuccessAndShutdown(t *testing.T) {
	// Ищем свободный порт
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // освобождаем порт

	stor, _ := storage.New(100)
	queue := make(chan model.LogEntry, 10)
	handler := ingest.NewHandler(queue)
	rtr, _ := router.New(nil, nil)

	cfg := config.ServerConfig{
		Port:         port,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}
	srv := api.New(cfg, handler, stor, rtr, t.TempDir())

	if err := srv.Start(); err != nil {
		t.Fatalf("Start вернул ошибку: %v", err)
	}

	// Проверяем healthz по реальному TCP-порту
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: expected 200, got %d", resp.StatusCode)
	}

	// Завершаем сервер
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
}

// TestSwaggerUIAndOpenAPI проверяет доступность Swagger UI и спецификаций OpenAPI 3.0.
func TestSwaggerUIAndOpenAPI(t *testing.T) {
	env := newTestEnv(t)

	tests := []struct {
		name        string
		path        string
		wantCode    int
		contentType string
		mustContain string
	}{
		{
			name:        "Swagger UI (/docs)",
			path:        "/docs",
			wantCode:    http.StatusOK,
			contentType: "text/html",
			mustContain: "Swagger UI",
		},
		{
			name:        "Swagger UI (/swagger)",
			path:        "/swagger",
			wantCode:    http.StatusOK,
			contentType: "text/html",
			mustContain: "Swagger UI",
		},
		{
			name:        "OpenAPI YAML (/docs/openapi.yaml)",
			path:        "/docs/openapi.yaml",
			wantCode:    http.StatusOK,
			contentType: "application/yaml",
			mustContain: "openapi: 3.0.3",
		},
		{
			name:        "OpenAPI YAML (/swagger/openapi.yaml)",
			path:        "/swagger/openapi.yaml",
			wantCode:    http.StatusOK,
			contentType: "application/yaml",
			mustContain: "openapi: 3.0.3",
		},
		{
			name:        "OpenAPI JSON (/docs/openapi.json)",
			path:        "/docs/openapi.json",
			wantCode:    http.StatusOK,
			contentType: "application/json",
			mustContain: `"openapi": "3.0.3"`,
		},
		{
			name:        "OpenAPI JSON (/swagger/openapi.json)",
			path:        "/swagger/openapi.json",
			wantCode:    http.StatusOK,
			contentType: "application/json",
			mustContain: `"openapi": "3.0.3"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := get(t, env.server, tt.path)
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantCode {
				t.Fatalf("expected status %d, got %d", tt.wantCode, resp.StatusCode)
			}

			ct := resp.Header.Get("Content-Type")
			if !strings.Contains(ct, tt.contentType) {
				t.Errorf("expected Content-Type containing %q, got %q", tt.contentType, ct)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed reading body: %v", err)
			}

			if !strings.Contains(string(body), tt.mustContain) {
				t.Errorf("expected body to contain %q", tt.mustContain)
			}

			if tt.contentType == "application/json" {
				var parsed map[string]any
				if err := json.Unmarshal(body, &parsed); err != nil {
					t.Fatalf("body is not valid JSON: %v", err)
				}
				if _, ok := parsed["openapi"]; !ok {
					t.Errorf("expected 'openapi' key in parsed JSON")
				}
			}
		})
	}
}

