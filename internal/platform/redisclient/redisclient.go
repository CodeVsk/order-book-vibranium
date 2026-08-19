package redisclient

import (
	"context"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

// Connect parses redisURL (e.g. "redis://localhost:6379/0") and verifies
// connectivity with a PING (bounded by ctx). The client is instrumented
// with redisotel so every Redis command (XADD, XREADGROUP, XACK, ...) emits
// a child span under whatever span is live in the caller's ctx.
func Connect(ctx context.Context, redisURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	if err := redisotel.InstrumentTracing(client); err != nil {
		client.Close()
		return nil, err
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}
