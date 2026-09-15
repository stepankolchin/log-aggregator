package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
	"github.com/stepankolchin/log-aggregator/internal/sink"
	"github.com/stepankolchin/log-aggregator/internal/storage"
)

func TestQueryArchive_PaginationAndFilters(t *testing.T) {
	tempDir := t.TempDir()
	fileSink, err := sink.NewFile(tempDir, "flat")
	if err != nil {
		t.Fatalf("sink.NewFile: %v", err)
	}

	ctx := context.Background()

	// Записываем 15 логов
	baseTime := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 15; i++ {
		svc := "auth"
		if i%2 == 0 {
			svc = "order"
		}
		lvl := "info"
		if i%3 == 0 {
			lvl = "error"
		}
		_ = fileSink.Write(ctx, model.LogEntry{
			Service:   svc,
			Level:     lvl,
			Message:   fmt.Sprintf("msg-%02d", i),
			Timestamp: baseTime.Add(time.Duration(i) * time.Minute),
		})
	}
	_ = fileSink.Close()

	// 1. Первая страница (limit=5, offset=0) -> должно вернуть 5 записей, hasMore=true
	page1, hasMore, err := storage.QueryArchive(tempDir, storage.QueryParams{Limit: 5, Offset: 0})
	if err != nil {
		t.Fatalf("QueryArchive page1: %v", err)
	}
	if len(page1) != 5 {
		t.Errorf("page1 len = %d, ожидалось 5", len(page1))
	}
	if !hasMore {
		t.Errorf("page1 hasMore = false, ожидалось true")
	}
	// Самый свежий лог (msg-15) должен быть первым
	if page1[0].Message != "msg-15" {
		t.Errorf("page1[0] = %s, ожидалось msg-15", page1[0].Message)
	}

	// 2. Вторая страница (limit=5, offset=5)
	page2, hasMore, err := storage.QueryArchive(tempDir, storage.QueryParams{Limit: 5, Offset: 5})
	if err != nil {
		t.Fatalf("QueryArchive page2: %v", err)
	}
	if len(page2) != 5 {
		t.Errorf("page2 len = %d, ожидалось 5", len(page2))
	}
	if !hasMore {
		t.Errorf("page2 hasMore = false, ожидалось true")
	}
	if page2[0].Message != "msg-10" {
		t.Errorf("page2[0] = %s, ожидалось msg-10", page2[0].Message)
	}

	// 3. Последняя страница (limit=5, offset=10) -> ровно 5 записей, hasMore=false
	page3, hasMore, err := storage.QueryArchive(tempDir, storage.QueryParams{Limit: 5, Offset: 10})
	if err != nil {
		t.Fatalf("QueryArchive page3: %v", err)
	}
	if len(page3) != 5 {
		t.Errorf("page3 len = %d, ожидалось 5", len(page3))
	}
	if hasMore {
		t.Errorf("page3 hasMore = true, ожидалось false")
	}
	if page3[4].Message != "msg-01" {
		t.Errorf("page3[4] = %s, ожидалось msg-01", page3[4].Message)
	}

	// 4. Фильтр по сервису (order)
	orderLogs, _, err := storage.QueryArchive(tempDir, storage.QueryParams{Service: "order", Limit: 50})
	if err != nil {
		t.Fatalf("QueryArchive order: %v", err)
	}
	// Четные: 2, 4, 6, 8, 10, 12, 14 -> 7 штук
	if len(orderLogs) != 7 {
		t.Errorf("order logs count = %d, ожидалось 7", len(orderLogs))
	}

	// 5. Фильтр по времени (From / To)
	from := baseTime.Add(5 * time.Minute)
	to := baseTime.Add(10 * time.Minute)
	timeLogs, _, err := storage.QueryArchive(tempDir, storage.QueryParams{From: from, To: to, Limit: 50})
	if err != nil {
		t.Fatalf("QueryArchive time: %v", err)
	}
	// Логи с 5 по 10 включительно -> 6 штук
	if len(timeLogs) != 6 {
		t.Errorf("time logs count = %d, ожидалось 6", len(timeLogs))
	}
}
