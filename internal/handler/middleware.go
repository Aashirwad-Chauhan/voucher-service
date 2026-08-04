package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/aashirwad/voucher-service/internal/model"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type contextKey string

const CorrelationIDKey contextKey = "correlation_id"

var (
	HttpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests processed",
		},
		[]string{"method", "path", "status"},
	)

	HttpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
		[]string{"method", "path"},
	)

	VoucherRedemptionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "voucher_redemptions_total",
			Help: "Total number of voucher redemptions by result",
		},
		[]string{"result"}, // "granted", "rejected_exhausted", "replay", "conflict", "not_found", "error"
	)
)

// CorrelationIDMiddleware ensures every request has an X-Correlation-ID header and context value.
func CorrelationIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corrID := r.Header.Get("X-Correlation-ID")
		if corrID == "" {
			corrID = uuid.New().String()
		}

		w.Header().Set("X-Correlation-ID", corrID)
		ctx := context.WithValue(r.Context(), CorrelationIDKey, corrID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetCorrelationID extracts the correlation ID from context.
func GetCorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(CorrelationIDKey).(string); ok {
		return id
	}
	return "unknown"
}

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (sw *statusResponseWriter) WriteHeader(code int) {
	sw.statusCode = code
	sw.ResponseWriter.WriteHeader(code)
}

// RequestLogger logs HTTP requests and updates metrics.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(sw, r)

			duration := time.Since(start)
			corrID := GetCorrelationID(r.Context())
			path := r.URL.Path

			// Update Prometheus metrics
			HttpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(sw.statusCode)).Inc()
			HttpRequestDuration.WithLabelValues(r.Method, path).Observe(duration.Seconds())

			logger.Info("http_request",
				slog.String("correlation_id", corrID),
				slog.String("method", r.Method),
				slog.String("path", path),
				slog.Int("status", sw.statusCode),
				slog.Int64("latency_ms", duration.Milliseconds()),
			)
		})
	}
}

// RecoveryMiddleware catches panics and returns 500 JSON error.
func RecoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					corrID := GetCorrelationID(r.Context())
					logger.Error("panic_recovered",
						slog.String("correlation_id", corrID),
						slog.Any("error", err),
						slog.String("stack", string(debug.Stack())),
					)

					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(model.ErrorResponse{
						Error:   "internal_server_error",
						Message: "An unexpected error occurred",
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
