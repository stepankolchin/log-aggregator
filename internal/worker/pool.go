package worker

import (
	"context"
	"sync"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// Processor — интерфейс, который реализует router.
// Worker pool не знает деталей маршрутизации — только вызывает Process.
type Processor interface {
	Process(ctx context.Context, entry model.LogEntry)
}

// Pool — пул воркеров, читающих логи из канала и передающих их процессору.
type Pool struct {
	queue     <-chan model.LogEntry // канал только для чтения
	processor Processor
	size      int
	wg        sync.WaitGroup
}

// New создаёт пул с указанным числом воркеров.
func New(queue <-chan model.LogEntry, processor Processor, size int) *Pool {
	return &Pool{
		queue:     queue,
		processor: processor,
		size:      size,
	}
}

// Start запускает воркеров в фоновых горутинах.
// Завершение происходит при закрытии ctx или закрытии канала queue.
func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.size; i++ {
		p.wg.Add(1)
		go p.work(ctx)
	}
}

// Wait блокируется до завершения всех воркеров.
// Вызывать после отмены ctx.
func (p *Pool) Wait() {
	p.wg.Wait()
}

// work — тело одного воркера: читает из канала, пока ctx не отменён.
func (p *Pool) work(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			// Контекст отменён — завершаем воркер
			return
		case entry, ok := <-p.queue:
			if !ok {
				// Канал закрыт — завершаем воркер
				return
			}
			p.processor.Process(ctx, entry)
		}
	}
}
