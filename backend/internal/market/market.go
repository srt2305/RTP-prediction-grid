package market

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/feed"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/pool"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/pricing"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/store"
)

// Declared here, where they are consumed: implementations never import this
// package, so dependencies point one way.
type PriceFeed interface {
	Subscribe(ctx context.Context, symbol string) (<-chan feed.Tick, error)
}

type SettlementOracle interface {
	OfficialPrice(ctx context.Context, symbol string, at time.Time) (float64, error)
}

type TickWriter interface {
	SaveTicks(ctx context.Context, ticks []feed.Tick) error
}

type Store interface {
	CreateMarket(ctx context.Context, m store.MarketRow) error
	SetMarketState(ctx context.Context, id, state string) error
	LatestPrice(ctx context.Context, symbol string) (float64, error)
	PlaceStake(ctx context.Context, stakeID, marketID, accountID string,
		cell int, amount int64, minMultiple float64, rakeBps int, at time.Time) error
	CancelStake(ctx context.Context, stakeID, marketID string, at time.Time) (int, int64, error)
	Stakes(ctx context.Context, marketID string) ([]pool.Stake, error)
	RecordSettlement(ctx context.Context, marketID, state string, price float64, cell int, s pool.Settlement) error
	RecordVoid(ctx context.Context, marketID, state string, s pool.Settlement) error
}

type State string

const (
	StateOpen    State = "open"
	StateFrozen  State = "frozen"
	StateSettled State = "settled"
	StateVoided  State = "voided"
)

// Both deadlines are measured back from expiry. See PLAN.md for why
// cancellation closes before staking.
type Template struct {
	Symbol       string
	Window       time.Duration
	FreezeBefore time.Duration
	AmendBefore  time.Duration
	CellWidth    float64
	BoundedCells int
	RakeBps      int
}

func (t Template) Validate() error {
	switch {
	case t.Symbol == "":
		return errors.New("market: template needs a symbol")
	case t.Window <= 0:
		return errors.New("market: window must be positive")
	case t.FreezeBefore < 0:
		return errors.New("market: freeze window must not be negative")
	case t.FreezeBefore >= t.Window:
		return fmt.Errorf("market: freeze window %s leaves no time to stake in a %s window", t.FreezeBefore, t.Window)
	case t.AmendBefore < t.FreezeBefore:
		return fmt.Errorf("market: withdrawals must close before the freeze, got amend %s against freeze %s", t.AmendBefore, t.FreezeBefore)
	case t.AmendBefore >= t.Window:
		return fmt.Errorf("market: amend window %s leaves no time to stake in a %s window", t.AmendBefore, t.Window)
	case t.CellWidth <= 0:
		return errors.New("market: cell width must be positive")
	case t.BoundedCells < 1:
		return errors.New("market: needs at least one bounded cell")
	case t.RakeBps < 0 || t.RakeBps >= 10000:
		return fmt.Errorf("market: rake %d bps outside [0, 10000)", t.RakeBps)
	}
	return nil
}

type Market struct {
	ID   string
	Tmpl Template
	Grid pricing.Grid

	OpensAt   time.Time
	AmendsAt  time.Time
	FreezesAt time.Time
	ExpiresAt time.Time
}

func (m Market) StakingOpen(at time.Time) bool {
	return !at.Before(m.OpensAt) && at.Before(m.FreezesAt)
}

func (m Market) AmendmentOpen(at time.Time) bool {
	return !at.Before(m.OpensAt) && at.Before(m.AmendsAt)
}
