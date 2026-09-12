package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// logPayload — тело запроса, отправляемого в aggregator.
type logPayload struct {
	Timestamp time.Time         `json:"timestamp"`
	Service   string            `json:"service"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Host      string            `json:"host,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// sender отправляет один лог в aggregator по HTTP.
type sender struct {
	endpoint string
	service  string
	host     string
	client   *http.Client
}

func newSender(endpoint, service string) *sender {
	hostname, _ := os.Hostname()
	return &sender{
		endpoint: endpoint + "/api/v1/logs",
		service:  service,
		host:     hostname,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// send отправляет лог и логирует результат.
func (s *sender) send(ctx context.Context, level, message string, traceID string, fields map[string]string) {
	p := logPayload{
		Timestamp: time.Now().UTC(),
		Service:   s.service,
		Level:     level,
		Message:   message,
		Host:      s.host,
		TraceID:   traceID,
		Fields:    fields,
	}

	data, _ := json.Marshal(p)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(data))
	if err != nil {
		slog.Error("ошибка создания запроса", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		slog.Error("ошибка отправки лога", "service", s.service, "err", err)
		return
	}
	defer resp.Body.Close()

	slog.Debug("лог отправлен", "service", s.service, "level", level, "status", resp.StatusCode)
}

// traceID генерирует случайный идентификатор трассировки.
func traceID() string {
	return fmt.Sprintf("%08x-%04x", rand.Uint32(), rand.Uint32()>>16)
}

// ── Сценарии demo-сервисов ────────────────────────────────────────────────────

// runAuthService имитирует сервис аутентификации:
// - часто: успешные входы
// - иногда: предупреждения о подозрительной активности
// - редко: ошибки аутентификации
func runAuthService(ctx context.Context, s *sender) {
	users := []string{"alice", "bob", "charlie", "diana", "eve"}
	ips := []string{"192.168.1.10", "10.0.0.5", "172.16.0.3", "203.0.113.42"}

	tickInfo := time.NewTicker(3 * time.Second)
	tickWarn := time.NewTicker(15 * time.Second)
	tickErr := time.NewTicker(45 * time.Second)
	defer tickInfo.Stop()
	defer tickWarn.Stop()
	defer tickErr.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tickInfo.C:
			user := users[rand.Intn(len(users))]
			s.send(ctx, "info", fmt.Sprintf("user logged in: %s", user), "",
				map[string]string{"user_id": user, "ip": ips[rand.Intn(len(ips))]})

		case <-tickWarn.C:
			user := users[rand.Intn(len(users))]
			s.send(ctx, "warn", "too many failed attempts, account temporarily locked",
				traceID(), map[string]string{"user_id": user, "attempts": "5"})

		case <-tickErr.C:
			user := users[rand.Intn(len(users))]
			s.send(ctx, "error", "authentication failed: invalid token signature",
				traceID(), map[string]string{"user_id": user, "ip": ips[rand.Intn(len(ips))]})
		}
	}
}

// runOrderService имитирует сервис заказов:
// - часто: создание и обработка заказов
// - иногда: медленная обработка
// - редко: сбой при оплате
func runOrderService(ctx context.Context, s *sender) {
	products := []string{"laptop", "keyboard", "monitor", "headset", "webcam"}
	amounts := []string{"999.99", "49.00", "299.00", "129.50", "79.90"}

	tickInfo := time.NewTicker(5 * time.Second)
	tickWarn := time.NewTicker(20 * time.Second)
	tickErr := time.NewTicker(60 * time.Second)
	defer tickInfo.Stop()
	defer tickWarn.Stop()
	defer tickErr.Stop()

	orderID := 1000
	for {
		select {
		case <-ctx.Done():
			return
		case <-tickInfo.C:
			orderID++
			product := products[rand.Intn(len(products))]
			s.send(ctx, "info", fmt.Sprintf("order #%d created: %s", orderID, product), "",
				map[string]string{"order_id": fmt.Sprint(orderID), "product": product,
					"amount": amounts[rand.Intn(len(amounts))]})

		case <-tickWarn.C:
			s.send(ctx, "warn", "order processing slow: queue depth high",
				traceID(), map[string]string{"queue_depth": fmt.Sprint(rand.Intn(50) + 10),
					"latency_ms": fmt.Sprint(rand.Intn(3000) + 1000)})

		case <-tickErr.C:
			orderID++
			s.send(ctx, "error", fmt.Sprintf("order #%d failed: payment timeout after 30s", orderID),
				traceID(), map[string]string{"order_id": fmt.Sprint(orderID), "retry": "3"})
		}
	}
}

// runPaymentService имитирует платёжный сервис:
// - часто: инициализация и подтверждение платежей
// - иногда: ошибки платёжного шлюза
func runPaymentService(ctx context.Context, s *sender) {
	gateways := []string{"stripe", "paypal", "yookassa"}
	currencies := []string{"USD", "EUR", "RUB"}

	tickDebug := time.NewTicker(4 * time.Second)
	tickInfo := time.NewTicker(10 * time.Second)
	tickErr := time.NewTicker(30 * time.Second)
	defer tickDebug.Stop()
	defer tickInfo.Stop()
	defer tickErr.Stop()

	txID := 5000
	for {
		select {
		case <-ctx.Done():
			return
		case <-tickDebug.C:
			txID++
			gw := gateways[rand.Intn(len(gateways))]
			cur := currencies[rand.Intn(len(currencies))]
			s.send(ctx, "debug", fmt.Sprintf("payment tx#%d initiated via %s", txID, gw), "",
				map[string]string{"tx_id": fmt.Sprint(txID), "gateway": gw, "currency": cur})

		case <-tickInfo.C:
			s.send(ctx, "info", fmt.Sprintf("payment tx#%d confirmed", txID), "",
				map[string]string{"tx_id": fmt.Sprint(txID),
					"amount": fmt.Sprintf("%.2f", rand.Float64()*500+10)})

		case <-tickErr.C:
			gw := gateways[rand.Intn(len(gateways))]
			s.send(ctx, "error", fmt.Sprintf("payment gateway error: %s connection refused", gw),
				traceID(), map[string]string{"gateway": gw,
					"code": fmt.Sprint(rand.Intn(3) + 500)})
		}
	}
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	serviceName := flag.String("service", "", "имя сервиса: auth-service, order-service, payment-service")
	endpoint := flag.String("aggregator", "http://localhost:8080", "адрес aggregator")
	flag.Parse()

	// Также проверяем переменную окружения (удобно для docker-compose)
	if *serviceName == "" {
		*serviceName = os.Getenv("SERVICE_NAME")
	}
	if *endpoint == "" || *endpoint == "http://localhost:8080" {
		if env := os.Getenv("AGGREGATOR_URL"); env != "" {
			*endpoint = env
		}
	}

	if *serviceName == "" {
		fmt.Fprintln(os.Stderr, "укажите имя сервиса: -service auth-service|order-service|payment-service")
		fmt.Fprintln(os.Stderr, "или установите переменную окружения SERVICE_NAME")
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	slog.Info("demo-сервис запущен", "service", *serviceName, "aggregator", *endpoint)

	// Небольшая задержка — даём aggregator время запуститься
	time.Sleep(2 * time.Second)

	s := newSender(*endpoint, *serviceName)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch *serviceName {
	case "auth-service":
		runAuthService(ctx, s)
	case "order-service":
		runOrderService(ctx, s)
	case "payment-service":
		runPaymentService(ctx, s)
	default:
		slog.Error("неизвестный сервис", "name", *serviceName)
		os.Exit(1)
	}

	slog.Info("demo-сервис завершён", "service", *serviceName)
}
