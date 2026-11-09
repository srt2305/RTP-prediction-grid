package store

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// Hot per-cell counters for the grid UI. Postgres stays the system of record;
// these rebuild from stake rows if lost.
type LiveTotals struct {
	client *redis.Client
}

func OpenRedis(ctx context.Context, addr string) (*LiveTotals, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis at %s: %w", addr, err)
	}
	return &LiveTotals{client: client}, nil
}

func (l *LiveTotals) Close() error { return l.client.Close() }

func (l *LiveTotals) Add(ctx context.Context, marketID string, cell int, amount int64) error {
	if err := l.client.HIncrBy(ctx, key(marketID), strconv.Itoa(cell), amount).Err(); err != nil {
		return fmt.Errorf("increment cell %d of %s: %w", cell, marketID, err)
	}
	return nil
}

func (l *LiveTotals) Totals(ctx context.Context, marketID string) (map[int]int64, error) {
	raw, err := l.client.HGetAll(ctx, key(marketID)).Result()
	if err != nil {
		return nil, fmt.Errorf("read totals for %s: %w", marketID, err)
	}

	totals := make(map[int]int64, len(raw))
	for field, value := range raw {
		cell, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("cell field %q of %s: %w", field, marketID, err)
		}
		amount, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("total %q for cell %d of %s: %w", value, cell, marketID, err)
		}
		totals[cell] = amount
	}
	return totals, nil
}

func (l *LiveTotals) Drop(ctx context.Context, marketID string) error {
	if err := l.client.Del(ctx, key(marketID)).Err(); err != nil {
		return fmt.Errorf("drop totals for %s: %w", marketID, err)
	}
	return nil
}

func key(marketID string) string { return "pool:" + marketID }
