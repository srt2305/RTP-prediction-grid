package api

import (
	"context"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/pricing"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/store"
)

// Buckets are finer where the action is: most cells price under 20%, and a
// single 0-20% bucket would hide every miscalibration inside it.
var buckets = []float64{0, 0.01, 0.025, 0.05, 0.10, 0.20, 0.35, 0.55, 1.01}

type Performance struct {
	Summary     Summary   `json:"summary"`
	Calibration []Bucket  `json:"calibration"`
	Markets     int       `json:"marketsAnalysed"`
	Sigma       float64   `json:"sigma"`
	GeneratedAt time.Time `json:"generatedAt"`
}

type Summary struct {
	Positions     int     `json:"positions"`
	StakedMinor   int64   `json:"stakedMinor"`
	ReturnedMinor int64   `json:"returnedMinor"`
	NetMinor      int64   `json:"netMinor"`
	Hits          int     `json:"hits"`
	Refunds       int     `json:"refunds"`
	Misses        int     `json:"misses"`
	HitRate       float64 `json:"hitRate"`
	Expectancy    float64 `json:"expectancyPerDollar"`
}

// Bucket compares what the model predicted against what actually happened, for
// every cell of every settled market. Predicted is the mean probability the
// model assigned; Actual is how often those cells really settled.
type Bucket struct {
	Lower     float64 `json:"lower"`
	Upper     float64 `json:"upper"`
	Cells     int     `json:"cells"`
	Predicted float64 `json:"predicted"`
	Hits      int     `json:"hits"`
	Actual    float64 `json:"actual"`
}

func (s *Server) performance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := Performance{Sigma: s.Sigma, GeneratedAt: time.Now().UTC(), Calibration: []Bucket{}}

	if handle := r.URL.Query().Get("account"); handle != "" {
		account, found, err := s.Store.LookupAccount(ctx, handle)
		if err != nil {
			s.fail(w, http.StatusInternalServerError, err)
			return
		}
		if found {
			positions, err := s.Store.Positions(ctx, account.ID, 500)
			if err != nil {
				s.fail(w, http.StatusInternalServerError, err)
				return
			}
			out.Summary = summarise(positions)
		}
	}

	markets, err := s.Store.SettledMarkets(ctx, 400)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	out.Calibration, out.Markets = s.calibrate(ctx, markets)

	writeJSON(w, http.StatusOK, out)
}

func summarise(positions []store.Position) Summary {
	var sum Summary
	for _, p := range positions {
		if p.Cancelled || (p.State != "settled" && p.State != "voided") {
			continue
		}
		sum.Positions++
		sum.StakedMinor += p.AmountMinor
		sum.ReturnedMinor += p.PaidMinor

		// A sole winner in a thin pool collects less than they staked, so a hit
		// is defined by the cell landing, not by finishing in profit.
		switch {
		case p.SettledCell != nil && *p.SettledCell == p.CellIndex:
			sum.Hits++
		case p.PaidMinor > 0:
			sum.Refunds++
		default:
			sum.Misses++
		}
	}
	sum.NetMinor = sum.ReturnedMinor - sum.StakedMinor

	// Refunds are excluded from the hit rate: nobody won, so they say nothing
	// about whether a cell was picked well.
	if decided := sum.Hits + sum.Misses; decided > 0 {
		sum.HitRate = float64(sum.Hits) / float64(decided)
	}
	if sum.StakedMinor > 0 {
		sum.Expectancy = float64(sum.NetMinor) / float64(sum.StakedMinor)
	}
	return sum
}

type tally struct {
	cells int
	sum   float64
	hits  int
}

// calibrateOne replays one settled market through the pricing model as it
// stood at open, then checks how often cells of a given probability really
// landed. The market's own PriceAt round trip is the only I/O.
func (s *Server) calibrateOne(ctx context.Context, m store.MarketDetail) ([]tally, bool) {
	if m.SettledCell == nil {
		return nil, false // voided: no cell landed, so there is nothing to calibrate against
	}
	spot, err := s.Store.PriceAt(ctx, m.Symbol, m.OpensAt)
	if err != nil {
		return nil, false
	}
	grid := pricing.Grid{Floor: m.FloorPrice, CellWidth: m.CellWidth, BoundedCells: m.BoundedCells}
	probs, err := pricing.CellProbabilities(grid, pricing.Params{
		Spot: spot, Sigma: s.Sigma, Tau: m.ExpiresAt.Sub(m.OpensAt),
	})
	if err != nil {
		return nil, false
	}

	tallies := make([]tally, len(buckets)-1)
	for cell, p := range probs {
		b := bucketOf(p)
		if b < 0 {
			continue
		}
		tallies[b].cells++
		tallies[b].sum += p
		if *m.SettledCell == cell {
			tallies[b].hits++
		}
	}
	return tallies, true
}

const calibrateConcurrency = 16

// calibrate fans calibrateOne out across the settled markets — each is an
// independent Postgres round trip, so running them one at a time serialises
// the whole request on DB latency for no reason.
func (s *Server) calibrate(ctx context.Context, markets []store.MarketDetail) ([]Bucket, int) {
	perMarket := make([][]tally, len(markets))
	sem := make(chan struct{}, calibrateConcurrency)
	var wg sync.WaitGroup
	for i, m := range markets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, m store.MarketDetail) {
			defer wg.Done()
			defer func() { <-sem }()
			if t, ok := s.calibrateOne(ctx, m); ok {
				perMarket[i] = t
			}
		}(i, m)
	}
	wg.Wait()

	tallies := make([]tally, len(buckets)-1)
	analysed := 0
	for _, t := range perMarket {
		if t == nil {
			continue
		}
		analysed++
		for b := range tallies {
			tallies[b].cells += t[b].cells
			tallies[b].sum += t[b].sum
			tallies[b].hits += t[b].hits
		}
	}

	out := make([]Bucket, 0, len(tallies))
	for i, t := range tallies {
		if t.cells == 0 {
			continue
		}
		out = append(out, Bucket{
			Lower: buckets[i], Upper: math.Min(buckets[i+1], 1),
			Cells: t.cells, Predicted: t.sum / float64(t.cells),
			Hits: t.hits, Actual: float64(t.hits) / float64(t.cells),
		})
	}
	return out, analysed
}

func bucketOf(p float64) int {
	for i := range len(buckets) - 1 {
		if p >= buckets[i] && p < buckets[i+1] {
			return i
		}
	}
	return -1
}
