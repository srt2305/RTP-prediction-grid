package api

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/market"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/pricing"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/store"
)

const (
	bandsShown = 13
	// Bands only re-centre once spot leaves the middle of the window. Without
	// the dead zone the whole grid slides a row every time spot crosses a
	// boundary, which is often enough to make a cell hard to click.
	recentreAfter = 3
	// A settled column stays on screen this long so the outcome lands where you
	// were looking rather than the column simply disappearing.
	holdSettled = 18 * time.Second
)

// Snapshot is the whole price/time grid in one message. Bands are the shared
// price rows and every column carries one cell per band, so the client renders
// a matrix without reconciling lattices itself.
type Snapshot struct {
	Symbol  string    `json:"symbol"`
	Spot    float64   `json:"spot"`
	At      time.Time `json:"at"`
	Bands   []Band    `json:"bands"`
	Columns []Column  `json:"columns"`
}

type Band struct {
	Lower  float64  `json:"lower"`
	Upper  *float64 `json:"upper"`
	IsSpot bool     `json:"isSpot"`
}

type Column struct {
	MarketID  string    `json:"marketId"`
	Phase     string    `json:"phase"`
	Outcome   *string   `json:"outcome"`
	SettledAt *float64  `json:"settledPrice"`
	SettledIn *int      `json:"settledCell"`
	AmendsAt  time.Time `json:"amendsAt"`
	FreezesAt time.Time `json:"freezesAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	RakeBps   int       `json:"rakeBps"`
	PoolMinor int64     `json:"poolMinor"`
	Cells     []Cell    `json:"cells"`
}

type Cell struct {
	Index       int      `json:"index"`
	Tradeable   bool     `json:"tradeable"`
	Settled     bool     `json:"settled"`
	Probability float64  `json:"probability"`
	Fair        *float64 `json:"fair"`
	StakedMinor int64    `json:"stakedMinor"`
	Pool        *float64 `json:"pool"`
}

func phaseOf(m market.Market, at time.Time) string {
	switch {
	case m.AmendmentOpen(at):
		return "amendable"
	case m.StakingOpen(at):
		return "committed"
	default:
		return "frozen"
	}
}

// finite keeps JSON valid: Inf and NaN have no encoding, and both arise
// naturally here from unbounded tail cells and zero-probability multipliers.
func finite(f float64) *float64 {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return nil
	}
	return &f
}

func (s *Server) snapshot(ctx context.Context) (Snapshot, error) {
	live := s.Series.Live()
	now := time.Now().UTC()

	snap := Snapshot{Symbol: s.Series.Template.Symbol, At: now,
		Bands: []Band{}, Columns: []Column{}}

	spot, err := s.Store.LatestPrice(ctx, snap.Symbol)
	if err != nil {
		return snap, err
	}
	snap.Spot = spot
	if len(live) == 0 {
		return snap, nil
	}

	width := s.Series.Template.CellWidth
	centre := s.anchorBand(spot, width)

	for offset := bandsShown / 2; offset >= -bandsShown/2; offset-- {
		lower := centre + width*float64(offset)
		if lower <= 0 {
			continue
		}
		upper := lower + width
		snap.Bands = append(snap.Bands, Band{
			Lower: lower, Upper: &upper, IsSpot: spot >= lower && spot < upper,
		})
	}

	settled, err := s.Store.RecentlySettled(ctx, snap.Symbol, holdSettled)
	if err != nil {
		return snap, err
	}
	stillLive := make(map[string]bool, len(live))
	for _, m := range live {
		stillLive[m.ID] = true
	}
	for _, m := range settled {
		// A market is briefly in both sets: settled in Postgres, not yet
		// untracked by the series. Emitting it twice duplicates React keys.
		if stillLive[m.ID] {
			continue
		}
		col, err := s.settledColumn(ctx, m, snap.Bands, width)
		if err != nil {
			return snap, err
		}
		snap.Columns = append(snap.Columns, col)
	}

	for _, m := range live {
		totals, err := s.Live.Totals(ctx, m.ID)
		if err != nil {
			return snap, err
		}
		probs, err := pricing.CellProbabilities(m.Grid, pricing.Params{
			Spot: spot, Sigma: s.Sigma, Tau: time.Until(m.ExpiresAt),
		})
		if err != nil {
			return snap, err
		}
		fair, err := pricing.FairMultipliers(probs, m.Tmpl.RakeBps)
		if err != nil {
			return snap, err
		}

		var pooled int64
		for _, amount := range totals {
			pooled += amount
		}
		net := float64(pooled) * pricing.PayoutShare(m.Tmpl.RakeBps)

		col := Column{
			MarketID: m.ID, Phase: phaseOf(m, now),
			AmendsAt: m.AmendsAt, FreezesAt: m.FreezesAt, ExpiresAt: m.ExpiresAt,
			RakeBps: m.Tmpl.RakeBps, PoolMinor: pooled,
			Cells: make([]Cell, 0, len(snap.Bands)),
		}

		for _, b := range snap.Bands {
			mid := b.Lower + width/2
			idx := m.Grid.CellIndexFor(mid)

			// A band beyond this market's own grid collapses onto a tail cell.
			// Showing it as that cell would present several distinct bands as
			// the same position, so it is marked untradeable instead.
			cell := Cell{Index: idx, Tradeable: mid >= m.Grid.Floor && mid < m.Grid.Ceiling()}
			if cell.Tradeable {
				cell.Probability = probs[idx]
				cell.Fair = finite(fair[idx])
				cell.StakedMinor = totals[idx]
				if staked := totals[idx]; staked > 0 {
					cell.Pool = finite(net / float64(staked))
				}
			}
			col.Cells = append(col.Cells, cell)
		}
		snap.Columns = append(snap.Columns, col)
	}

	// ExpiresAt never changes once a market opens, so ordering columns by it
	// keeps a market's position stable as it moves from live to settled —
	// nothing reflows out from under whoever's watching it resolve.
	slices.SortFunc(snap.Columns, func(a, b Column) int { return a.ExpiresAt.Compare(b.ExpiresAt) })
	return snap, nil
}

// anchorBand returns the lower bound of the middle row, held steady until spot
// drifts recentreAfter bands away from it.
func (s *Server) anchorBand(spot, width float64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	onLattice := math.Floor(spot/width) * width
	if s.anchor == 0 || math.Abs(spot-s.anchor) > width*recentreAfter {
		s.anchor = onLattice
	}
	return s.anchor
}

func (s *Server) settledColumn(ctx context.Context, m store.MarketDetail, bands []Band, width float64) (Column, error) {
	totals, err := s.Store.SettledTotals(ctx, m.ID)
	if err != nil {
		return Column{}, err
	}

	col := Column{
		MarketID: m.ID, Phase: "settled", Outcome: m.Outcome,
		SettledAt: m.SettledPrice, SettledIn: m.SettledCell,
		AmendsAt: m.AmendsAt, FreezesAt: m.FreezesAt, ExpiresAt: m.ExpiresAt,
		RakeBps: m.RakeBps, Cells: make([]Cell, 0, len(bands)),
	}
	for _, amount := range totals {
		col.PoolMinor += amount
	}

	grid := pricing.Grid{Floor: m.FloorPrice, CellWidth: m.CellWidth, BoundedCells: m.BoundedCells}
	net := float64(col.PoolMinor) * pricing.PayoutShare(m.RakeBps)

	for _, b := range bands {
		mid := b.Lower + width/2
		idx := grid.CellIndexFor(mid)
		cell := Cell{Index: idx, Tradeable: mid >= grid.Floor && mid < grid.Ceiling()}
		if !cell.Tradeable {
			col.Cells = append(col.Cells, cell)
			continue
		}
		cell.StakedMinor = totals[idx]
		cell.Settled = m.SettledCell != nil && *m.SettledCell == idx
		if cell.StakedMinor > 0 {
			cell.Pool = finite(net / float64(cell.StakedMinor))
		}
		col.Cells = append(col.Cells, cell)
	}
	return col, nil
}
