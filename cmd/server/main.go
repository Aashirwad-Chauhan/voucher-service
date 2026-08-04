package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aashirwad/voucher-service/internal/config"
	"github.com/aashirwad/voucher-service/internal/handler"
	"github.com/aashirwad/voucher-service/internal/observability"
	"github.com/aashirwad/voucher-service/internal/repository"
	"github.com/aashirwad/voucher-service/internal/service"
	"github.com/aashirwad/voucher-service/migrations"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	// 1. Setup Logger
	var logLevel slog.Level
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed_to_load_config", slog.Any("error", err))
		os.Exit(1)
	}

	lokiPusher := observability.NewLokiPusher(cfg)
	if lokiPusher != nil {
		defer lokiPusher.Close()
	}

	dualWriter := observability.NewDualWriter(lokiPusher)
	jsonHandler := slog.NewJSONHandler(dualWriter, &slog.HandlerOptions{Level: logLevel})
	logger := slog.New(jsonHandler)
	slog.SetDefault(logger)

	logger.Info("starting_voucher_service",
		slog.String("port", cfg.Port),
		slog.String("log_level", cfg.LogLevel),
	)

	// 2. Setup Database Pool
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed_to_parse_database_url", slog.Any("error", err))
		os.Exit(1)
	}

	poolConfig.MaxConns = 25
	poolConfig.MinConns = 2

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		logger.Error("failed_to_connect_database", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		logger.Error("failed_to_ping_database", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("database_connected_successfully")

	// 3. Run Schema Migrations
	if err := runMigrations(ctx, pool); err != nil {
		logger.Error("failed_to_run_migrations", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("database_migrations_applied_successfully")

	// 4. Wire Layers
	repo := repository.NewPostgresRepository(pool)
	svc := service.NewVoucherService(repo)

	voucherHandler := handler.NewVoucherHandler(svc, logger)
	healthHandler := handler.NewHealthHandler(repo)

	// 5. Router Setup
	r := chi.NewRouter()

	r.Use(handler.TraceIDMiddleware)
	r.Use(handler.RequestLogger(logger))
	r.Use(handler.RecoveryMiddleware(logger))

	// Health & Metrics
	r.Get("/healthz", healthHandler.Healthz)
	r.Get("/readyz", healthHandler.Readyz)
	r.Handle("/metrics", promhttp.Handler())

	// Core API
	r.Post("/vouchers", voucherHandler.CreateVoucher)
	r.Post("/vouchers/{code}/redeem", voucherHandler.RedeemVoucher)
	r.Get("/vouchers/{code}", voucherHandler.GetVoucher)

	// 6. Start HTTP Server
	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("http_server_listening", slog.String("addr", server.Addr))
		serverErrors <- server.ListenAndServe()
	}()

	// 7. Graceful Shutdown
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		logger.Error("server_error", slog.Any("error", err))
	case sig := <-shutdown:
		logger.Info("shutdown_signal_received", slog.String("signal", sig.String()))

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful_shutdown_failed", slog.Any("error", err))
			_ = server.Close()
		} else {
			logger.Info("server_shutdown_completed_cleanly")
		}
	}
}

func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if migrations.InitSQL == "" {
		return fmt.Errorf("migration SQL is empty")
	}
	_, err := pool.Exec(ctx, migrations.InitSQL)
	return err
}
