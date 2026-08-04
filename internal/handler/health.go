package handler

import (
	"encoding/json"
	"net/http"

	"github.com/aashirwad/voucher-service/internal/model"
	"github.com/aashirwad/voucher-service/internal/repository"
)

type HealthHandler struct {
	repo repository.Repository
}

func NewHealthHandler(repo repository.Repository) *HealthHandler {
	return &HealthHandler{repo: repo}
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

	if err := h.repo.Ping(r.Context()); err != nil {
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
