package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// Webhook отправляет каждый лог HTTP POST-запросом на внешний URL.
type Webhook struct {
	url    string
	client *http.Client
}

// NewWebhook создаёт webhook-синк с указанным URL и таймаутом.
func NewWebhook(url string, timeout time.Duration) *Webhook {
	return &Webhook{
		url: url,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (w *Webhook) Name() string { return "webhook" }

// Write сериализует лог в JSON и отправляет POST на заданный URL.
func (w *Webhook) Write(ctx context.Context, entry model.LogEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("сериализация лога: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("создание запроса: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("отправка на webhook: %w", err)
	}
	defer resp.Body.Close()

	// Считаем успехом любой 2xx ответ
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook вернул статус %d", resp.StatusCode)
	}
	return nil
}
