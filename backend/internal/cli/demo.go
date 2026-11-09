package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/feed"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/market"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/oracle"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/pool"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/pricing"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/store"
)

type position struct {
	account   string
	accountID string
	stakeID   string
	cell      int
	amount    int64
	withdrawn bool
}

func runDemo(ctx context.Context, log *slog.Logger, args []string) error {
	cfg := defaultConfig()

	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	spot := fs.Float64("spot", 65000, "starting spot price")
	sigma := fs.Float64("sigma", 0.6, "annualised volatility of the simulated process")
	speed := fs.Float64("speed", 15, "simulated clock multiplier")
	window := fs.Duration("window", 20*time.Second, "market window")
	freeze := fs.Duration("freeze", 5*time.Second, "freeze this long before expiry")
	amend := fs.Duration("amend", 10*time.Second, "close withdrawals this long before expiry")
	rake := fs.Int("rake", cfg.rakeBps, "rake in basis points")
	fs.Float64Var(&cfg.cellWidth, "width", 50, "cell width in dollars")
	fs.IntVar(&cfg.boundedCells, "cells", 20, "number of bounded cells")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pg, live, err := connect(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pg.Close()
	defer func() { _ = live.Close() }()

	sim := feed.Simulator{
		Start:    *spot,
		Sigma:    *sigma,
		Interval: 100 * time.Millisecond,
		Speed:    *speed,
		Seed:     uint64(time.Now().UnixNano()),
	}

	feedCtx, stopFeed := context.WithCancel(ctx)
	defer stopFeed()

	startedAt := time.Now().UTC()
	ingestDone := make(chan error, 1)
	go func() {
		ingestDone <- market.Ingestor{Feed: sim, Store: pg, Log: log}.Run(feedCtx, cfg.symbol)
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
			Poll:  100 * time.Millisecond,
		},
		Log: log,
	}

	openingSpot, err := pg.LatestPrice(ctx, cfg.symbol)
	if err != nil {
		return err
	}

	m, err := runner.Open(ctx, market.Template{
		Symbol:       cfg.symbol,
		Window:       *window,
		FreezeBefore: *freeze, AmendBefore: *amend,
		CellWidth:    cfg.cellWidth,
		BoundedCells: cfg.boundedCells,
		RakeBps:      *rake,
	}, openingSpot, time.Now().UTC())
	if err != nil {
		return err
	}

	if err := printIndicativeOdds(m, openingSpot, *sigma, sim.MarketTime(time.Until(m.ExpiresAt))); err != nil {
		return err
	}

	placed, err := placeDemoPositions(ctx, pg, runner, m, openingSpot)
	if err != nil {
		return err
	}
	if err := printLiveTotals(ctx, live, m); err != nil {
		return err
	}
	if err := demonstratePhases(ctx, pg, runner, m, live, placed, log); err != nil {
		return err
	}

	result, err := runner.Run(ctx, m)
	if err != nil {
		return err
	}

	stopFeed()
	<-ingestDone

	return printSettlement(result, placed)
}

// Without the since bound this accepts a tick left by an earlier run and opens
// the market centred on a stale price.
func awaitFreshTick(ctx context.Context, pg *store.Postgres, symbol string, since time.Time) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		fresh, err := pg.HasTicksAfter(ctx, symbol, since)
		if err != nil {
			return err
		}
		if fresh {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no ticks for %s within 10s of starting the feed", symbol)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func printIndicativeOdds(m market.Market, spot, sigma float64, tau time.Duration) error {
	probs, err := pricing.CellProbabilities(m.Grid, pricing.Params{Spot: spot, Sigma: sigma, Tau: tau})
	if err != nil {
		return err
	}
	multipliers, err := pricing.FairMultipliers(probs, m.Tmpl.RakeBps)
	if err != nil {
		return err
	}

	fmt.Printf("\nindicative odds at open (spot %.2f, sigma %.2f, %s of market time to expiry)\n",
		spot, sigma, tau.Round(time.Second))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "cell\trange\tprobability\tmultiplier\t")
	for i, c := range m.Grid.Cells() {
		if probs[i] < 1e-4 {
			continue
		}
		fmt.Fprintf(w, "%d\t%s\t%.4f\t%s\t\n", c.Index, cellRange(c), probs[i], multiplier(multipliers[i]))
	}
	return w.Flush()
}

func placeDemoPositions(ctx context.Context, pg *store.Postgres, r *market.Runner, m market.Market, spot float64) ([]position, error) {
	centre := m.Grid.CellIndexFor(spot)
	positions := []position{
		{account: "alice", cell: centre, amount: 25_000},
		{account: "bob", cell: centre, amount: 75_000},
		{account: "carol", cell: clampCell(m, centre+1), amount: 40_000},
		{account: "dan", cell: clampCell(m, centre-1), amount: 10_000},
		{account: "erin", cell: clampCell(m, centre+2), amount: 5_000},
		{account: "frank", cell: clampCell(m, centre-2), amount: 30_000},
		{account: "grace", cell: clampCell(m, centre+3), amount: 15_000},
		{account: "heidi", cell: clampCell(m, centre-3), amount: 20_000},
	}

	fmt.Printf("\nplacing %d positions\n\n", len(positions))
	for i, p := range positions {
		account, err := fund(ctx, pg, p.account, 100_000)
		if err != nil {
			return nil, err
		}
		positions[i].accountID = account.ID

		id, err := r.PlaceStake(ctx, m, account.ID, p.cell, p.amount, 0, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		positions[i].stakeID = id
	}
	return positions, nil
}

func fund(ctx context.Context, pg *store.Postgres, handle string, amount int64) (store.Account, error) {
	account, err := pg.EnsureAccount(ctx, handle)
	if err != nil {
		return store.Account{}, err
	}
	return account, pg.Deposit(ctx, account.ID, amount)
}

func clampCell(m market.Market, cell int) int {
	return max(0, min(cell, m.Grid.TotalCells()-1))
}

func printLiveTotals(ctx context.Context, live *store.LiveTotals, m market.Market) error {
	totals, err := live.Totals(ctx, m.ID)
	if err != nil {
		return err
	}

	var staked int64
	for _, amount := range totals {
		staked += amount
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "cell\trange\tstaked\tshare of pool\t")
	for _, c := range m.Grid.Cells() {
		amount, ok := totals[c.Index]
		if !ok {
			continue
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%.1f%%\t\n",
			c.Index, cellRange(c), money(amount), 100*float64(amount)/float64(staked))
	}
	fmt.Fprintf(w, "\tpool\t%s\t\t\n", money(staked))
	return w.Flush()
}

func demonstratePhases(ctx context.Context, pg *store.Postgres, r *market.Runner, m market.Market,
	live *store.LiveTotals, placed []position, log *slog.Logger) error {

	fmt.Printf("\namendable until %s, frozen at %s\n",
		m.AmendsAt.Format("15:04:05"), m.FreezesAt.Format("15:04:05"))

	victim := &placed[len(placed)-1]
	if err := r.CancelPosition(ctx, m, victim.stakeID, time.Now().UTC()); err != nil {
		return fmt.Errorf("cancellation refused while amendable: %w", err)
	}
	victim.withdrawn = true
	fmt.Printf("  amendable  cancelled %s from cell %d\n", money(victim.amount), victim.cell)
	if err := printLiveTotals(ctx, live, m); err != nil {
		return err
	}

	if err := sleepUntil(ctx, m.AmendsAt.Add(100*time.Millisecond)); err != nil {
		return err
	}
	if err := r.CancelPosition(ctx, m, placed[0].stakeID, time.Now().UTC()); !errors.Is(err, market.ErrAmendmentClosed) {
		return fmt.Errorf("cancelling after %s should report ErrAmendmentClosed, got %v", m.AmendsAt, err)
	}
	late, err := fund(ctx, pg, "late", 20_000)
	if err != nil {
		return err
	}
	id, err := r.PlaceStake(ctx, m, late.ID, placed[0].cell, 20_000, 0, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("stake refused while still open: %w", err)
	}
	fmt.Printf("  committed  cancellation refused, new $200.00 position accepted (%s)\n", id[:8])

	if err := sleepUntil(ctx, m.FreezesAt.Add(100*time.Millisecond)); err != nil {
		return err
	}
	arb, err := fund(ctx, pg, "latency-arb", 1_000_000)
	if err != nil {
		return err
	}
	if _, err := r.PlaceStake(ctx, m, arb.ID, 0, 1_000_000, 0, time.Now().UTC()); !errors.Is(err, market.ErrStakingClosed) {
		return fmt.Errorf("staking after %s should report ErrStakingClosed, got %v", m.FreezesAt, err)
	}
	fmt.Println("  frozen     both refused")
	log.Info("phase checks passed", "market", m.ID)
	return nil
}

func printSettlement(result market.Result, placed []position) error {
	s := result.Settlement
	fmt.Printf("\nsettled at %.2f in cell %d, outcome %q\n", result.OfficialPrice, result.SettledCell, s.Outcome)
	fmt.Printf("pool %s, rake %s at %d bps, paid out %s\n\n",
		money(s.TotalStaked), money(s.Rake), result.Market.Tmpl.RakeBps, money(totalPaid(s.Payouts)))

	paid := make(map[string]int64, len(s.Payouts))
	for _, p := range s.Payouts {
		paid[p.StakeID] = p.Amount
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "account\tcell\tstaked\tpaid\tnet\t")
	for _, p := range placed {
		if p.withdrawn {
			fmt.Fprintf(w, "%s\t%d\t%s\twithdrawn\t%s\t\n",
				p.account, p.cell, money(p.amount), signedMoney(0))
			continue
		}
		amount := paid[p.stakeID]
		delete(paid, p.stakeID)
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t\n",
			p.account, p.cell, money(p.amount), money(amount), signedMoney(amount-p.amount))
	}
	for id, amount := range paid {
		fmt.Fprintf(w, "%s\t\t\t%s\t\t\n", id[:8], money(amount))
	}
	fmt.Fprintf(w, "house\t\t\t%s\t%s\t\n", money(s.Rake), signedMoney(s.Rake))
	if err := w.Flush(); err != nil {
		return err
	}

	if conserved := totalPaid(s.Payouts) + s.Rake; conserved != s.TotalStaked {
		return fmt.Errorf("pool not conserved: payouts+rake %d != staked %d", conserved, s.TotalStaked)
	}
	fmt.Println("\npool conserved: payouts + rake == total staked")
	return nil
}

func totalPaid(payouts []pool.Payout) int64 {
	var sum int64
	for _, p := range payouts {
		sum += p.Amount
	}
	return sum
}

func money(minor int64) string { return fmt.Sprintf("$%.2f", float64(minor)/100) }

func signedMoney(minor int64) string { return fmt.Sprintf("%+.2f", float64(minor)/100) }

func sleepUntil(ctx context.Context, t time.Time) error {
	d := time.Until(t)
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
