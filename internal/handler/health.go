package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/aashirwad/voucher-service/internal/model"
	"github.com/aashirwad/voucher-service/internal/repository"
)

type HealthHandler struct {
	repo   repository.Repository
	logger *slog.Logger
}

func NewHealthHandler(repo repository.Repository, logger *slog.Logger) *HealthHandler {
	return &HealthHandler{
		repo:   repo,
		logger: logger,
	}
}

// Healthz returns 200 if the app process is alive.
func (h *HealthHandler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(model.HealthResponse{Status: "ok"})
}

// Readyz checks the database connectivity.
func (h *HealthHandler) Readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	traceID := GetTraceID(r.Context())

	if err := h.repo.Ping(r.Context()); err != nil {
		h.logger.Warn("readiness_check_failed",
			slog.String("trace_id", traceID),
			slog.Any("error", err),
		)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(model.HealthResponse{
			Status:   "not_ready",
			Database: "unreachable",
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(model.HealthResponse{
		Status:   "ready",
		Database: "ok",
	})
}
