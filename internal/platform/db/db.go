package db

import (
	"context"
	"time"

	"github.com/XSAM/otelsql"
	"github.com/jmoiron/sqlx"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" sql driver
)

// Connect opens a sqlx connection pool against dsn using the pgx stdlib
// driver, wrapped with otelsql so every query/exec on this pool emits a
// child span (visible under whatever span is live in the caller's ctx —
// e.g. the HTTP request span, or the matcher's per-batch span). Verifies
// connectivity with a ping (bounded by ctx).
func Connect(ctx context.Context, dsn string) (*sqlx.DB, error) {
	rawDB, err := otelsql.Open("pgx", dsn, otelsql.WithAttributes(semconv.DBSystemPostgreSQL))
	if err != nil {
		return nil, err
	}
	if _, err := otelsql.RegisterDBStatsMetrics(rawDB, otelsql.WithAttributes(semconv.DBSystemPostgreSQL)); err != nil {
		return nil, err
	}
	db := sqlx.NewDb(rawDB, "pgx")
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return nil, err
	}
	return db, nil
}
