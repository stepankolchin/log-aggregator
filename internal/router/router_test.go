package router

import (
	"context"
	"testing"

	"github.com/stepankolchin/log-aggregator/internal/config"
	"github.com/stepankolchin/log-aggregator/internal/model"
)

// mockSink — тестовая реализация интерфейса Sink.
// Записывает все полученные логи в срез для последующей проверки.
type mockSink struct {
	sinkName string
	received []model.LogEntry
}

func (m *mockSink) Name() string { return m.sinkName }
func (m *mockSink) Write(_ context.Context, e model.LogEntry) error {
	m.received = append(m.received, e)
	return nil
}

// makeEntry — вспомогательная функция создания записи лога.
func makeEntry(service, level, message string) model.LogEntry {
	return model.LogEntry{Service: service, Level: level, Message: message}
}

// TestRouter_FanOut проверяет что лог направляется ВО ВСЕ совпавшие правила.
func TestRouter_FanOut(t *testing.T) {
	sinkA := &mockSink{sinkName: "sinkA"}
	sinkB := &mockSink{sinkName: "sinkB"}

	rtr, err := New([]config.RouteConfig{
		{Name: "by-level", Match: config.MatchConfig{Level: "error"}, Sinks: []string{"sinkA"}},
		{Name: "by-service", Match: config.MatchConfig{Service: "auth-service"}, Sinks: []string{"sinkB"}},
	}, []Sink{sinkA, sinkB})
	if err != nil {
		t.Fatal(err)
	}

	// Лог auth-service + error → совпадает с ОБОИМИ правилами
	rtr.Process(context.Background(), makeEntry("auth-service", "error", "auth failed"))

	if len(sinkA.received) != 1 {
		t.Errorf("sinkA: ожидалась 1 запись, получено %d", len(sinkA.received))
	}
	if len(sinkB.received) != 1 {
		t.Errorf("sinkB: ожидалась 1 запись, получено %d", len(sinkB.received))
	}
}

// TestRouter_Default проверяет что default-правило срабатывает ТОЛЬКО если ничего не совпало.
func TestRouter_Default(t *testing.T) {
	specificSink := &mockSink{sinkName: "specific"}
	defaultSink := &mockSink{sinkName: "default-sink"}

	rtr, err := New([]config.RouteConfig{
		{Name: "auth-only", Match: config.MatchConfig{Service: "auth-service"}, Sinks: []string{"specific"}},
		{Name: "default", Match: config.MatchConfig{}, Sinks: []string{"default-sink"}},
	}, []Sink{specificSink, defaultSink})
	if err != nil {
		t.Fatal(err)
	}

	// order-service не совпадает с "auth-only" → должен попасть в default
	rtr.Process(context.Background(), makeEntry("order-service", "info", "order created"))

	if len(defaultSink.received) != 1 {
		t.Errorf("default sink: ожидалась 1 запись, получено %d", len(defaultSink.received))
	}
	if len(specificSink.received) != 0 {
		t.Errorf("specific sink не должен получать этот лог, получено %d", len(specificSink.received))
	}
}

// TestRouter_DefaultNotFiredWhenMatched проверяет что default НЕ срабатывает если есть совпадение.
func TestRouter_DefaultNotFiredWhenMatched(t *testing.T) {
	matchedSink := &mockSink{sinkName: "matched"}
	defaultSink := &mockSink{sinkName: "default-sink"}

	rtr, err := New([]config.RouteConfig{
		{Name: "auth-only", Match: config.MatchConfig{Service: "auth-service"}, Sinks: []string{"matched"}},
		{Name: "default", Match: config.MatchConfig{}, Sinks: []string{"default-sink"}},
	}, []Sink{matchedSink, defaultSink})
	if err != nil {
		t.Fatal(err)
	}

	rtr.Process(context.Background(), makeEntry("auth-service", "info", "logged in"))

	if len(matchedSink.received) != 1 {
		t.Errorf("matched sink: ожидалась 1 запись, получено %d", len(matchedSink.received))
	}
	if len(defaultSink.received) != 0 {
		t.Errorf("default не должен срабатывать при совпадении, получено %d", len(defaultSink.received))
	}
}

// TestRouter_LevelsFilter проверяет фильтрацию по списку уровней.
func TestRouter_LevelsFilter(t *testing.T) {
	sink := &mockSink{sinkName: "sink"}
	rtr, err := New([]config.RouteConfig{
		{Name: "high-prio", Match: config.MatchConfig{Levels: []string{"warn", "error"}}, Sinks: []string{"sink"}},
	}, []Sink{sink})
	if err != nil {
		t.Fatal(err)
	}

	rtr.Process(context.Background(), makeEntry("svc", "info", "usual log"))
	rtr.Process(context.Background(), makeEntry("svc", "debug", "verbose log"))
	if len(sink.received) != 0 {
		t.Errorf("info/debug не должны попадать в правило warn+error, получено %d", len(sink.received))
	}

	rtr.Process(context.Background(), makeEntry("svc", "warn", "slow response"))
	rtr.Process(context.Background(), makeEntry("svc", "error", "db failure"))
	if len(sink.received) != 2 {
		t.Errorf("warn и error должны попасть в правило, ожидалось 2, получено %d", len(sink.received))
	}
}

// TestRouter_MessageRegex проверяет фильтрацию по регулярному выражению в поле message.
func TestRouter_MessageRegex(t *testing.T) {
	sink := &mockSink{sinkName: "sink"}
	rtr, err := New([]config.RouteConfig{
		{Name: "slow", Match: config.MatchConfig{MessageRegex: "slow|timeout"}, Sinks: []string{"sink"}},
	}, []Sink{sink})
	if err != nil {
		t.Fatal(err)
	}

	rtr.Process(context.Background(), makeEntry("svc", "warn", "order processing slow"))
	rtr.Process(context.Background(), makeEntry("svc", "info", "order created"))
	rtr.Process(context.Background(), makeEntry("svc", "error", "payment timeout exceeded"))

	if len(sink.received) != 2 {
		t.Errorf("ожидалось 2 совпадения по regex, получено %d", len(sink.received))
	}
}

// TestRouter_Discard проверяет что sink "discard" не записывает лог никуда.
func TestRouter_Discard(t *testing.T) {
	realSink := &mockSink{sinkName: "real"}
	rtr, err := New([]config.RouteConfig{
		{Name: "drop-debug", Match: config.MatchConfig{Level: "debug"}, Sinks: []string{"discard"}},
		{Name: "default", Match: config.MatchConfig{}, Sinks: []string{"real"}},
	}, []Sink{realSink})
	if err != nil {
		t.Fatal(err)
	}

	// debug совпало с drop-debug → matched=true → default не срабатывает
	rtr.Process(context.Background(), makeEntry("svc", "debug", "verbose internal log"))

	if len(realSink.received) != 0 {
		t.Errorf("после discard real sink не должен получать лог, получено %d", len(realSink.received))
	}
}

// TestRouter_InvalidRegex проверяет что невалидный regex возвращает ошибку при создании.
func TestRouter_InvalidRegex(t *testing.T) {
	_, err := New([]config.RouteConfig{
		{Name: "bad", Match: config.MatchConfig{MessageRegex: "["}, Sinks: []string{"sink"}},
	}, []Sink{&mockSink{sinkName: "sink"}})
	if err == nil {
		t.Error("ожидалась ошибка при невалидном regex")
	}
}

// TestRouter_UnregisteredSink проверяет, что ссылка на незарегистрированный sink возвращает ошибку.
func TestRouter_UnregisteredSink(t *testing.T) {
	_, err := New([]config.RouteConfig{
		{Name: "rule1", Sinks: []string{"unknown_sink"}},
	}, nil)
	if err == nil {
		t.Error("ожидалась ошибка при ссылке на незарегистрированный sink")
	}
}

