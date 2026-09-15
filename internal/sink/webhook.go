package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// Webhook отправляет каждый лог HTTP POST-запросом на внешний URL с поддержкой повторных попыток и кастомных заголовков.
type Webhook struct {
	url        string
	client     *http.Client
	retryCount int
	headers    map[string]string
}

// NewWebhook создаёт webhook-синк с указанным URL, таймаутом, числом повторных попыток и заголовками.
func NewWebhook(url string, timeout time.Duration, retryCount int, headers map[string]string) *Webhook {
	return &Webhook{
		url: url,
		client: &http.Client{
			Timeout: timeout,
		},
		retryCount: retryCount,
		headers:    headers,
	}
}

func (w *Webhook) Name() string { return "webhook" }

// Write сериализует лог в JSON и отправляет POST на заданный URL с учётом retry_count и headers.
func (w *Webhook) Write(ctx context.Context, entry model.LogEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("сериализация лога: %w", err)
	}

	var lastErr error
	maxAttempts := 1 + w.retryCount

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*100) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("создание запроса: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		for k, v := range w.headers {
			req.Header.Set(k, v)
		}

		resp, err := w.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("отправка на webhook: %w", err)
			continue
		}

		// Считаем успехом любой 2xx ответ
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return nil
		}

		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		lastErr = fmt.Errorf("webhook вернул статус %d", resp.StatusCode)

		// 4xx ошибки (кроме 429 Too Many Requests) не имеет смысла ретраить
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return lastErr
		}
	}

	return lastErr
}
