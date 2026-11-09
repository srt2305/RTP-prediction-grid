package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/api"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/feed"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/market"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/oracle"
)

func runServe(ctx context.Context, log *slog.Logger, args []string) error {
	cfg := defaultConfig()

	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	live := fs.Bool("live", false, "ingest real Binance trades instead of the simulator")
	spot := fs.Float64("spot", 76000, "simulator starting price")
	sigma := fs.Float64("sigma", 0.55, "annualised volatility used for indicative pricing")
	speed := fs.Float64("speed", 1, "simulator clock multiplier")
	fs.StringVar(&cfg.symbol, "symbol", cfg.symbol, "exchange symbol")
	fs.DurationVar(&cfg.window, "window", 90*time.Second, "market window")
	fs.DurationVar(&cfg.freezeBefore, "freeze", cfg.freezeBefore, "freeze before expiry")
	fs.DurationVar(&cfg.amendBefore, "amend", cfg.amendBefore, "close cancellation before expiry")
	interval := fs.Duration("interval", 15*time.Second, "gap between consecutive expiries")
	fs.Float64Var(&cfg.cellWidth, "width", 0, "cell width in dollars; 0 sizes it from sigma and the window")
	fs.IntVar(&cfg.boundedCells, "cells", cfg.boundedCells, "bounded cells per market")
	fs.IntVar(&cfg.rakeBps, "rake", cfg.rakeBps, "rake in basis points")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// A grid whose cells are wide relative to the move leaves most rows
	// unreachable; too narrow and the price walks straight out of it. Aim for
	// the visible bands to span roughly three sigma either side.
	if cfg.cellWidth <= 0 {
		sd := *spot * *sigma * math.Sqrt(cfg.window.Seconds()/(365*24*3600))
		cfg.cellWidth = roundToNice(sd / 2.5)
		log.Info("cell width sized from volatility", "sigma", *sigma,
			"window", cfg.window, "one_sigma", math.Round(sd*100)/100, "width", cfg.cellWidth)
	}

	pg, totals, err := connect(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pg.Close()
	defer func() { _ = totals.Close() }()

	var source market.PriceFeed = feed.Simulator{
		Start: *spot, Sigma: *sigma, Interval: 250 * time.Millisecond,
		Speed: *speed, Seed: uint64(time.Now().UnixNano()),
	}
	if *live {
		source = feed.Binance{Log: log}
	}

	feedCtx, stopFeed := context.WithCancel(ctx)
	defer stopFeed()

	startedAt := time.Now().UTC()
	ingestDone := make(chan error, 1)
	go func() {
		ingestDone <- market.Ingestor{Feed: source, Store: pg, Log: log}.Run(feedCtx, cfg.symbol)
	}()
	if err := awaitFreshTick(ctx, pg, cfg.symbol, startedAt); err != nil {
		return err
	}

	series := &market.Series{
		Runner: &market.Runner{
			Store: pg, Live: totals, Log: log,
			Oracle: oracle.LastTrade{Ticks: pg, Log: log, Grace: 5 * time.Second,
				Poll: 200 * time.Millisecond},
		},
		Template: market.Template{
			Symbol: cfg.symbol, Window: cfg.window, FreezeBefore: cfg.freezeBefore,
			AmendBefore: cfg.amendBefore, CellWidth: cfg.cellWidth,
			BoundedCells: cfg.boundedCells, RakeBps: cfg.rakeBps,
		},
		Interval: *interval,
		Log:      log,
	}

	seriesCtx, stopSeries := context.WithCancel(ctx)
	defer stopSeries()
	go func() {
		if err := series.Run(seriesCtx); err != nil && seriesCtx.Err() == nil {
			log.Error("series stopped", "err", err)
		}
	}()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           (&api.Server{Series: series, Store: pg, Live: totals, Sigma: *sigma, Log: log}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	feedKind := "simulated"
	if *live {
		feedKind = "binance"
	}
	log.Info("serving", "addr", *addr, "symbol", cfg.symbol, "feed", feedKind,
		"window", cfg.window, "interval", *interval)
	fmt.Printf("\n  grid      http://localhost%s/api/grid\n", *addr)
	fmt.Printf("  health    http://localhost%s/api/health\n", *addr)
	fmt.Printf("  stream    ws://localhost%s/ws\n\n", *addr)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	stopFeed()
	<-ingestDone
	return nil
}

// roundToNice snaps a width to 1, 2 or 5 times a power of ten so price bands
// read as round numbers.
func roundToNice(v float64) float64 {
	if v <= 0 {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(v)))
	switch step := v / mag; {
	case step < 1.5:
		return mag
	case step < 3.5:
		return 2 * mag
	case step < 7.5:
		return 5 * mag
	default:
		return 10 * mag
	}
}
