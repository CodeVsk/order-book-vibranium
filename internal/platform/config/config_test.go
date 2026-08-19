package config

import "testing"

func TestLoad_DefaultsAndOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/orderbook?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("APP_PORT", "9090")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DatabaseURL != "postgres://user:pass@localhost:5432/orderbook?sslmode=disable" {
		t.Fatalf("unexpected DatabaseURL: %s", cfg.DatabaseURL)
	}
	if cfg.AppPort != "9090" {
		t.Fatalf("unexpected AppPort: %s", cfg.AppPort)
	}
	if cfg.OrdersStreamName != "orders:incoming" {
		t.Fatalf("expected default OrdersStreamName, got %s", cfg.OrdersStreamName)
	}
}

func TestLoad_MissingDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error when DATABASE_URL is missing")
	}
}
