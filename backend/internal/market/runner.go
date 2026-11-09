package market

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/pool"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/pricing"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/store"
)

var (
	ErrStakingClosed   = errors.New("market: staking is closed")
	ErrAmendmentClosed = errors.New("market: cancellation is closed")
)

type Runner struct {
	Store  Store
	Live   *store.LiveTotals
	Oracle SettlementOracle
	Log    *slog.Logger
}

type Result struct {
	Market        Market
	State         State
	OfficialPrice float64
	SettledCell   int
	Settlement    pool.Settlement
}

func (r *Runner) Open(ctx context.Context, tmpl Template, spot float64, now time.Time) (Market, error) {
	if err := tmpl.Validate(); err != nil {
		return Market{}, err
	}

	grid := pricing.GridAround(spot, tmpl.CellWidth, tmpl.BoundedCells)
	if err := grid.Validate(); err != nil {
		return Market{}, err
	}

	m := Market{
		ID:        uuid.NewString(),
		Tmpl:      tmpl,
		Grid:      grid,
		OpensAt:   now,
		AmendsAt:  now.Add(tmpl.Window - tmpl.AmendBefore),
		FreezesAt: now.Add(tmpl.Window - tmpl.FreezeBefore),
		ExpiresAt: now.Add(tmpl.Window),
	}

	if err := r.Store.CreateMarket(ctx, store.MarketRow{
		ID:           m.ID,
		Symbol:       tmpl.Symbol,
		State:        string(StateOpen),
		FloorPrice:   grid.Floor,
		CellWidth:    grid.CellWidth,
		BoundedCells: grid.BoundedCells,
		RakeBps:      tmpl.RakeBps,
		OpensAt:      m.OpensAt,
		AmendsAt:     m.AmendsAt,
		FreezesAt:    m.FreezesAt,
		ExpiresAt:    m.ExpiresAt,
	}); err != nil {
		return Market{}, err
	}

	r.Log.Info("market opened",
		"market", m.ID, "symbol", tmpl.Symbol, "spot", spot,
		"floor", grid.Floor, "ceiling", grid.Ceiling(),
		"cells", grid.TotalCells(), "amends_at", m.AmendsAt,
		"freezes_at", m.FreezesAt, "expires_at", m.ExpiresAt)
	return m, nil
}

// The freeze defeats latency arbitrage: a faster feed would otherwise see the
// settling move first and stake on an outcome already determined.
func (r *Runner) PlaceStake(ctx context.Context, m Market, accountID string, cell int, amount int64, minMultiple float64, now time.Time) (string, error) {
	if !m.StakingOpen(now) {
		return "", fmt.Errorf("%w: market %s froze at %s", ErrStakingClosed, m.ID, m.FreezesAt.Format(time.RFC3339))
	}
	if cell < 0 || cell >= m.Grid.TotalCells() {
		return "", fmt.Errorf("market: cell %d outside grid of %d cells", cell, m.Grid.TotalCells())
	}
	if amount <= 0 {
		return "", fmt.Errorf("market: stake amount %d must be positive", amount)
	}

	id := uuid.NewString()
	if err := r.Store.PlaceStake(ctx, id, m.ID, accountID, cell, amount, minMultiple, m.Tmpl.RakeBps, now); err != nil {
		return "", err
	}
	// Committed. Redis only feeds the display, and returning an error here
	// would invite a retry that stakes the account twice.
	if err := r.Live.Add(ctx, m.ID, cell, amount); err != nil {
		r.Log.Error("live totals stale after stake", "market", m.ID, "stake", id, "err", err)
	}
	return id, nil
}

// Cancellation closes before staking so it is never the last move on the book;
// otherwise pulling out late is a spoofing tool.
func (r *Runner) CancelPosition(ctx context.Context, m Market, stakeID string, now time.Time) error {
	if !m.AmendmentOpen(now) {
		return fmt.Errorf("%w: market %s stopped accepting cancellations at %s",
			ErrAmendmentClosed, m.ID, m.AmendsAt.Format(time.RFC3339))
	}

	cell, amount, err := r.Store.CancelStake(ctx, stakeID, m.ID, now)
	if err != nil {
		return err
	}
	if err := r.Live.Add(ctx, m.ID, cell, -amount); err != nil {
		r.Log.Error("live totals stale after cancel", "market", m.ID, "stake", stakeID, "err", err)
	}

	r.Log.Info("position cancelled", "market", m.ID, "stake", stakeID,
		"cell", cell, "amount_minor", amount)
	return nil
}

func (r *Runner) Run(ctx context.Context, m Market) (Result, error) {
	if err := sleepUntil(ctx, m.FreezesAt); err != nil {
		return Result{}, err
	}
	if err := r.Store.SetMarketState(ctx, m.ID, string(StateFrozen)); err != nil {
		return Result{}, err
	}
	r.Log.Info("market frozen", "market", m.ID, "expires_in", time.Until(m.ExpiresAt))

	if err := sleepUntil(ctx, m.ExpiresAt); err != nil {
		return Result{}, err
	}
	return r.Settle(ctx, m)
}

func (r *Runner) Settle(ctx context.Context, m Market) (Result, error) {
	price, err := r.Oracle.OfficialPrice(ctx, m.Tmpl.Symbol, m.ExpiresAt)
	if err != nil {
		if voidErr := r.void(ctx, m); voidErr != nil {
			return Result{}, errors.Join(err, voidErr)
		}
		return Result{Market: m, State: StateVoided}, fmt.Errorf("settling %s: %w", m.ID, err)
	}

	stakes, err := r.Store.Stakes(ctx, m.ID)
	if err != nil {
		return Result{}, err
	}

	cell := m.Grid.CellIndexFor(price)
	settlement, err := pool.Settle(stakes, cell, m.Tmpl.RakeBps)
	if err != nil {
		return Result{}, fmt.Errorf("settling %s: %w", m.ID, err)
	}

	state := StateSettled
	if settlement.Outcome == pool.OutcomeVoid {
		state = StateVoided
	}
	if err := r.Store.RecordSettlement(ctx, m.ID, string(state), price, cell, settlement); err != nil {
		return Result{}, err
	}
	if err := r.Live.Drop(ctx, m.ID); err != nil {
		r.Log.Error("live totals not dropped after settlement", "market", m.ID, "err", err)
	}

	r.Log.Info("market settled",
		"market", m.ID, "official_price", price, "cell", cell,
		"outcome", settlement.Outcome, "staked_minor", settlement.TotalStaked,
		"rake_minor", settlement.Rake, "payouts", len(settlement.Payouts))

	return Result{
		Market:        m,
		State:         state,
		OfficialPrice: price,
		SettledCell:   cell,
		Settlement:    settlement,
	}, nil
}

// void refunds every stake. The ledger already debited them, so leaving a
// market unsettled would destroy the money rather than merely strand it.
func (r *Runner) void(ctx context.Context, m Market) error {
	stakes, err := r.Store.Stakes(ctx, m.ID)
	if err != nil {
		return err
	}

	refund := pool.Refund(stakes)
	if err := r.Store.RecordVoid(ctx, m.ID, string(StateVoided), refund); err != nil {
		return err
	}

	r.Log.Warn("market voided, stakes refunded", "market", m.ID,
		"refunded_minor", refund.TotalStaked, "positions", len(refund.Payouts))

	if err := r.Live.Drop(ctx, m.ID); err != nil {
		r.Log.Error("live totals not dropped for voided market", "market", m.ID, "err", err)
	}
	return nil
}

func sleepUntil(ctx context.Context, t time.Time) error {
	timer := time.NewTimer(time.Until(t))
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
