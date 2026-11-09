package api

import (
	"net/http"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/pricing"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/store"
)

// Watch is everything one screen needs to follow a position to its conclusion:
// the market's clock, the price path so far, the band being watched, and once
// it settles, whether that band was hit and what it paid.
type Watch struct {
	Market store.MarketDetail `json:"market"`
	Path   []store.PricePoint `json:"path"`
	Spot   float64            `json:"spot"`

	Lower  *float64 `json:"lower"`
	Upper  *float64 `json:"upper"`
	Cell   int      `json:"cell"`
	Fair   *float64 `json:"fair"`
	Pool   *float64 `json:"pool"`
	Staked int64    `json:"stakedMinor"`

	PoolMinor int64 `json:"poolTotalMinor"`

	MyStakeMinor int64  `json:"myStakeMinor"`
	MyPaidMinor  int64  `json:"myPaidMinor"`
	Resolved     bool   `json:"resolved"`
	Hit          *bool  `json:"hit"`
	StakeID      string `json:"stakeId"`
}

func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	detail, err := s.Store.Market(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, http.StatusNotFound, err)
		return
	}

	cell, err := intParam(r, "cell")
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}

	grid := pricing.Grid{Floor: detail.FloorPrice, CellWidth: detail.CellWidth,
		BoundedCells: detail.BoundedCells}
	cells := grid.Cells()
	if cell < 0 || cell >= len(cells) {
		s.fail(w, http.StatusBadRequest, errBadCell)
		return
	}

	out := Watch{Market: detail, Cell: cell,
		Lower: finite(cells[cell].Lower), Upper: finite(cells[cell].Upper)}

	// A settled market's chart must stop at expiry, not run on to now.
	until := time.Now().UTC()
	if detail.SettledPrice != nil && until.After(detail.ExpiresAt) {
		until = detail.ExpiresAt
	}
	if out.Path, err = s.Store.PricePath(ctx, detail.Symbol, detail.OpensAt, until, 180); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if len(out.Path) > 0 {
		out.Spot = out.Path[len(out.Path)-1].Price
	}

	// Redis is dropped at settlement, so a finished market reads its totals
	// back from the stake rows instead.
	totals, err := s.Live.Totals(ctx, detail.ID)
	if err == nil && len(totals) == 0 && detail.Outcome != nil {
		totals, err = s.Store.SettledTotals(ctx, detail.ID)
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	for _, amount := range totals {
		out.PoolMinor += amount
	}
	out.Staked = totals[cell]

	if out.Spot > 0 {
		probs, err := pricing.CellProbabilities(grid, pricing.Params{
			Spot: out.Spot, Sigma: s.Sigma, Tau: time.Until(detail.ExpiresAt),
		})
		if err == nil {
			fair, ferr := pricing.FairMultipliers(probs, detail.RakeBps)
			if ferr == nil {
				out.Fair = finite(fair[cell])
			}
		}
	}
	if out.Staked > 0 {
		net := float64(out.PoolMinor) * pricing.PayoutShare(detail.RakeBps)
		out.Pool = finite(net / float64(out.Staked))
	}

	if stakeID := r.URL.Query().Get("stake"); stakeID != "" {
		out.StakeID = stakeID
		outcome, err := s.Store.StakeOutcome(ctx, stakeID)
		if err != nil {
			s.fail(w, http.StatusInternalServerError, err)
			return
		}
		out.MyStakeMinor = outcome.StakedMinor
		out.MyPaidMinor = outcome.PaidMinor
		out.Resolved = outcome.Resolved
		if outcome.Resolved {
			hit := detail.SettledCell != nil && *detail.SettledCell == outcome.Cell
			out.Hit = &hit
		}
	}

	writeJSON(w, http.StatusOK, out)
}
