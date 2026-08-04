package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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
	traceID := GetTraceID(r.Context())

	var req model.CreateVoucherRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_json", "Failed to parse request JSON", traceID)
		return
	}

	h.logger.Info("voucher_create_requested",
		slog.String("trace_id", traceID),
		slog.String("code", req.Code),
	)

	resp, err := h.svc.CreateVoucher(r.Context(), &req)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrDuplicateVoucher):
			h.writeError(w, http.StatusConflict, "voucher_already_exists", err.Error(), traceID)
		case errors.Is(err, model.ErrInvalidInput):
			h.writeError(w, http.StatusBadRequest, "validation_error", err.Error(), traceID)
		default:
			h.logger.Error("create_voucher_failed",
				slog.String("trace_id", traceID),
				slog.Any("error", err),
			)
			h.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create voucher", traceID)
		}
		return
	}

	h.logger.Info("voucher_created",
		slog.String("trace_id", traceID),
		slog.String("code", resp.Code),
		slog.Int("remaining", resp.Remaining),
	)

	h.writeJSON(w, http.StatusCreated, resp)
}

// RedeemVoucher handles POST /vouchers/{code}/redeem
func (h *VoucherHandler) RedeemVoucher(w http.ResponseWriter, r *http.Request) {
	traceID := GetTraceID(r.Context())
	code := chi.URLParam(r, "code")

	var req model.RedeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		VoucherRedemptionsTotal.WithLabelValues("error").Inc()
		h.writeError(w, http.StatusBadRequest, "invalid_json", "Failed to parse request JSON", traceID)
		return
	}

	h.logger.Info("redeem_requested",
		slog.String("trace_id", traceID),
		slog.String("code", code),
		slog.String("user_id", req.UserID),
		slog.String("idempotency_key", req.IdempotencyKey),
	)

	resp, isReplay, err := h.svc.RedeemVoucher(r.Context(), code, &req)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrVoucherExhausted):
			VoucherRedemptionsTotal.WithLabelValues("rejected_exhausted").Inc()
			h.logger.Warn("redeem_rejected_exhausted",
				slog.String("trace_id", traceID),
				slog.String("code", code),
				slog.String("user_id", req.UserID),
			)
			h.writeError(w, http.StatusUnprocessableEntity, "voucher_exhausted", "Voucher has no remaining redemptions", traceID)

		case errors.Is(err, model.ErrVoucherNotFound):
			VoucherRedemptionsTotal.WithLabelValues("not_found").Inc()
			h.logger.Warn("redeem_rejected_not_found",
				slog.String("trace_id", traceID),
				slog.String("code", code),
			)
			h.writeError(w, http.StatusNotFound, "voucher_not_found", "Voucher code not found", traceID)

		case errors.Is(err, model.ErrIdempotencyConflict):
			VoucherRedemptionsTotal.WithLabelValues("conflict").Inc()
			h.logger.Warn("idempotent_conflict",
				slog.String("trace_id", traceID),
				slog.String("code", code),
				slog.String("idempotency_key", req.IdempotencyKey),
			)
			h.writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency key was used with a different request body", traceID)

		case errors.Is(err, model.ErrInvalidInput):
			VoucherRedemptionsTotal.WithLabelValues("error").Inc()
			h.writeError(w, http.StatusBadRequest, "validation_error", err.Error(), traceID)

		default:
			VoucherRedemptionsTotal.WithLabelValues("error").Inc()
			h.logger.Error("redeem_internal_error",
				slog.String("trace_id", traceID),
				slog.String("code", code),
				slog.Any("error", err),
			)
			h.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to redeem voucher", traceID)
		}
		return
	}

	if isReplay {
		VoucherRedemptionsTotal.WithLabelValues("replay").Inc()
		h.logger.Info("idempotent_replay",
			slog.String("trace_id", traceID),
			slog.String("code", code),
			slog.String("user_id", req.UserID),
			slog.String("idempotency_key", req.IdempotencyKey),
		)
	} else {
		VoucherRedemptionsTotal.WithLabelValues("granted").Inc()
		h.logger.Info("redeem_granted",
			slog.String("trace_id", traceID),
			slog.String("code", code),
			slog.String("user_id", req.UserID),
			slog.Int("remaining", resp.Remaining),
		)
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// GetVoucher handles GET /vouchers/{code}
func (h *VoucherHandler) GetVoucher(w http.ResponseWriter, r *http.Request) {
	traceID := GetTraceID(r.Context())
	code := chi.URLParam(r, "code")

	h.logger.Info("get_voucher_requested",
		slog.String("trace_id", traceID),
		slog.String("code", code),
	)

	resp, err := h.svc.GetVoucher(r.Context(), code)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrVoucherNotFound):
			h.writeError(w, http.StatusNotFound, "voucher_not_found", "Voucher code not found", traceID)
		case errors.Is(err, model.ErrInvalidInput):
			h.writeError(w, http.StatusBadRequest, "validation_error", err.Error(), traceID)
		default:
			h.logger.Error("get_voucher_failed",
				slog.String("trace_id", traceID),
				slog.Any("error", err),
			)
			h.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch voucher status", traceID)
		}
		return
	}

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
