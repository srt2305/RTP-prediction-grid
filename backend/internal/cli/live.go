package cli

import (
	"context"
	"flag"
	"log/slog"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/feed"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/market"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/oracle"
)

// Nothing stakes into these markets yet, so they settle empty and void.
func runLive(ctx context.Context, log *slog.Logger, args []string) error {
	cfg := defaultConfig()

	fs := flag.NewFlagSet("live", flag.ExitOnError)
	fs.StringVar(&cfg.symbol, "symbol", cfg.symbol, "exchange symbol")
	fs.DurationVar(&cfg.window, "window", cfg.window, "market window")
	fs.DurationVar(&cfg.freezeBefore, "freeze", cfg.freezeBefore, "freeze this long before expiry")
	fs.DurationVar(&cfg.amendBefore, "amend", cfg.amendBefore, "close withdrawals this long before expiry")
	fs.Float64Var(&cfg.cellWidth, "width", cfg.cellWidth, "cell width in dollars")
	fs.IntVar(&cfg.boundedCells, "cells", cfg.boundedCells, "number of bounded cells")
	fs.IntVar(&cfg.rakeBps, "rake", cfg.rakeBps, "rake in basis points")
	rounds := fs.Int("rounds", 1, "number of consecutive markets to run")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pg, live, err := connect(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pg.Close()
	defer func() { _ = live.Close() }()

	feedCtx, stopFeed := context.WithCancel(ctx)
	defer stopFeed()

	startedAt := time.Now().UTC()
	ingestDone := make(chan error, 1)
	go func() {
		ingestDone <- market.Ingestor{Feed: feed.Binance{Log: log}, Store: pg, Log: log}.Run(feedCtx, cfg.symbol)
	}()

	if err := awaitFreshTick(ctx, pg, cfg.symbol, startedAt); err != nil {
		return err
	}

	runner := &market.Runner{
		Store: pg,
		Live:  live,
		Oracle: oracle.LastTrade{
			Ticks: pg,
			Log:   log,
			Grace: 5 * time.Second,
			Poll:  200 * time.Millisecond,
		},
		Log: log,
	}

	tmpl := market.Template{
		Symbol:       cfg.symbol,
		Window:       cfg.window,
		FreezeBefore: cfg.freezeBefore, AmendBefore: cfg.amendBefore,
		CellWidth:    cfg.cellWidth,
		BoundedCells: cfg.boundedCells,
		RakeBps:      cfg.rakeBps,
	}

	for round := range *rounds {
		spot, err := pg.LatestPrice(ctx, cfg.symbol)
		if err != nil {
			return err
		}

		m, err := runner.Open(ctx, tmpl, spot, time.Now().UTC())
		if err != nil {
			return err
		}
		if _, err := runner.Run(ctx, m); err != nil {
			return err
		}
		log.Info("round complete", "round", round+1, "of", *rounds)
	}

	stopFeed()
	<-ingestDone
	return nil
}
