package sink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// Stdout пишет каждый лог в стандартный поток вывода в формате JSON или Text.
type Stdout struct {
	format string
}

// NewStdout создаёт новый stdout-синк с заданным форматом (json или text).
func NewStdout(format string) *Stdout {
	if format == "" {
		format = "json"
	}
	return &Stdout{format: strings.ToLower(format)}
}

func (s *Stdout) Name() string { return "stdout" }

func (s *Stdout) Write(_ context.Context, entry model.LogEntry) error {
	if s.format == "text" {
		t := entry.EffectiveTime().UTC().Format(time.RFC3339)
		level := strings.ToUpper(entry.Level)
		if level == "" {
			level = "UNKNOWN"
		}
		service := entry.Service
		if service == "" {
			service = "unknown"
		}

		var line string
		if len(entry.Fields) > 0 {
			fieldsBytes, _ := json.Marshal(entry.Fields)
			line = fmt.Sprintf("[%s] [%s] %s: %s %s\n", t, level, service, entry.Message, string(fieldsBytes))
		} else {
			line = fmt.Sprintf("[%s] [%s] %s: %s\n", t, level, service, entry.Message)
		}
		_, err := fmt.Fprint(os.Stdout, line)
		return err
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("сериализация лога: %w", err)
	}
	// Каждый лог — отдельная строка (JSON Lines)
	_, err = fmt.Fprintf(os.Stdout, "%s\n", data)
	return err
}
