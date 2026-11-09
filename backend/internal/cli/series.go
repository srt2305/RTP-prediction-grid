package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"text/tabwriter"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/feed"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/market"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/oracle"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/pricing"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/store"
)

const visibleRows = 9

func runSeries(ctx context.Context, log *slog.Logger, args []string) error {
	cfg := defaultConfig()

	fs := flag.NewFlagSet("series", flag.ExitOnError)
	spot := fs.Float64("spot", 76000, "starting spot price")
	sigma := fs.Float64("sigma", 0.55, "annualised volatility")
	speed := fs.Float64("speed", 12, "simulated clock multiplier")
	window := fs.Duration("window", 90*time.Second, "market window")
	freeze := fs.Duration("freeze", 10*time.Second, "freeze before expiry")
	amend := fs.Duration("amend", 25*time.Second, "close withdrawals before expiry")
	interval := fs.Duration("interval", 15*time.Second, "gap between consecutive expiries")
	runFor := fs.Duration("for", 150*time.Second, "how long to run")
	fs.Float64Var(&cfg.cellWidth, "width", 50, "cell width in dollars")
	fs.IntVar(&cfg.boundedCells, "cells", 20, "bounded cells per market")
	fs.IntVar(&cfg.rakeBps, "rake", cfg.rakeBps, "rake in basis points")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pg, live, err := connect(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pg.Close()
	defer func() { _ = live.Close() }()

	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	sim := feed.Simulator{
		Start: *spot, Sigma: *sigma, Interval: 100 * time.Millisecond,
		Speed: *speed, Seed: uint64(time.Now().UnixNano()),
	}

	feedCtx, stopFeed := context.WithCancel(ctx)
	defer stopFeed()

	startedAt := time.Now().UTC()
	ingestDone := make(chan error, 1)
	go func() {
		ingestDone <- market.Ingestor{Feed: sim, Store: pg, Log: quiet}.Run(feedCtx, cfg.symbol)
	}()
	if err := awaitFreshTick(ctx, pg, cfg.symbol, startedAt); err != nil {
		return err
	}

	series := &market.Series{
		Runner: &market.Runner{
			Store: pg, Live: live, Log: quiet,
			Oracle: oracle.LastTrade{Ticks: pg, Log: quiet, Grace: 5 * time.Second,
				Poll: 100 * time.Millisecond},
		},
		Template: market.Template{
			Symbol: cfg.symbol, Window: *window, FreezeBefore: *freeze, AmendBefore: *amend,
			CellWidth: cfg.cellWidth, BoundedCells: cfg.boundedCells, RakeBps: cfg.rakeBps,
		},
		Interval: *interval,
		Log:      log,
	}

	seriesCtx, stopSeries := context.WithCancel(ctx)
	defer stopSeries()
	seriesDone := make(chan error, 1)
	go func() { seriesDone <- series.Run(seriesCtx) }()

	deadline := time.After(*runFor)
	paint := time.NewTicker(3 * time.Second)
	defer paint.Stop()

	staked := make(map[string]bool)
	r := rand.New(rand.NewPCG(7, 11))

	for {
		select {
		case err := <-seriesDone:
			if err != nil && ctx.Err() == nil {
				return err
			}
			return nil

		case <-deadline:
			stopSeries()
			if err := <-seriesDone; err != nil && !errors.Is(err, context.Canceled) {
				log.Error("series stopped early", "err", err)
			}
			stopFeed()
			<-ingestDone
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-paint.C:
			if err := stakeNewMarkets(ctx, series, pg, cfg.symbol, staked, r); err != nil {
				return err
			}
			if err := paintGrid(ctx, series, pg, live, *sigma, *speed); err != nil {
				return err
			}
		}
	}
}

func stakeNewMarkets(ctx context.Context, s *market.Series, pg *store.Postgres,
	symbol string, staked map[string]bool, r *rand.Rand) error {

	spot, err := pg.LatestPrice(ctx, symbol)
	if err != nil {
		return err
	}

	sim, err := pg.EnsureAccount(ctx, "sim")
	if err != nil {
		return err
	}
	if err := pg.Deposit(ctx, sim.ID, 5_000_00); err != nil {
		return err
	}

	for _, m := range s.Live() {
		if staked[m.ID] || !m.StakingOpen(time.Now().UTC()) {
			continue
		}
		staked[m.ID] = true

		centre := m.Grid.CellIndexFor(spot)
		for _, offset := range []int{0, 0, 1, -1, 2, -2, 3} {
			cell := max(0, min(centre+offset, m.Grid.TotalCells()-1))
			amount := int64(2500+r.IntN(60)*500) / 100 * 100
			if _, err := s.Runner.PlaceStake(ctx, m, sim.ID, cell, amount, 0, time.Now().UTC()); err != nil {
				return err
			}
		}
	}
	return nil
}

func paintGrid(ctx context.Context, s *market.Series, pg *store.Postgres,
	live *store.LiveTotals, sigma, speed float64) error {

	cols := s.Live()
	if len(cols) == 0 {
		return nil
	}
	spot, err := pg.LatestPrice(ctx, cols[0].Tmpl.Symbol)
	if err != nil {
		return err
	}

	pools := make([]map[int]int64, len(cols))
	totals := make([]int64, len(cols))
	for i, m := range cols {
		if pools[i], err = live.Totals(ctx, m.ID); err != nil {
			return err
		}
		for _, amount := range pools[i] {
			totals[i] += amount
		}
	}

	// Grids snap to the same price lattice, so rows drawn from the newest
	// market line up with every older column.
	ref := cols[len(cols)-1].Grid
	centre := ref.CellIndexFor(spot)
	width := ref.CellWidth

	fmt.Printf("\n\033[1mBTCUSDT %.2f\033[0m   %d markets open   %s\n",
		spot, len(cols), time.Now().Format("15:04:05"))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprint(w, "  price band\t")
	for _, m := range cols {
		state := "open"
		if !m.StakingOpen(time.Now().UTC()) {
			state = "frozen"
		}
		fmt.Fprintf(w, "%s %s\t", time.Until(m.ExpiresAt).Round(time.Second), state)
	}
	fmt.Fprintln(w)

	for offset := visibleRows / 2; offset >= -visibleRows/2; offset-- {
		idx := centre + offset
		if idx < 1 || idx > ref.BoundedCells {
			continue
		}
		lo := ref.Floor + width*float64(idx-1)

		marker := " "
		if spot >= lo && spot < lo+width {
			marker = ">"
		}
		fmt.Fprintf(w, "%s %9.0f-%.0f\t", marker, lo, lo+width)

		for i, m := range cols {
			cell := m.Grid.CellIndexFor(lo + width/2)
			probs, err := pricing.CellProbabilities(m.Grid, pricing.Params{
				Spot: spot, Sigma: sigma,
				Tau: time.Duration(float64(time.Until(m.ExpiresAt)) * speed),
			})
			if err != nil {
				return err
			}

			if stake := pools[i][cell]; stake > 0 {
				net := float64(totals[i]) * (1 - float64(m.Tmpl.RakeBps)/10000)
				fmt.Fprintf(w, "[%.1fx]\t", net/float64(stake))
				continue
			}
			if p := probs[cell]; p > 0.0002 {
				fmt.Fprintf(w, "%.1fx\t", (1-float64(m.Tmpl.RakeBps)/10000)/p)
				continue
			}
			fmt.Fprint(w, "-\t")
		}
		fmt.Fprintln(w)
	}

	fmt.Fprint(w, "  pool\t")
	for _, total := range totals {
		fmt.Fprintf(w, "$%.0f\t", float64(total)/100)
	}
	fmt.Fprintln(w)
	return w.Flush()
}
