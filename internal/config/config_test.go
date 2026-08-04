package config_test

import (
	"os"
	"testing"

	"github.com/aashirwad/voucher-service/internal/config"
)

func TestConfigLoad_Defaults(t *testing.T) {
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("PORT")
	os.Unsetenv("LOG_LEVEL")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Unexpected error loading default config: %v", err)
	}

	if cfg.Port != "8080" {
		t.Errorf("Expected default Port 8080, got %s", cfg.Port)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("Expected default LogLevel info, got %s", cfg.LogLevel)
	}
	if cfg.DatabaseURL == "" {
		t.Errorf("Expected fallback DatabaseURL, got empty string")
	}
}

func TestConfigLoad_CustomEnv(t *testing.T) {
	os.Setenv("PORT", "9090")
	os.Setenv("LOG_LEVEL", "debug")
	os.Setenv("DATABASE_URL", "postgres://test:test@localhost:5432/testdb")
	defer func() {
		os.Unsetenv("PORT")
		os.Unsetenv("LOG_LEVEL")
		os.Unsetenv("DATABASE_URL")
	}()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if cfg.Port != "9090" {
		t.Errorf("Expected Port 9090, got %s", cfg.Port)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("Expected LogLevel debug, got %s", cfg.LogLevel)
	}
	if cfg.DatabaseURL != "postgres://test:test@localhost:5432/testdb" {
		t.Errorf("Expected custom DatabaseURL, got %s", cfg.DatabaseURL)
	}
}
