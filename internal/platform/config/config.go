package config

import (
	"errors"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL           string
	RedisURL              string
	AppPort               string
	LogLevel              string
	OrdersStreamName      string
	TradesStreamName      string
	ConsumerGroupName     string
	MatcherBatchSize      int64
	MatcherBatchTimeout   time.Duration
	OutboxBatchSize       int
	OutboxPollInterval    time.Duration
	OtelExporterEndpoint  string  // OTEL_EXPORTER_OTLP_ENDPOINT
	OtelTracesSampleRatio float64 // OTEL_TRACES_SAMPLER_ARG, 0.0-1.0
}

var ErrMissingDatabaseURL = errors.New("config: DATABASE_URL is required")
var ErrMissingRedisURL = errors.New("config: REDIS_URL is required")

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		RedisURL:              os.Getenv("REDIS_URL"),
		AppPort:               getEnvDefault("APP_PORT", "8080"),
		LogLevel:              getEnvDefault("LOG_LEVEL", "info"),
		OrdersStreamName:      getEnvDefault("ORDERS_STREAM_NAME", "orders:incoming"),
		TradesStreamName:      getEnvDefault("TRADES_STREAM_NAME", "trades:executed"),
		ConsumerGroupName:     getEnvDefault("CONSUMER_GROUP_NAME", "matcher-group"),
		MatcherBatchSize:      getEnvInt64Default("MATCHER_BATCH_SIZE", 200),
		MatcherBatchTimeout:   getEnvDurationMillisDefault("MATCHER_BATCH_TIMEOUT_MS", 50),
		OutboxBatchSize:       int(getEnvInt64Default("OUTBOX_BATCH_SIZE", 200)),
		OutboxPollInterval:    getEnvDurationMillisDefault("OUTBOX_POLL_INTERVAL_MS", 100),
		OtelExporterEndpoint:  getEnvDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318"),
		OtelTracesSampleRatio: getEnvFloat64Default("OTEL_TRACES_SAMPLER_ARG", 1.0),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, ErrMissingDatabaseURL
	}
	if cfg.RedisURL == "" {
		return Config{}, ErrMissingRedisURL
	}
	return cfg, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt64Default(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func getEnvDurationMillisDefault(key string, defMillis int64) time.Duration {
	return time.Duration(getEnvInt64Default(key, defMillis)) * time.Millisecond
}

func getEnvFloat64Default(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}
