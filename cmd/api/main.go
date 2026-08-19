// cmd/api/main.go
package main

import (
	"context"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"trade-market/internal/application/orders"
	"trade-market/internal/application/users"
	"trade-market/internal/infra/httpapi"
	"trade-market/internal/infra/postgres"
	"trade-market/internal/platform/config"
	"trade-market/internal/platform/db"
	"trade-market/internal/platform/logger"
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
	}, "trade-market-api", log)
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

	walletRepo := postgres.NewWalletRepository(sqlDB)
	orderRepo := postgres.NewOrderRepository(sqlDB)
	tradeRepo := postgres.NewTradeRepository(sqlDB)
	outboxRepo := postgres.NewOutboxRepository(sqlDB)
	userRepo := postgres.NewUserRepository(sqlDB)

	placeOrderSvc := orders.NewPlaceOrderService(sqlDB, walletRepo, userRepo, orderRepo, outboxRepo, cfg.OrdersStreamName, log)
	cancelOrderSvc := orders.NewCancelOrderService(sqlDB, orderRepo, outboxRepo, cfg.OrdersStreamName, log)
	getOrderQuery := orders.NewGetOrderQuery(orderRepo)
	getWalletQuery := orders.NewGetWalletQuery(walletRepo)
	listTradesQuery := orders.NewListTradesQuery(tradeRepo)
	getUserQuery := users.NewGetUserQuery(userRepo)
	listUsersQuery := users.NewListUsersQuery(userRepo)

	orderHandler := httpapi.NewOrderHandler(placeOrderSvc, cancelOrderSvc, getOrderQuery, log)
	walletHandler := httpapi.NewWalletHandler(getWalletQuery, log)
	tradeHandler := httpapi.NewTradeHandler(listTradesQuery, log)
	userHandler := httpapi.NewUserHandler(getUserQuery, listUsersQuery, log)

	router := httpapi.NewRouter(orderHandler, walletHandler, tradeHandler, userHandler, log)

	srv := &http.Server{
		Addr:              ":" + cfg.AppPort,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB, same as Go's http.DefaultMaxHeaderBytes — explicit rather than implicit
	}

	go func() {
		log.Info("api listening", zap.String("port", cfg.AppPort))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("server error", zap.Error(err))
		}
	}()

	<-ctx.Done()
	log.Info("shutting down api")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", zap.Error(err))
	}
}
