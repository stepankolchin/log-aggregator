package sink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// Stdout пишет каждый лог в стандартный поток вывода в формате JSON.
type Stdout struct{}

// NewStdout создаёт новый stdout-синк.
func NewStdout() *Stdout { return &Stdout{} }

func (s *Stdout) Name() string { return "stdout" }

func (s *Stdout) Write(_ context.Context, entry model.LogEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("сериализация лога: %w", err)
	}
	// Каждый лог — отдельная строка (JSON Lines)
	_, err = fmt.Fprintf(os.Stdout, "%s\n", data)
	return err
}
