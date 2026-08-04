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

const TraceIDKey contextKey = "trace_id"

var (
	HttpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests processed",
		},
		[]string{"method", "path", "status"},
	)

	HttpErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_errors_total",
			Help: "Total number of HTTP error responses (status >= 400)",
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

// TraceIDMiddleware ensures every request has an X-Trace-ID header and context value.
func TraceIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Trace-ID")
		if traceID == "" {
			traceID = r.Header.Get("X-Correlation-ID")
		}
		if traceID == "" {
			traceID = uuid.New().String()
		}

		w.Header().Set("X-Trace-ID", traceID)
		ctx := context.WithValue(r.Context(), TraceIDKey, traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetTraceID extracts the trace ID from context.
func GetTraceID(ctx context.Context) string {
	if id, ok := ctx.Value(TraceIDKey).(string); ok {
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

// RequestLogger updates Prometheus metrics and logs request completion cleanly.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(sw, r)

			duration := time.Since(start)
			path := r.URL.Path
			statusStr := strconv.Itoa(sw.statusCode)

			// Update Prometheus metrics
			HttpRequestsTotal.WithLabelValues(r.Method, path, statusStr).Inc()
			HttpRequestDuration.WithLabelValues(r.Method, path).Observe(duration.Seconds())

			// Track errors specifically
			if sw.statusCode >= 400 {
				HttpErrorsTotal.WithLabelValues(r.Method, path, statusStr).Inc()
			}
		})
	}
}

// RecoveryMiddleware catches panics and returns 500 JSON error.
func RecoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					traceID := GetTraceID(r.Context())
					logger.Error("request_failed",
						slog.String("trace_id", traceID),
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path),
						slog.Int("status", http.StatusInternalServerError),
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
