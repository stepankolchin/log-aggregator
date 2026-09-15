package aggregatorslog_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stepankolchin/log-aggregator/pkg/aggregatorslog"
)

// mockFallbackHandler реализует slog.Handler для проверки перехвата логов в fallback.
type mockFallbackHandler struct {
	mu          sync.Mutex
	records     []slog.Record
	shouldError bool
}

func (m *mockFallbackHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (m *mockFallbackHandler) Handle(_ context.Context, r slog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shouldError {
		return errors.New("fallback write failed")
	}
	m.records = append(m.records, r)
	return nil
}

func (m *mockFallbackHandler) WithAttrs([]slog.Attr) slog.Handler {
	return m
}

func (m *mockFallbackHandler) WithGroup(string) slog.Handler {
	return m
}

func (m *mockFallbackHandler) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

func TestHandler_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/logs" {
			t.Errorf("неожиданный путь: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer ts.Close()

	fallback := &mockFallbackHandler{}
	var errReported atomic.Bool

	h := aggregatorslog.New(ts.URL, "test-service").
		WithClient(ts.Client()).
		WithFallback(fallback).
		WithErrorHandler(func(err error, r slog.Record) {
			errReported.Store(true)
		})

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "test success message", 0)
	err := h.Handle(context.Background(), rec)
	if err != nil {
		t.Fatalf("ожидался nil, получена ошибка: %v", err)
	}

	sent, errs, dropped := h.Stats()
	if sent != 1 || errs != 0 || dropped != 0 {
		t.Errorf("Stats() = (sent: %d, errors: %d, dropped: %d), ожидалось (1, 0, 0)", sent, errs, dropped)
	}
	if fallback.count() != 0 {
		t.Errorf("fallback не должен был вызываться, вызовов: %d", fallback.count())
	}
	if errReported.Load() {
		t.Errorf("onError не должен был вызываться при успешной отправке")
	}
}

func TestHandler_ErrorStatusCodes_SavedToFallback(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "422 Unprocessable Entity (ошибка валидации)",
			statusCode: http.StatusUnprocessableEntity,
			body:       `{"error":"service is required"}`,
		},
		{
			name:       "503 Service Unavailable (очередь переполнена)",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"error":"queue is full"}`,
		},
		{
			name:       "500 Internal Server Error",
			statusCode: http.StatusInternalServerError,
			body:       `internal error`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer ts.Close()

			fallback := &mockFallbackHandler{}
			var lastErr error
			var lastRec slog.Record
			var errCount int

			h := aggregatorslog.New(ts.URL, "test-service").
				WithClient(ts.Client()).
				WithFallback(fallback).
				WithErrorHandler(func(err error, r slog.Record) {
					lastErr = err
					lastRec = r
					errCount++
				})

			rec := slog.NewRecord(time.Now(), slog.LevelWarn, "warning log", 0)
			err := h.Handle(context.Background(), rec)

			if err == nil {
				t.Fatalf("ожидалась ошибка для статуса %d, получен nil", tt.statusCode)
			}

			// Ошибка была, но лог записан в fallback -> dropped == 0!
			sent, errs, dropped := h.Stats()
			if sent != 0 || errs != 1 || dropped != 0 {
				t.Errorf("Stats() = (sent: %d, errors: %d, dropped: %d), ожидалось (0, 1, 0)", sent, errs, dropped)
			}

			if fallback.count() != 1 {
				t.Errorf("fallback должен был получить 1 запись, получено: %d", fallback.count())
			} else if fallback.records[0].Message != "warning log" {
				t.Errorf("неверное сообщение в fallback: %s", fallback.records[0].Message)
			}

			if errCount != 1 {
				t.Errorf("onError должен быть вызван ровно 1 раз, вызван: %d", errCount)
			}
			if lastRec.Message != "warning log" {
				t.Errorf("в onError передана неверная запись: %s", lastRec.Message)
			}
			if lastErr == nil {
				t.Errorf("в onError должна передаваться непустая ошибка")
			}
		})
	}
}

func TestHandler_WithoutFallback_Dropped(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	// Отключаем fallback: лог не дошёл до агрегатора И некуда сохранить -> dropped == 1
	h := aggregatorslog.New(ts.URL, "test-service").
		WithClient(ts.Client()).
		WithoutFallback()

	rec := slog.NewRecord(time.Now(), slog.LevelError, "critical log", 0)
	err := h.Handle(context.Background(), rec)
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	sent, errs, dropped := h.Stats()
	if sent != 0 || errs != 1 || dropped != 1 {
		t.Errorf("Stats() = (%d, %d, %d), ожидалось (0, 1, 1)", sent, errs, dropped)
	}
}

func TestHandler_FallbackFails_Dropped(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	failingFallback := &mockFallbackHandler{shouldError: true}

	h := aggregatorslog.New(ts.URL, "test-service").
		WithClient(ts.Client()).
		WithFallback(failingFallback)

	rec := slog.NewRecord(time.Now(), slog.LevelError, "critical log", 0)
	_ = h.Handle(context.Background(), rec)

	sent, errs, dropped := h.Stats()
	if sent != 0 || errs != 1 || dropped != 1 {
		t.Errorf("Stats() = (%d, %d, %d), ожидалось (0, 1, 1) т.к. fallback упал", sent, errs, dropped)
	}
}

func TestHandler_NetworkError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	// Закрываем сервер немедленно, чтобы вызвать сетевую ошибку (connection refused)
	ts.Close()

	fallback := &mockFallbackHandler{}
	var hookCalled bool

	h := aggregatorslog.New(ts.URL, "test-service").
		WithFallback(fallback).
		WithErrorHandler(func(err error, r slog.Record) {
			hookCalled = true
		})

	rec := slog.NewRecord(time.Now(), slog.LevelError, "db failed", 0)
	err := h.Handle(context.Background(), rec)

	if err == nil {
		t.Fatalf("ожидалась сетевая ошибка, получен nil")
	}

	sent, errs, dropped := h.Stats()
	if sent != 0 || errs != 1 || dropped != 0 {
		t.Errorf("Stats() = (%d, %d, %d), ожидалось (0, 1, 0)", sent, errs, dropped)
	}

	if fallback.count() != 1 {
		t.Errorf("fallback должен был сохранить лог при сетевой ошибке")
	}
	if !hookCalled {
		t.Errorf("onError должен был быть вызван при сетевой ошибке")
	}
}

func TestHandler_SlogLoggerIntegration(t *testing.T) {
	// Демонстрация: slog.Logger игнорирует ошибку Handle(), но fallback и Stats работают
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"queue is full"}`)
	}))
	defer ts.Close()

	fallback := &mockFallbackHandler{}
	var capturedErr error

	h := aggregatorslog.New(ts.URL, "api-service").
		WithClient(ts.Client()).
		WithFallback(fallback).
		WithErrorHandler(func(err error, r slog.Record) {
			capturedErr = err
		})

	logger := slog.New(h)

	// Пользователь вызывает logger.Info — в стандартном slog ошибка Handle отбрасывается
	logger.Info("important order created", "order_id", 12345)

	// Благодаря fallback запись спасена:
	if fallback.count() != 1 {
		t.Fatalf("ожидалась 1 запись в fallback, получено: %d", fallback.count())
	}
	if fallback.records[0].Message != "important order created" {
		t.Errorf("неверное сообщение в fallback: %s", fallback.records[0].Message)
	}

	// errors == 1, но dropped == 0!
	sent, errs, dropped := h.Stats()
	if sent != 0 || errs != 1 || dropped != 0 {
		t.Errorf("Stats = (%d, %d, %d), ожидалось (0, 1, 0)", sent, errs, dropped)
	}

	if capturedErr == nil {
		t.Errorf("onError должен был перехватить ошибку 503")
	}
}

func TestHandler_CloningSharesStats(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	h := aggregatorslog.New(ts.URL, "main-svc").WithClient(ts.Client())

	h1 := h.WithAttrs([]slog.Attr{slog.String("region", "eu-central")})
	h2 := h.WithGroup("billing")

	ctx := context.Background()
	_ = h.Handle(ctx, slog.NewRecord(time.Now(), slog.LevelInfo, "log 1", 0))
	_ = h1.Handle(ctx, slog.NewRecord(time.Now(), slog.LevelInfo, "log 2", 0))
	_ = h2.Handle(ctx, slog.NewRecord(time.Now(), slog.LevelInfo, "log 3", 0))

	sent, errs, dropped := h.Stats()
	if sent != 3 || errs != 0 || dropped != 0 {
		t.Errorf("ожидалось 3 sent во всех клонах, получено: (%d, %d, %d)", sent, errs, dropped)
	}

	h.ResetStats()
	sent, _, _ = h.Stats()
	if sent != 0 {
		t.Errorf("после ResetStats() sent должен быть 0, получено %d", sent)
	}
}

func TestHandler_Enabled(t *testing.T) {
	h := aggregatorslog.New("http://localhost:8080", "test").WithMinLevel(slog.LevelWarn)

	ctx := context.Background()
	if h.Enabled(ctx, slog.LevelDebug) {
		t.Errorf("LevelDebug не должен быть Enabled")
	}
	if h.Enabled(ctx, slog.LevelInfo) {
		t.Errorf("LevelInfo не должен быть Enabled")
	}
	if !h.Enabled(ctx, slog.LevelWarn) {
		t.Errorf("LevelWarn должен быть Enabled")
	}
	if !h.Enabled(ctx, slog.LevelError) {
		t.Errorf("LevelError должен быть Enabled")
	}
}

func TestHandler_WithGroup_PrefixesFields(t *testing.T) {
	var receivedPayload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedPayload)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	h := aggregatorslog.New(ts.URL, "auth-service").WithClient(ts.Client())
	logger := slog.New(h).WithGroup("http")

	// Отправляем лог с атрибутами
	logger.Info("request completed", "method", "POST", "status", "200")

	if receivedPayload == nil {
		t.Fatal("payload не получен")
	}

	// Service должен остаться "auth-service"
	if receivedPayload["service"] != "auth-service" {
		t.Errorf("service = %v, ожидалось 'auth-service'", receivedPayload["service"])
	}

	fields, ok := receivedPayload["fields"].(map[string]any)
	if !ok {
		t.Fatalf("fields = %v, ожидался объект", receivedPayload["fields"])
	}

	if fields["http.method"] != "POST" {
		t.Errorf("http.method = %v, ожидалось 'POST'", fields["http.method"])
	}
	if fields["http.status"] != "200" {
		t.Errorf("http.status = %v, ожидалось '200'", fields["http.status"])
	}
}

func TestHandler_NestedGroupsAndKindGroup(t *testing.T) {
	var receivedPayload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedPayload)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	h := aggregatorslog.New(ts.URL, "order-service").WithClient(ts.Client())
	// Вложенные группы: req -> db + постоянный атрибут + slog.Group
	logger := slog.New(h).
		WithGroup("req").
		With(slog.String("id", "req-123")).
		WithGroup("db")

	logger.Info("query executed",
		slog.String("query", "SELECT 1"),
		slog.Group("metrics", slog.Int("duration_ms", 12), slog.Int("rows", 1)),
	)

	if receivedPayload == nil {
		t.Fatal("payload не получен")
	}

	if receivedPayload["service"] != "order-service" {
		t.Errorf("service = %v, ожидалось 'order-service'", receivedPayload["service"])
	}

	fields, ok := receivedPayload["fields"].(map[string]any)
	if !ok {
		t.Fatalf("fields = %v, ожидался объект", receivedPayload["fields"])
	}

	if fields["req.id"] != "req-123" {
		t.Errorf("req.id = %v, ожидалось 'req-123'", fields["req.id"])
	}
	if fields["req.db.query"] != "SELECT 1" {
		t.Errorf("req.db.query = %v, ожидалось 'SELECT 1'", fields["req.db.query"])
	}
	if fields["req.db.metrics.duration_ms"] != "12" {
		t.Errorf("req.db.metrics.duration_ms = %v, ожидалось '12'", fields["req.db.metrics.duration_ms"])
	}
	if fields["req.db.metrics.rows"] != "1" {
		t.Errorf("req.db.metrics.rows = %v, ожидалось '1'", fields["req.db.metrics.rows"])
	}
}
