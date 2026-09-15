package sink_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
	"github.com/stepankolchin/log-aggregator/internal/sink"
)

func TestStdout_Write(t *testing.T) {
	entry := model.LogEntry{
		Service:         "auth-service",
		Level:           "info",
		Message:         "user login",
		ServerTimestamp: time.Now(),
		Fields:          map[string]string{"user_id": "42"},
	}

	// JSON format
	sJSON := sink.NewStdout("json")
	if err := sJSON.Write(context.Background(), entry); err != nil {
		t.Fatalf("stdout json write error: %v", err)
	}

	// Text format
	sText := sink.NewStdout("text")
	if err := sText.Write(context.Background(), entry); err != nil {
		t.Fatalf("stdout text write error: %v", err)
	}
}

func TestFile_Patterns(t *testing.T) {
	tmpDir := t.TempDir()

	entry := model.LogEntry{
		Service:         "billing-service",
		Level:           "error",
		Message:         "payment failed",
		ServerTimestamp: time.Now(),
	}
	today := time.Now().Format("2006-01-02")

	// 1. Flat pattern: logs/YYYY-MM-DD.jsonl
	fFlat, err := sink.NewFile(filepath.Join(tmpDir, "flat"), "flat")
	if err != nil {
		t.Fatal(err)
	}
	if err := fFlat.Write(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	_ = fFlat.Close()

	flatPath := filepath.Join(tmpDir, "flat", today+".jsonl")
	if _, err := os.Stat(flatPath); os.IsNotExist(err) {
		t.Fatalf("flat pattern file not found at %s", flatPath)
	}

	// 2. By-Service pattern: logs/{service}/YYYY-MM-DD.jsonl
	fService, err := sink.NewFile(filepath.Join(tmpDir, "service"), "by-service")
	if err != nil {
		t.Fatal(err)
	}
	if err := fService.Write(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	_ = fService.Close()

	servicePath := filepath.Join(tmpDir, "service", "billing-service", today+".jsonl")
	if _, err := os.Stat(servicePath); os.IsNotExist(err) {
		t.Fatalf("by-service pattern file not found at %s", servicePath)
	}

	// 3. By-Date pattern: logs/YYYY-MM-DD/{service}.jsonl
	fDate, err := sink.NewFile(filepath.Join(tmpDir, "date"), "by-date")
	if err != nil {
		t.Fatal(err)
	}
	if err := fDate.Write(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	_ = fDate.Close()

	datePath := filepath.Join(tmpDir, "date", today, "billing-service.jsonl")
	if _, err := os.Stat(datePath); os.IsNotExist(err) {
		t.Fatalf("by-date pattern file not found at %s", datePath)
	}
}

func TestWebhook_RetriesAndHeaders(t *testing.T) {
	var attempts int32
	var receivedAuthHeader string
	var receivedCustomHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		receivedAuthHeader = r.Header.Get("Authorization")
		receivedCustomHeader = r.Header.Get("X-Custom-Header")

		var payload model.LogEntry
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Первые 2 попытки возвращаем 500, на 3-й отдаём 200 OK
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	headers := map[string]string{
		"Authorization":   "Bearer test-token-123",
		"X-Custom-Header": "custom-val",
	}

	wh := sink.NewWebhook(srv.URL, 2*time.Second, 3, headers)
	entry := model.LogEntry{
		Service: "order-service",
		Level:   "warn",
		Message: "webhook retry test",
	}

	err := wh.Write(context.Background(), entry)
	if err != nil {
		t.Fatalf("webhook write should succeed after retries, got err: %v", err)
	}

	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
	}
	if receivedAuthHeader != "Bearer test-token-123" {
		t.Errorf("expected auth header, got %q", receivedAuthHeader)
	}
	if receivedCustomHeader != "custom-val" {
		t.Errorf("expected custom header, got %q", receivedCustomHeader)
	}
}

func TestWebhook_NonRetryable4xx(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	wh := sink.NewWebhook(srv.URL, 2*time.Second, 5, nil)
	err := wh.Write(context.Background(), model.LogEntry{Service: "auth", Level: "error", Message: "test"})
	if err == nil {
		t.Fatal("expected error on 403 Forbidden")
	}
	// Не должно быть ретраев при 403
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected exactly 1 attempt on 403, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestWebhook_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	wh := sink.NewWebhook(srv.URL, 2*time.Second, 5, nil)

	// Отменяем контекст сразу
	cancel()
	err := wh.Write(ctx, model.LogEntry{Service: "auth", Level: "error", Message: "test"})
	if err == nil {
		t.Fatal("expected context error")
	}
}
