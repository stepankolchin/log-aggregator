package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stepankolchin/log-aggregator/internal/config"
)

func TestConfig_Validate(t *testing.T) {
	validConfig := func() *config.Config {
		return &config.Config{
			App: config.AppConfig{
				LogLevel: "info",
			},
			Server: config.ServerConfig{
				Port:              8080,
				DefaultQueryLimit: 100,
				MaxQueryLimit:     1000,
			},
			Worker: config.WorkerConfig{
				PoolSize:   4,
				BufferSize: 1024,
			},
			Storage: config.StorageConfig{
				MemoryLimit: 5000,
			},
			Sinks: config.SinksConfig{
				Stdout: config.StdoutSinkConfig{
					Enabled: true,
					Format:  "json",
				},
				File: config.FileSinkConfig{
					Enabled: true,
					Dir:     "./logs",
					Pattern: "flat",
				},
				Webhook: config.WebhookSinkConfig{
					Enabled:    false,
					URL:        "http://localhost:9000/webhook",
					RetryCount: 3,
				},
			},
			Routes: []config.RouteConfig{
				{
					Name:  "default",
					Sinks: []string{"stdout", "file"},
				},
			},
		}
	}

	tests := []struct {
		name        string
		modify      func(c *config.Config)
		expectError bool
	}{
		{
			name:        "дефолтный конфиг валиден",
			modify:      func(c *config.Config) {},
			expectError: false,
		},
		{
			name: "недопустимый log_level -> ошибка",
			modify: func(c *config.Config) {
				c.App.LogLevel = "verbose"
			},
			expectError: true,
		},
		{
			name: "memory_limit меньше 10 -> ошибка",
			modify: func(c *config.Config) {
				c.Storage.MemoryLimit = 5
			},
			expectError: true,
		},
		{
			name: "memory_limit больше 100 000 -> ошибка",
			modify: func(c *config.Config) {
				c.Storage.MemoryLimit = 200_000
			},
			expectError: true,
		},
		{
			name: "worker.pool_size = 0 -> ошибка",
			modify: func(c *config.Config) {
				c.Worker.PoolSize = 0
			},
			expectError: true,
		},
		{
			name: "worker.pool_size > 256 -> ошибка",
			modify: func(c *config.Config) {
				c.Worker.PoolSize = 300
			},
			expectError: true,
		},
		{
			name: "worker.buffer_size = 0 -> ошибка",
			modify: func(c *config.Config) {
				c.Worker.BufferSize = 0
			},
			expectError: true,
		},
		{
			name: "worker.buffer_size > 1 000 000 -> ошибка",
			modify: func(c *config.Config) {
				c.Worker.BufferSize = 2_000_000
			},
			expectError: true,
		},
		{
			name: "server.port = 0 -> ошибка",
			modify: func(c *config.Config) {
				c.Server.Port = 0
			},
			expectError: true,
		},
		{
			name: "server.port > 65535 -> ошибка",
			modify: func(c *config.Config) {
				c.Server.Port = 70000
			},
			expectError: true,
		},
		{
			name: "server.default_query_limit > server.max_query_limit -> ошибка",
			modify: func(c *config.Config) {
				c.Server.DefaultQueryLimit = 2000
				c.Server.MaxQueryLimit = 1000
			},
			expectError: true,
		},
		{
			name: "server.max_query_limit < 1 -> ошибка",
			modify: func(c *config.Config) {
				c.Server.MaxQueryLimit = 0
			},
			expectError: true,
		},
		{
			name: "stdout.format недопустим -> ошибка",
			modify: func(c *config.Config) {
				c.Sinks.Stdout.Format = "xml"
			},
			expectError: true,
		},
		{
			name: "file.pattern недопустим -> ошибка",
			modify: func(c *config.Config) {
				c.Sinks.File.Pattern = "custom-unknown"
			},
			expectError: true,
		},
		{
			name: "webhook enabled с пустым url -> ошибка",
			modify: func(c *config.Config) {
				c.Sinks.Webhook.Enabled = true
				c.Sinks.Webhook.URL = ""
			},
			expectError: true,
		},
		{
			name: "webhook enabled с невалидным url -> ошибка",
			modify: func(c *config.Config) {
				c.Sinks.Webhook.Enabled = true
				c.Sinks.Webhook.URL = "not-a-url"
			},
			expectError: true,
		},
		{
			name: "webhook enabled с отрицательным retry_count -> ошибка",
			modify: func(c *config.Config) {
				c.Sinks.Webhook.Enabled = true
				c.Sinks.Webhook.URL = "http://localhost:9000"
				c.Sinks.Webhook.RetryCount = -1
			},
			expectError: true,
		},
		{
			name: "роут ссылается на выключенный webhook -> ошибка",
			modify: func(c *config.Config) {
				c.Sinks.Webhook.Enabled = false
				c.Routes = []config.RouteConfig{
					{Name: "auth-rule", Sinks: []string{"file", "webhook"}},
				}
			},
			expectError: true,
		},
		{
			name: "роут ссылается на неизвестный sink -> ошибка",
			modify: func(c *config.Config) {
				c.Routes = []config.RouteConfig{
					{Name: "kafka-rule", Sinks: []string{"kafka"}},
				}
			},
			expectError: true,
		},
		{
			name: "роут с discard -> валидно",
			modify: func(c *config.Config) {
				c.Routes = []config.RouteConfig{
					{Name: "drop-rule", Sinks: []string{"discard"}},
				}
			},
			expectError: false,
		},
		{
			name: "роут без имени -> ошибка",
			modify: func(c *config.Config) {
				c.Routes = []config.RouteConfig{
					{Name: "", Sinks: []string{"stdout"}},
				}
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.modify(cfg)
			err := cfg.Validate()
			if tt.expectError && err == nil {
				t.Errorf("ожидалась ошибка валидации, получен nil")
			} else if !tt.expectError && err != nil {
				t.Errorf("неожиданная ошибка валидации: %v", err)
			}
		})
	}
}

func TestConfig_LoadInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	invalidFile := filepath.Join(tmpDir, "invalid.yaml")
	_ = os.WriteFile(invalidFile, []byte("storage:\n  memory_limit: 2\n"), 0o644)

	_, err := config.Load(invalidFile)
	if err == nil {
		t.Fatal("Load должен был вернуть ошибку валидации для memory_limit=2")
	}
}
