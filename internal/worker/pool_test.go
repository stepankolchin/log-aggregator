package worker_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
	"github.com/stepankolchin/log-aggregator/internal/worker"
)

type mockProcessor struct {
	processed atomic.Int64
}

func (m *mockProcessor) Process(_ context.Context, _ model.LogEntry) {
	m.processed.Add(1)
	time.Sleep(1 * time.Millisecond) // имитация обработки
}

func TestPool_DrainsQueueOnShutdown(t *testing.T) {
	const totalLogs = 100
	const bufferSize = 100
	const workers = 4

	queue := make(chan model.LogEntry, bufferSize)
	proc := &mockProcessor{}
	pool := worker.New(queue, proc, workers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool.Start(ctx)

	// Заполняем буфер очереди
	for i := 0; i < totalLogs; i++ {
		queue <- model.LogEntry{
			Message: "test log message",
		}
	}

	// Закрываем очередь (как при штатном graceful shutdown)
	close(queue)

	// Дожидаемся завершения воркеров
	pool.Wait()

	// Проверяем, что абсолютно ВСЕ логи были обработаны и ни один не потерян
	if got := proc.processed.Load(); got != totalLogs {
		t.Fatalf("обработано %d логов из %d, часть логов потеряна при остановке!", got, totalLogs)
	}
}

func TestPool_ContextCancelStopsWorkers(t *testing.T) {
	queue := make(chan model.LogEntry, 10)
	proc := &mockProcessor{}
	pool := worker.New(queue, proc, 2)

	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)

	// Отменяем контекст без закрытия канала
	cancel()

	done := make(chan struct{})
	go func() {
		pool.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Успешно остановлен по контексту
	case <-time.After(1 * time.Second):
		t.Fatal("воркеры не остановились при отмене контекста")
	}
}
