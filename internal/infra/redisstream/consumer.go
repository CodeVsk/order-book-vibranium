// internal/infra/redisstream/consumer.go
package redisstream

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type Consumer struct {
	client       *redis.Client
	stream       string
	group        string
	consumerName string
}

// NewConsumer creates (or reuses) a single consumer group on stream and
// returns a Consumer bound to consumerName. Per the architecture, exactly
// one consumer group with exactly one active consumer name is used for the
// orders:incoming stream — this single-consumer guarantee is what
// serializes order arrival for the matching engine.
func NewConsumer(ctx context.Context, client *redis.Client, stream, group, consumerName string) (*Consumer, error) {
	err := client.XGroupCreateMkStream(ctx, stream, group, "$").Err()
	if err != nil && !redis.HasErrorPrefix(err, "BUSYGROUP") {
		return nil, err
	}
	return &Consumer{client: client, stream: stream, group: group, consumerName: consumerName}, nil
}

// ReadBatch blocks for up to block waiting for up to count new entries
// (">" = never-delivered-to-this-group), implementing the spec's
// micro-batch throughput lever ("até N eventos ... ou até T ms, o que vier
// primeiro"). Returns an empty (non-nil) slice on timeout, not an error.
func (c *Consumer) ReadBatch(ctx context.Context, count int64, block time.Duration) ([]redis.XMessage, error) {
	res, err := c.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    c.group,
		Consumer: c.consumerName,
		Streams:  []string{c.stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if err != nil {
		if err == redis.Nil {
			return []redis.XMessage{}, nil
		}
		return nil, err
	}
	if len(res) == 0 {
		return []redis.XMessage{}, nil
	}
	return res[0].Messages, nil
}

// ReadPending returns up to count entries delivered to this consumer name
// but never XAck'd (e.g. a crash between XREADGROUP and XAck). Call once at
// boot, before the main ReadBatch(">") loop, so orphaned entries get
// reprocessed through the idempotency-guarded path (processed_stream_events)
// instead of sitting in the PEL forever. Returns an empty (non-nil) slice
// once no pending entries remain.
func (c *Consumer) ReadPending(ctx context.Context, count int64) ([]redis.XMessage, error) {
	res, err := c.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    c.group,
		Consumer: c.consumerName,
		Streams:  []string{c.stream, "0"},
		Count:    count,
	}).Result()
	if err != nil {
		if err == redis.Nil {
			return []redis.XMessage{}, nil
		}
		return nil, err
	}
	if len(res) == 0 {
		return []redis.XMessage{}, nil
	}
	return res[0].Messages, nil
}

// Ack confirms processing of the given entry ids. Must only be called
// after the corresponding Postgres transaction has committed.
func (c *Consumer) Ack(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	return c.client.XAck(ctx, c.stream, c.group, ids...).Err()
}
