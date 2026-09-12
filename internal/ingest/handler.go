package ingest

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/stepankolchin/log-aggregator/internal/model"
)

// Handler принимает HTTP-запросы с логами и кладёт их во внутреннюю очередь.
type Handler struct {
	queue    chan<- model.LogEntry // канал передаётся снаружи, Handler только пишет
	accepted atomic.Int64
	dropped  atomic.Int64
	errCount atomic.Int64
}

// NewHandler создаёт Handler, привязанный к указанному каналу.
func NewHandler(queue chan<- model.LogEntry) *Handler {
	return &Handler{queue: queue}
}

// Stats возвращает счётчики: принято, отброшено, ошибок.
func (h *Handler) Stats() (accepted, dropped, errors int64) {
	return h.accepted.Load(), h.dropped.Load(), h.errCount.Load()
}

// HandleSingle — POST /api/v1/logs, принимает один лог.
func (h *Handler) HandleSingle(w http.ResponseWriter, r *http.Request) {
	var entry model.LogEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		h.errCount.Add(1)
		writeError(w, http.StatusBadRequest, "неверный JSON: "+err.Error())
		return
	}

	if err := Validate(&entry); err != nil {
		h.errCount.Add(1)
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	h.enqueue(w, entry)
}

// HandleBatch — POST /api/v1/logs/batch, принимает массив логов.
func (h *Handler) HandleBatch(w http.ResponseWriter, r *http.Request) {
	var req model.BatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.errCount.Add(1)
		writeError(w, http.StatusBadRequest, "неверный JSON: "+err.Error())
		return
	}
	if len(req.Logs) == 0 {
		writeError(w, http.StatusBadRequest, "батч пустой")
		return
	}

	var accepted, dropped, errs int
	for i := range req.Logs {
		if err := Validate(&req.Logs[i]); err != nil {
			errs++
			continue
		}
		select {
		case h.queue <- req.Logs[i]:
			accepted++
		default:
			// Канал заполнен — не блокируемся, считаем отброшенный лог
			dropped++
		}
	}

	h.accepted.Add(int64(accepted))
	h.dropped.Add(int64(dropped))
	h.errCount.Add(int64(errs))

	writeJSON(w, http.StatusAccepted, map[string]int{
		"accepted": accepted,
		"dropped":  dropped,
		"errors":   errs,
	})
}

// enqueue кладёт одиночный лог в канал без блокировки.
func (h *Handler) enqueue(w http.ResponseWriter, entry model.LogEntry) {
	select {
	case h.queue <- entry:
		h.accepted.Add(1)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	default:
		// Если воркеры не справляются — возвращаем 503 и не теряем запрос молча
		h.dropped.Add(1)
		writeError(w, http.StatusServiceUnavailable, "очередь переполнена, попробуйте позже")
	}
}

// writeError отправляет JSON-ответ с описанием ошибки.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeJSON сериализует v в JSON и записывает в ответ.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
