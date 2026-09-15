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
// Контракт статусов:
// - 202 Accepted: если хотя бы одна запись принята в очередь (accepted > 0, включая полный успех accepted == len(logs)).
// - 422 Unprocessable Entity: если ни одна запись не принята (accepted == 0) из-за ошибок валидации.
// - 503 Service Unavailable: если ни одна запись не принята (accepted == 0) из-за переполнения очереди.
// - 400 Bad Request: если JSON невалиден или передан пустой массив logs.
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
	var failed []model.BatchItemError

	for i := range req.Logs {
		if err := Validate(&req.Logs[i]); err != nil {
			errs++
			failed = append(failed, model.BatchItemError{
				Index:     i,
				Status:    "validation_error",
				Error:     err.Error(),
				Retryable: false,
			})
			continue
		}
		select {
		case h.queue <- req.Logs[i]:
			accepted++
		default:
			// Канал заполнен — не блокируемся, считаем отброшенный лог
			dropped++
			failed = append(failed, model.BatchItemError{
				Index:     i,
				Status:    "queue_full",
				Error:     "очередь переполнена, повторите попытку позже",
				Retryable: true,
			})
		}
	}

	h.accepted.Add(int64(accepted))
	h.dropped.Add(int64(dropped))
	h.errCount.Add(int64(errs))

	resp := model.BatchResponse{
		Accepted: accepted,
		Dropped:  dropped,
		Errors:   errs,
		Failed:   failed,
	}

	// Определение HTTP-статуса по контракту:
	if accepted > 0 {
		writeJSON(w, http.StatusAccepted, resp)
		return
	}

	// accepted == 0: полный отказ
	if dropped > 0 && errs == 0 {
		// Все записи отклонены исключительно из-за переполнения очереди сервера
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return
	}

	// Все записи содержат ошибки валидации (или смешанный отказ без единого успеха)
	writeJSON(w, http.StatusUnprocessableEntity, resp)
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
