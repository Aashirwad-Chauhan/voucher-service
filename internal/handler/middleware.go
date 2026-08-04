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
			traceID = r.Header.Get("X-Correlation-ID") // fallback support
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

// RequestLogger logs HTTP requests and updates metrics.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			traceID := GetTraceID(r.Context())

			next.ServeHTTP(sw, r)

			duration := time.Since(start)
			path := r.URL.Path

			// Update Prometheus metrics
			HttpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(sw.statusCode)).Inc()
			HttpRequestDuration.WithLabelValues(r.Method, path).Observe(duration.Seconds())

			logger.Info("http_request",
				slog.String("trace_id", traceID),
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
					traceID := GetTraceID(r.Context())
					logger.Error("panic_recovered",
						slog.String("trace_id", traceID),
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
