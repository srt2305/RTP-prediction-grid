package market

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/feed"
)

const (
	batchSize    = 200
	flushEvery   = 250 * time.Millisecond
	flushOnClose = 5 * time.Second
)

// Ticks are batched: settlement only needs them durable before expiry, and one
// write per trade would dominate the write load.
type Ingestor struct {
	Feed  PriceFeed
	Store TickWriter
	Log   *slog.Logger
}

func (i Ingestor) Run(ctx context.Context, symbol string) error {
	ticks, err := i.Feed.Subscribe(ctx, symbol)
	if err != nil {
		return err
	}

	flush := time.NewTicker(flushEvery)
	defer flush.Stop()

	batch := make([]feed.Tick, 0, batchSize)
	// Both shutdown paths outlive the context that triggered them: at shutdown
	// ctx.Done and a closed stream are ready together and select picks either.
	finalWrite := func(write func(context.Context) error) error {
		final, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushOnClose)
		defer cancel()
		return write(final)
	}

	write := func(ctx context.Context) error {
		if len(batch) == 0 {
			return nil
		}
		if err := i.Store.SaveTicks(ctx, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			if err := finalWrite(write); err != nil {
				i.Log.Error("final tick flush failed", "symbol", symbol, "err", err)
			}
			return ctx.Err()

		case tick, ok := <-ticks:
			if !ok {
				return errors.Join(finalWrite(write), errors.New("market: tick stream closed"))
			}
			batch = append(batch, tick)
			if len(batch) >= batchSize {
				if err := write(ctx); err != nil {
					return err
				}
			}

		case <-flush.C:
			if err := write(ctx); err != nil {
				return err
			}
		}
	}
}
