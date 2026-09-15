package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/api"
	"github.com/stepankolchin/log-aggregator/internal/config"
	"github.com/stepankolchin/log-aggregator/internal/ingest"
	"github.com/stepankolchin/log-aggregator/internal/model"
	"github.com/stepankolchin/log-aggregator/internal/router"
	"github.com/stepankolchin/log-aggregator/internal/sink"
	"github.com/stepankolchin/log-aggregator/internal/storage"
	"github.com/stepankolchin/log-aggregator/internal/worker"
)

// pipeline связывает роутер и хранилище в один объект, реализующий worker.Processor.
// Воркер-пул не знает об их существовании по отдельности.
type pipeline struct {
	rtr  *router.Router
	stor *storage.Storage
}

func (p *pipeline) Process(ctx context.Context, entry model.LogEntry) {
	p.rtr.Process(ctx, entry) // маршрутизация → синки
	p.stor.Store(entry)       // сохранение в память для API
}

func main() {
	cfgPath := flag.String("config", "configs/config.yaml", "путь к файлу конфигурации")
	flag.Parse()

	// Временный логгер до загрузки конфига
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// ── Загрузка конфига ──────────────────────────────────────────────────────
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("не удалось загрузить конфиг", "err", err)
		os.Exit(1)
	}

	// Настройка уровня логирования aggregator'а в соответствии с конфигом
	var appLogLevel slog.Level
	switch strings.ToLower(cfg.App.LogLevel) {
	case "debug":
		appLogLevel = slog.LevelDebug
	case "warn", "warning":
		appLogLevel = slog.LevelWarn
	case "error":
		appLogLevel = slog.LevelError
	default:
		appLogLevel = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: appLogLevel,
	})))

	slog.Info("конфиг загружен", "path", *cfgPath, "log_level", cfg.App.LogLevel)

	// ── Инициализация синков ──────────────────────────────────────────────────
	var sinks []router.Sink

	if cfg.Sinks.Stdout.Enabled {
		sinks = append(sinks, sink.NewStdout(cfg.Sinks.Stdout.Format))
		slog.Info("sink: stdout включён", "format", cfg.Sinks.Stdout.Format)
	}

	var fileSink *sink.File
	if cfg.Sinks.File.Enabled {
		fileSink, err = sink.NewFile(cfg.Sinks.File.Dir, cfg.Sinks.File.Pattern)
		if err != nil {
			slog.Error("sink: ошибка инициализации file", "err", err)
			os.Exit(1)
		}
		sinks = append(sinks, fileSink)
		slog.Info("sink: file включён", "dir", cfg.Sinks.File.Dir, "pattern", cfg.Sinks.File.Pattern)
	}

	if cfg.Sinks.Webhook.Enabled {
		sinks = append(sinks, sink.NewWebhook(cfg.Sinks.Webhook.URL, cfg.Sinks.Webhook.Timeout, cfg.Sinks.Webhook.RetryCount, cfg.Sinks.Webhook.Headers))
		slog.Info("sink: webhook включён", "url", cfg.Sinks.Webhook.URL, "retry_count", cfg.Sinks.Webhook.RetryCount)
	}

	// ── Роутер ───────────────────────────────────────────────────────────────
	rtr, err := router.New(cfg.Routes, sinks)
	if err != nil {
		slog.Error("ошибка инициализации роутера", "err", err)
		os.Exit(1)
	}
	slog.Info("роутер инициализирован", "rules", len(cfg.Routes))

	// ── Хранилище ────────────────────────────────────────────────────────────
	stor, err := storage.New(cfg.Storage.MemoryLimit)
	if err != nil {
		slog.Error("ошибка инициализации хранилища", "err", err)
		os.Exit(1)
	}

	// При старте восстанавливаем данные сегодняшнего дня из файла файлового синка
	if cfg.Sinks.File.Enabled {
		if err := stor.LoadFromFile(cfg.Sinks.File.Dir); err != nil {
			slog.Warn("не удалось загрузить логи из файла", "err", err, "dir", cfg.Sinks.File.Dir)
		} else {
			st := stor.GetStats()
			slog.Info("данные хранилища восстановлены из файла", "dir", cfg.Sinks.File.Dir, "count", st.InMemory)
		}
	} else {
		slog.Warn("файловый синк отключен: хранилище работает только в оперативной памяти (логи не сохранятся при перезапуске)")
	}

	// ── Канал + воркер-пул ───────────────────────────────────────────────────
	queue := make(chan model.LogEntry, cfg.Worker.BufferSize)
	pipe := &pipeline{rtr: rtr, stor: stor}
	pool := worker.New(queue, pipe, cfg.Worker.PoolSize)

	// Контекст управляет жизненным циклом всех воркеров
	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)
	slog.Info("worker pool запущен", "workers", cfg.Worker.PoolSize, "buffer", cfg.Worker.BufferSize)

	// ── HTTP-сервер ───────────────────────────────────────────────────────────
	handler := ingest.NewHandler(queue)
	srv := api.New(cfg.Server, handler, stor, rtr, cfg.Sinks.File.Dir)
	srv.Start()

	// ── Ожидание сигнала завершения ───────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("получен сигнал, начинаем graceful shutdown", "signal", sig)

	// ── Graceful shutdown (порядок важен) ────────────────────────────────────
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()

	// 1. HTTP-сервер: прекращаем принимать новые запросы, ждём завершения текущих
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Error("ошибка при завершении HTTP-сервера", "err", err)
	}

	// 2. Закрываем входной канал очереди: новых запросов больше не будет
	close(queue)

	// 3. Воркеры: вычитывают оставшиеся в буфере канала записи до конца и завершаются
	pool.Wait()
	cancel()
	slog.Info("воркеры завершены")

	// 3. Файловый синк: сбрасываем буфер и закрываем файл
	if fileSink != nil {
		if err := fileSink.Close(); err != nil {
			slog.Error("ошибка закрытия файлового синка", "err", err)
		}
	}

	slog.Info("сервис успешно завершён")
}
