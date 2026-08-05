package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aashirwad/voucher-service/internal/model"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"
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
						TraceID: traceID,
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AdminKeyMiddleware guards sensitive admin routes (like pprof).
// Fails closed if adminKey is empty or header is invalid.
func AdminKeyMiddleware(adminKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			traceID := GetTraceID(r.Context())
			key := r.Header.Get("X-Admin-Key")

			if adminKey == "" || key != adminKey {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(model.ErrorResponse{
					Error:   "forbidden",
					Message: "Admin access required",
					TraceID: traceID,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type RateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*ipLimiter
	r        rate.Limit
	b        int
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewRateLimiter(r rate.Limit, b int) *RateLimiter {
	rl := &RateLimiter{
		limiters: make(map[string]*ipLimiter),
		r:        r,
		b:        b,
		stopCh:   make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for ip, l := range rl.limiters {
				if now.Sub(l.lastSeen) > 10*time.Minute {
					delete(rl.limiters, ip)
				}
			}
			rl.mu.Unlock()
		case <-rl.stopCh:
			return
		}
	}
}

func (rl *RateLimiter) Stop() {
	if rl == nil {
		return
	}
	rl.stopOnce.Do(func() {
		close(rl.stopCh)
	})
}

func realIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if ip != "" {
				return ip
			}
		}
	}

	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// RateLimiterMiddleware enforces token bucket rate limiting per real client IP.
func RateLimiterMiddleware(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := realIP(r)

			rl.mu.Lock()
			l, exists := rl.limiters[ip]
			if !exists {
				l = &ipLimiter{
					limiter:  rate.NewLimiter(rl.r, rl.b),
					lastSeen: time.Now(),
				}
				rl.limiters[ip] = l
			}
			l.lastSeen = time.Now()
			allowed := l.limiter.Allow()
			rl.mu.Unlock()

			if !allowed {
				traceID := GetTraceID(r.Context())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(model.ErrorResponse{
					Error:   "rate_limit_exceeded",
					Message: "Too many requests. Please slow down.",
					TraceID: traceID,
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
