package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"barcodereader/internal/apierrors"
	"barcodereader/internal/config"
	"barcodereader/internal/decode"
)

// Handler обслуживает HTTP-контракт сервиса.
type Handler struct {
	cfg      config.Config
	decoder  Decoder
	logger   *slog.Logger
	draining atomic.Bool
}

// NewHandler собирает обработчик запросов.
func NewHandler(cfg config.Config, decoder Decoder, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{cfg: cfg, decoder: decoder, logger: logger}
}

// Routes возвращает настроенный mux.
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/decode", h.handleDecode)
	mux.HandleFunc("GET /healthz", h.handleHealth)
	mux.HandleFunc("GET /readyz", h.handleReady)
	return mux
}

// StartDraining перестаёт принимать запросы: используется при shutdown.
func (h *Handler) StartDraining() { h.draining.Store(true) }

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleReady(w http.ResponseWriter, _ *http.Request) {
	if h.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *Handler) handleDecode(w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	started := time.Now()

	if h.draining.Load() {
		h.writeError(w, requestID, apierrors.ShuttingDown())
		return
	}

	contentType := mediaType(r.Header.Get("Content-Type"))
	var (
		jsonBody *decodeRequest
		rawBody  []byte
	)

	switch contentType {
	case "application/pdf":
		body, err := readBody(w, r, h.cfg.MaxBodyBytes)
		if err != nil {
			h.writeError(w, requestID, err)
			return
		}
		rawBody = body
	case "application/json", "":
		body, err := readBody(w, r, h.cfg.MaxBodyBytes)
		if err != nil {
			h.writeError(w, requestID, err)
			return
		}
		parsed := &decodeRequest{}
		if err := json.Unmarshal(body, parsed); err != nil {
			h.writeError(w, requestID, apierrors.InvalidJSON(err))
			return
		}
		jsonBody = parsed
	default:
		h.writeError(w, requestID, apierrors.UnsupportedMediaType(contentType))
		return
	}

	pdf, err := extractPDF(contentType, jsonBody, rawBody, h.cfg.MaxPDFBytes)
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}

	opts, err := buildOptions(optionsOf(jsonBody), decode.Options{})
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.RequestTimeout)
	defer cancel()

	result, err := h.decoder.Decode(ctx, pdf, opts)
	if err != nil {
		h.logFailure(requestID, err, started, len(pdf))
		h.writeError(w, requestID, err)
		return
	}

	writeJSON(w, http.StatusOK, toResponse(requestID, result))

	h.logger.Info("документ обработан",
		"request_id", requestID,
		"codes", result.Meta.CodesUnique,
		"stages", result.Meta.StagesUsed,
		"pages", result.Meta.PagesScanned,
		"bytes", len(pdf),
		"elapsed_ms", time.Since(started).Milliseconds(),
	)
}

func optionsOf(body *decodeRequest) *requestOption {
	if body == nil {
		return nil
	}
	return body.Options
}

// readBody читает тело с ограничением размера.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	limited := http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, apierrors.PayloadTooLarge(limit)
		}
		return nil, apierrors.InvalidJSON(err)
	}
	if len(body) == 0 {
		return nil, apierrors.InvalidJSON(errors.New("пустое тело запроса"))
	}
	return body, nil
}

func (h *Handler) logFailure(requestID string, err error, started time.Time, size int) {
	apiErr := apierrors.AsError(err)

	attrs := []any{
		"request_id", requestID,
		"code", apiErr.Code,
		"status", apiErr.HTTPStatus,
		"bytes", size,
		"elapsed_ms", time.Since(started).Milliseconds(),
	}
	if apiErr.Code == "no_codes_found" {
		// Отсутствие кода — нормальный исход, а не сбой.
		h.logger.Info("коды не найдены", attrs...)
		return
	}
	h.logger.Error("запрос не обработан", append(attrs, "err", apiErr.Error())...)
}

func (h *Handler) writeError(w http.ResponseWriter, requestID string, err error) {
	apiErr := apierrors.AsError(err)
	writeJSON(w, apiErr.HTTPStatus, errorResponse{
		RequestID: requestID,
		Found:     false,
		Error: errorDTO{
			Code:    apiErr.Code,
			Message: apiErr.Message,
			Details: apiErr.Details,
		},
	})
}

// mediaType возвращает тип содержимого без параметров ("application/json; charset=utf-8" → "application/json").
func mediaType(header string) string {
	if header == "" {
		return ""
	}
	parsed, _, err := mime.ParseMediaType(header)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(header))
	}
	return parsed
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("не удалось записать ответ", "err", err)
	}
}