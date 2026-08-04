package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/aashirwad/voucher-service/internal/model"
	"github.com/aashirwad/voucher-service/internal/service"
	"github.com/go-chi/chi/v5"
)

type VoucherHandler struct {
	svc    service.VoucherService
	logger *slog.Logger
}

func NewVoucherHandler(svc service.VoucherService, logger *slog.Logger) *VoucherHandler {
	return &VoucherHandler{
		svc:    svc,
		logger: logger,
	}
}

// CreateVoucher handles POST /vouchers
func (h *VoucherHandler) CreateVoucher(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	traceID := GetTraceID(r.Context())

	var req model.CreateVoucherRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Warn("request_completed",
			slog.String("trace_id", traceID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", http.StatusBadRequest),
			slog.Int64("latency_ms", time.Since(start).Milliseconds()),
			slog.String("result", "invalid_json"),
		)
		h.writeError(w, http.StatusBadRequest, "invalid_json", "Failed to parse request JSON", traceID)
		return
	}

	maxRedemptions := 1
	if req.MaxRedemptions != nil {
		maxRedemptions = *req.MaxRedemptions
	}

	// 1. Initial Request Log
	h.logger.Info("request_received",
		slog.String("trace_id", traceID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("code", req.Code),
		slog.Int("max_redemptions", maxRedemptions),
	)

	resp, err := h.svc.CreateVoucher(r.Context(), &req)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		switch {
		case errors.Is(err, model.ErrDuplicateVoucher):
			h.logger.Warn("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusConflict),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "already_exists"),
				slog.String("code", req.Code),
			)
			h.writeError(w, http.StatusConflict, "voucher_already_exists", err.Error(), traceID)

		case errors.Is(err, model.ErrInvalidInput):
			h.logger.Warn("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusBadRequest),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "validation_error"),
				slog.String("code", req.Code),
			)
			h.writeError(w, http.StatusBadRequest, "validation_error", err.Error(), traceID)

		default:
			h.logger.Error("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusInternalServerError),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "error"),
				slog.Any("error", err),
			)
			h.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create voucher", traceID)
		}
		return
	}

	// 2. Final Response Log
	h.logger.Info("request_completed",
		slog.String("trace_id", traceID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", http.StatusCreated),
		slog.Int64("latency_ms", latencyMs),
		slog.String("result", "created"),
		slog.String("code", resp.Code),
		slog.Int("remaining", resp.Remaining),
		slog.Int("max_redemptions", maxRedemptions),
	)

	h.writeJSON(w, http.StatusCreated, resp)
}

// RedeemVoucher handles POST /vouchers/{code}/redeem
func (h *VoucherHandler) RedeemVoucher(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	traceID := GetTraceID(r.Context())
	code := chi.URLParam(r, "code")

	var req model.RedeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		VoucherRedemptionsTotal.WithLabelValues("error").Inc()
		h.logger.Warn("request_completed",
			slog.String("trace_id", traceID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", http.StatusBadRequest),
			slog.Int64("latency_ms", time.Since(start).Milliseconds()),
			slog.String("result", "invalid_json"),
		)
		h.writeError(w, http.StatusBadRequest, "invalid_json", "Failed to parse request JSON", traceID)
		return
	}

	// 1. Initial Request Log
	h.logger.Info("request_received",
		slog.String("trace_id", traceID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("code", code),
		slog.String("user_id", req.UserID),
		slog.String("idempotency_key", req.IdempotencyKey),
	)

	resp, isReplay, err := h.svc.RedeemVoucher(r.Context(), code, &req)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		switch {
		case errors.Is(err, model.ErrVoucherExhausted):
			VoucherRedemptionsTotal.WithLabelValues("rejected_exhausted").Inc()
			h.logger.Warn("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusUnprocessableEntity),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "rejected_exhausted"),
				slog.String("code", code),
				slog.String("user_id", req.UserID),
				slog.Int("remaining", 0),
			)
			h.writeError(w, http.StatusUnprocessableEntity, "voucher_exhausted", "Voucher has no remaining redemptions", traceID)

		case errors.Is(err, model.ErrVoucherNotFound):
			VoucherRedemptionsTotal.WithLabelValues("not_found").Inc()
			h.logger.Warn("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusNotFound),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "not_found"),
				slog.String("code", code),
			)
			h.writeError(w, http.StatusNotFound, "voucher_not_found", "Voucher code not found", traceID)

		case errors.Is(err, model.ErrIdempotencyConflict):
			VoucherRedemptionsTotal.WithLabelValues("conflict").Inc()
			h.logger.Warn("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusConflict),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "idempotency_conflict"),
				slog.String("code", code),
				slog.String("idempotency_key", req.IdempotencyKey),
			)
			h.writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency key was used with a different request body", traceID)

		case errors.Is(err, model.ErrInvalidInput):
			VoucherRedemptionsTotal.WithLabelValues("error").Inc()
			h.logger.Warn("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusBadRequest),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "validation_error"),
				slog.String("code", code),
			)
			h.writeError(w, http.StatusBadRequest, "validation_error", err.Error(), traceID)

		default:
			VoucherRedemptionsTotal.WithLabelValues("error").Inc()
			h.logger.Error("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusInternalServerError),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "error"),
				slog.String("code", code),
				slog.Any("error", err),
			)
			h.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to redeem voucher", traceID)
		}
		return
	}

	resultType := "granted"
	if isReplay {
		resultType = "replay"
		VoucherRedemptionsTotal.WithLabelValues("replay").Inc()
	} else {
		VoucherRedemptionsTotal.WithLabelValues("granted").Inc()
	}

	// 2. Final Response Log
	h.logger.Info("request_completed",
		slog.String("trace_id", traceID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", http.StatusOK),
		slog.Int64("latency_ms", latencyMs),
		slog.String("result", resultType),
		slog.String("code", code),
		slog.String("user_id", req.UserID),
		slog.String("redemption_id", resp.RedemptionID.String()),
		slog.Int("remaining", resp.Remaining),
	)

	h.writeJSON(w, http.StatusOK, resp)
}

// GetVoucher handles GET /vouchers/{code}
func (h *VoucherHandler) GetVoucher(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	traceID := GetTraceID(r.Context())
	code := chi.URLParam(r, "code")

	// 1. Initial Request Log
	h.logger.Info("request_received",
		slog.String("trace_id", traceID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("code", code),
	)

	resp, err := h.svc.GetVoucher(r.Context(), code)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		switch {
		case errors.Is(err, model.ErrVoucherNotFound):
			h.logger.Warn("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusNotFound),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "not_found"),
				slog.String("code", code),
			)
			h.writeError(w, http.StatusNotFound, "voucher_not_found", "Voucher code not found", traceID)

		case errors.Is(err, model.ErrInvalidInput):
			h.logger.Warn("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusBadRequest),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "validation_error"),
				slog.String("code", code),
			)
			h.writeError(w, http.StatusBadRequest, "validation_error", err.Error(), traceID)

		default:
			h.logger.Error("request_completed",
				slog.String("trace_id", traceID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", http.StatusInternalServerError),
				slog.Int64("latency_ms", latencyMs),
				slog.String("result", "error"),
				slog.String("code", code),
				slog.Any("error", err),
			)
			h.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch voucher status", traceID)
		}
		return
	}

	// 2. Final Response Log
	h.logger.Info("request_completed",
		slog.String("trace_id", traceID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", http.StatusOK),
		slog.Int64("latency_ms", latencyMs),
		slog.String("result", "ok"),
		slog.String("code", resp.Code),
		slog.Int("remaining", resp.Remaining),
		slog.Int("redemptions_count", resp.RedemptionsCount),
		slog.Int("max_redemptions", resp.MaxRedemptions),
	)

	h.writeJSON(w, http.StatusOK, resp)
}

func (h *VoucherHandler) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (h *VoucherHandler) writeError(w http.ResponseWriter, status int, errType, message, traceID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.ErrorResponse{
		Error:   errType,
		Message: message,
	})
}
