package config

import (
	"fmt"
	"os"

	_ "github.com/joho/godotenv/autoload" // optional autoload from .env if present
)

type Config struct {
	DatabaseURL     string
	Port            string
	LogLevel        string
	GrafanaLokiURL  string
	GrafanaLokiUser string
	GrafanaAPIKey   string
	GrafanaPromURL  string
	GrafanaPromUser string
	GrafanaPromKey  string
	AdminKey        string
}

func Load() (*Config, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// Fallback default for local docker-compose
		dbURL = "postgres://voucher:voucher@localhost:5432/voucher?sslmode=disable"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}

	cfg := &Config{
		DatabaseURL:     dbURL,
		Port:            port,
		LogLevel:        logLevel,
		GrafanaLokiURL:  os.Getenv("GRAFANA_LOKI_URL"),
		GrafanaLokiUser: os.Getenv("GRAFANA_LOKI_USER"),
		GrafanaAPIKey:   os.Getenv("GRAFANA_API_KEY"),
		GrafanaPromURL:  os.Getenv("GRAFANA_PROM_URL"),
		GrafanaPromUser: os.Getenv("GRAFANA_PROM_USER"),
		GrafanaPromKey:  os.Getenv("GRAFANA_PROM_KEY"),
		AdminKey:        os.Getenv("ADMIN_KEY"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable is required")
	}

	return cfg, nil
}
