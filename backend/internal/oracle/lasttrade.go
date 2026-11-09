package oracle

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const defaultPoll = 100 * time.Millisecond

type TickSource interface {
	PriceAt(ctx context.Context, symbol string, at time.Time) (float64, error)
	HasTicksAfter(ctx context.Context, symbol string, at time.Time) (bool, error)
}

// LastTrade settles on the final trade at or before expiry. It waits for a tick
// strictly after expiry first, so the price is not a function of feed latency.
type LastTrade struct {
	Ticks TickSource
	Log   *slog.Logger
	Grace time.Duration
	Poll  time.Duration
}

func (o LastTrade) OfficialPrice(ctx context.Context, symbol string, at time.Time) (float64, error) {
	if err := o.awaitStreamPast(ctx, symbol, at); err != nil {
		return 0, err
	}

	price, err := o.Ticks.PriceAt(ctx, symbol, at)
	if err != nil {
		return 0, fmt.Errorf("official price for %s at %s: %w", symbol, at.Format(time.RFC3339Nano), err)
	}
	return price, nil
}

func (o LastTrade) awaitStreamPast(ctx context.Context, symbol string, at time.Time) error {
	poll := o.Poll
	if poll <= 0 {
		poll = defaultPoll
	}

	deadline := time.Now().Add(o.Grace)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		advanced, err := o.Ticks.HasTicksAfter(ctx, symbol, at)
		if err != nil {
			return err
		}
		if advanced {
			return nil
		}
		if time.Now().After(deadline) {
			o.Log.Warn("settling without a tick past expiry",
				"symbol", symbol, "expiry", at, "grace", o.Grace)
			return nil
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
