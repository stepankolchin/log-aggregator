package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config — корневая структура всего конфига приложения.
type Config struct {
	App     AppConfig     `yaml:"app"`
	Server  ServerConfig  `yaml:"server"`
	Worker  WorkerConfig  `yaml:"worker"`
	Storage StorageConfig `yaml:"storage"`
	Sinks   SinksConfig   `yaml:"sinks"`
	Routes  []RouteConfig `yaml:"routes"`
}

// AppConfig — настройки самого приложения.
type AppConfig struct {
	// LogLevel — уровень логирования aggregator'а (не логов микросервисов): debug, info, warn, error.
	LogLevel string `yaml:"log_level"`
}

// ServerConfig — параметры HTTP-сервера.
type ServerConfig struct {
	Port              int           `yaml:"port"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	DefaultQueryLimit int           `yaml:"default_query_limit"`
	MaxQueryLimit     int           `yaml:"max_query_limit"`
}

// WorkerConfig — параметры пула воркеров.
type WorkerConfig struct {
	PoolSize   int `yaml:"pool_size"`
	BufferSize int `yaml:"buffer_size"`
}

// StorageConfig — параметры хранилища.
type StorageConfig struct {
	// MemoryLimit — максимальное число логов в оперативной памяти для API (10..1000000).
	MemoryLimit int `yaml:"memory_limit"`
}

// SinksConfig — настройки всех назначений.
type SinksConfig struct {
	Stdout  StdoutSinkConfig  `yaml:"stdout"`
	File    FileSinkConfig    `yaml:"file"`
	Webhook WebhookSinkConfig `yaml:"webhook"`
}

// StdoutSinkConfig — вывод в стандартный поток.
type StdoutSinkConfig struct {
	Enabled bool `yaml:"enabled"`
	// Format — формат вывода: "json" (по умолчанию) или "text" (человекочитаемый).
	Format string `yaml:"format"`
}

// FileSinkConfig — запись в файл с ежедневной ротацией.
type FileSinkConfig struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"`
	// Pattern определяет структуру директорий:
	//   flat       — logs/YYYY-MM-DD.jsonl  (все сервисы в одном файле)
	//   by-service — logs/{service}/YYYY-MM-DD.jsonl  (папка на сервис)
	//   by-date    — logs/YYYY-MM-DD/{service}.jsonl  (папка на дату)
	Pattern string `yaml:"pattern"`
}

// WebhookSinkConfig — отправка POST-запроса на внешний URL.
type WebhookSinkConfig struct {
	Enabled bool          `yaml:"enabled"`
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
	// RetryCount — число повторных попыток при ошибке отправки (0 = без повторов).
	RetryCount int `yaml:"retry_count"`
	// Headers — произвольные HTTP-заголовки (например для авторизации).
	Headers map[string]string `yaml:"headers"`
}

// RouteConfig — одно правило маршрутизации.
type RouteConfig struct {
	Name  string      `yaml:"name"`
	Match MatchConfig `yaml:"match"`
	// Sinks — список имён назначений: stdout, file, webhook, discard.
	Sinks []string `yaml:"sinks"`
}

// MatchConfig — условия совпадения для правила.
type MatchConfig struct {
	// Service — точное имя сервиса (пусто = любой).
	Service string `yaml:"service"`
	// Level — точный уровень (пусто = любой).
	Level string `yaml:"level"`
	// Levels — список уровней (альтернатива Level).
	Levels []string `yaml:"levels"`
	// MessageRegex — регулярное выражение для поля message (пусто = любое).
	MessageRegex string `yaml:"message_regex"`
}

// Load читает конфигурацию из YAML-файла по указанному пути и выполняет валидацию.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение конфига %q: %w", path, err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("разбор конфига: %w", err)
	}

	applyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("ошибка валидации конфига: %w", err)
	}
	return cfg, nil
}

// Validate проверяет корректность параметров конфигурации.
func (c *Config) Validate() error {
	if c.Storage.MemoryLimit < 10 || c.Storage.MemoryLimit > 1_000_000 {
		return fmt.Errorf("недопустимый storage.memory_limit=%d: должен быть в диапазоне от 10 до 1 000 000", c.Storage.MemoryLimit)
	}
	if c.Worker.PoolSize < 1 {
		return fmt.Errorf("worker.pool_size должен быть >= 1 (указано: %d)", c.Worker.PoolSize)
	}
	if c.Worker.BufferSize < 1 {
		return fmt.Errorf("worker.buffer_size должен быть >= 1 (указано: %d)", c.Worker.BufferSize)
	}
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port должен быть от 1 до 65535 (указано: %d)", c.Server.Port)
	}
	if c.Server.DefaultQueryLimit < 1 {
		return fmt.Errorf("server.default_query_limit должен быть >= 1 (указано: %d)", c.Server.DefaultQueryLimit)
	}
	if c.Server.MaxQueryLimit < 1 {
		return fmt.Errorf("server.max_query_limit должен быть >= 1 (указано: %d)", c.Server.MaxQueryLimit)
	}
	if c.Server.DefaultQueryLimit > c.Server.MaxQueryLimit {
		return fmt.Errorf("server.default_query_limit (%d) не может быть больше server.max_query_limit (%d)", c.Server.DefaultQueryLimit, c.Server.MaxQueryLimit)
	}
	return nil
}

// applyDefaults заполняет нулевые значения разумными умолчаниями.
func applyDefaults(cfg *Config) {
	if cfg.App.LogLevel == "" {
		cfg.App.LogLevel = "info"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 10 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 10 * time.Second
	}
	if cfg.Server.DefaultQueryLimit == 0 {
		cfg.Server.DefaultQueryLimit = 100
	}
	if cfg.Server.MaxQueryLimit == 0 {
		cfg.Server.MaxQueryLimit = 1000
	}
	if cfg.Worker.PoolSize == 0 {
		cfg.Worker.PoolSize = 4
	}
	if cfg.Worker.BufferSize == 0 {
		cfg.Worker.BufferSize = 1024
	}
	if cfg.Storage.MemoryLimit == 0 {
		cfg.Storage.MemoryLimit = 5000
	}
	if cfg.Sinks.File.Dir == "" {
		cfg.Sinks.File.Dir = "./logs"
	}
	if cfg.Sinks.File.Pattern == "" {
		cfg.Sinks.File.Pattern = "by-service"
	}
	if cfg.Sinks.Stdout.Format == "" {
		cfg.Sinks.Stdout.Format = "json"
	}
	if cfg.Sinks.Webhook.Timeout == 0 {
		cfg.Sinks.Webhook.Timeout = 5 * time.Second
	}
}
