package redisstream

import (
	"context"

	"github.com/redis/go-redis/v9"
)

type Producer struct {
	client *redis.Client
}

func NewProducer(client *redis.Client) *Producer {
	return &Producer{client: client}
}

// Publish appends payload to stream and returns the Redis-assigned entry
// ID. Used by the outbox publisher — never called directly by the API
// (that would reintroduce the dual-write problem the outbox pattern
// exists to eliminate).
func (p *Producer) Publish(ctx context.Context, stream string, payload []byte) (string, error) {
	id, err := p.client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: map[string]any{"payload": payload},
	}).Result()
	if err != nil {
		return "", err
	}
	return id, nil
}
