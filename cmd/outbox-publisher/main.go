// cmd/outbox-publisher/main.go
package main

import (
	"context"
	"os/signal"
	"syscall"
	"time"

	"trade-market/internal/application/outboxapp"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/infra/redisstream"
	"trade-market/internal/platform/config"
	"trade-market/internal/platform/db"
	"trade-market/internal/platform/logger"
	"trade-market/internal/platform/redisclient"
	"trade-market/internal/platform/telemetry"

	"go.uber.org/zap"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log, err := logger.New(cfg.LogLevel)
	if err != nil {
		panic(err)
	}
	defer log.Sync()

	shutdownTracing, err := telemetry.InitTracerProvider(ctx, telemetry.Config{
		ExporterEndpoint: cfg.OtelExporterEndpoint,
		SampleRatio:      cfg.OtelTracesSampleRatio,
	}, "trade-market-outbox-publisher", log)
	if err != nil {
		log.Fatal("failed to initialize tracing", zap.Error(err))
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			log.Error("failed to flush tracer provider on shutdown", zap.Error(err))
		}
	}()

	sqlDB, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal("failed to connect to postgres", zap.Error(err))
	}
	defer sqlDB.Close()

	redisClient, err := redisclient.Connect(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatal("failed to connect to redis", zap.Error(err))
	}
	defer redisClient.Close()

	outboxRepo := postgres.NewOutboxRepository(sqlDB)
	producer := redisstream.NewProducer(redisClient)
	publisher := outboxapp.NewPublisher(sqlDB, outboxRepo, producer, cfg.OutboxBatchSize, log)

	log.Info("outbox publisher starting", zap.Duration("poll_interval", cfg.OutboxPollInterval))
	if err := publisher.Run(ctx, cfg.OutboxPollInterval); err != nil && ctx.Err() == nil {
		log.Fatal("publisher loop exited unexpectedly", zap.Error(err))
	}
	log.Info("outbox publisher stopped")
}
