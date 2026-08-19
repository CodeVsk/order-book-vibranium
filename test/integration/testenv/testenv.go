//go:build integration

package testenv

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"trade-market/internal/platform/db"
	"trade-market/internal/platform/redisclient"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// Env bundles a real Postgres (schema migrated) and Redis, spun up via
// testcontainers-go, for the integration/concurrency/idempotency test
// suites. Call Cleanup (or t.Cleanup(env.Cleanup)) when done.
type Env struct {
	DB    *sqlx.DB
	Redis *redis.Client

	cleanupFns []func()
}

func Setup(t *testing.T, ctx context.Context) *Env {
	t.Helper()
	env := &Env{}
	t.Cleanup(env.Cleanup)

	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("orderbook"),
		tcpostgres.WithUsername("orderbook"),
		tcpostgres.WithPassword("orderbook"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}
	// Terminate is called from Cleanup, which may run after the caller's own
	// ctx has already been cancelled (e.g. a test's `defer cancel()` runs
	// during the stack unwind triggered by a t.Fatalf inside this very
	// function, BEFORE the t.Cleanup-registered functions run — this is
	// normal Go testing package behavior, not specific to this helper).
	// Terminate must not be handed an already-Done context, or container
	// teardown silently fails and leaks the container. Always use a fresh,
	// independent context for teardown.
	env.cleanupFns = append(env.cleanupFns, func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = pgContainer.Terminate(cleanupCtx)
	})

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get postgres dsn: %v", err)
	}

	sqlDB, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("failed to connect to postgres container: %v", err)
	}
	env.DB = sqlDB
	env.cleanupFns = append(env.cleanupFns, func() { sqlDB.Close() })

	runMigrations(t, sqlDB)

	redisContainer, err := tcredis.Run(ctx, "redis:7-alpine", tcredis.WithSnapshotting(10, 1))
	if err != nil {
		t.Fatalf("failed to start redis container: %v", err)
	}
	env.cleanupFns = append(env.cleanupFns, func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = redisContainer.Terminate(cleanupCtx)
	})

	redisURI, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("failed to get redis connection string: %v", err)
	}
	// The container's wait strategy (the default "* Ready to accept
	// connections" log match) can report ready a couple of seconds before
	// the mapped port is actually reachable through some Docker network
	// setups (observed on some Docker network setups; may not reproduce on
	// every host). A short bounded retry absorbs that gap
	// without weakening the container's own readiness check.
	redisClient, err := connectRedisWithRetry(ctx, redisURI)
	if err != nil {
		t.Fatalf("failed to connect to redis container: %v", err)
	}
	env.Redis = redisClient
	env.cleanupFns = append(env.cleanupFns, func() { redisClient.Close() })

	return env
}

// connectRedisWithRetry retries redisclient.Connect for up to 10s (200ms
// between attempts) to absorb a brief window where a container's
// readiness signal has fired but its mapped port isn't reachable yet.
func connectRedisWithRetry(ctx context.Context, uri string) (*redis.Client, error) {
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for {
		client, err := redisclient.Connect(ctx, uri)
		if err == nil {
			return client, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (e *Env) Cleanup() {
	for i := len(e.cleanupFns) - 1; i >= 0; i-- {
		e.cleanupFns[i]()
	}
}

func runMigrations(t *testing.T, sqlDB *sqlx.DB) {
	t.Helper()
	driver, err := migratepgx.WithInstance(sqlDB.DB, &migratepgx.Config{})
	if err != nil {
		t.Fatalf("failed to create migrate driver: %v", err)
	}
	m, err := migrate.NewWithDatabaseInstance("file://"+migrationsDir(), "pgx5", driver)
	if err != nil {
		t.Fatalf("failed to init migrate: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
}

// migrationsDir resolves the absolute path to the repo-root migrations/
// directory using this source file's own location (runtime.Caller),
// rather than a path relative to the calling test's working directory.
// Setup is called from packages at different depths (test/integration,
// test/concurrency, internal/application/matcherapp), so a path relative
// to the CALLER's CWD would be wrong for at least one of them — anchoring
// to testenv.go's own location makes it correct for all callers.
func migrationsDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("testenv: unable to determine caller for migrationsDir")
	}
	// thisFile == <repo-root>/test/integration/testenv/testenv.go
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations")
}
