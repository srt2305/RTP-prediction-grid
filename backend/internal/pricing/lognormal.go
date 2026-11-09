package pricing

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// calendarYear is the scaling base for Sigma and Drift, both quoted per annum.
// Crypto trades continuously; an equity model would use 252 trading days.
const calendarYear = 365 * 24 * time.Hour

const bpsDenominator = 10000

type Params struct {
	Spot  float64
	Sigma float64
	Drift float64
	Tau   time.Duration
}

func (p Params) validate() error {
	switch {
	case !(p.Spot > 0):
		return errors.New("pricing: spot must be positive")
	case p.Sigma < 0:
		return errors.New("pricing: sigma must not be negative")
	case math.IsNaN(p.Drift):
		return errors.New("pricing: drift must be a number")
	}
	return nil
}

func (p Params) years() float64 { return float64(p.Tau) / float64(calendarYear) }

// Under GBM, ln(S_T/S_0) is normal with mean (drift-sigma^2/2)*tau and standard
// deviation sigma*sqrt(tau). Consecutive CDF differences telescope to 1.
func CellProbabilities(g Grid, p Params) ([]float64, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}

	cells := g.Cells()
	probs := make([]float64, len(cells))

	tau := p.years()
	if tau <= 0 || p.Sigma == 0 {
		probs[g.CellIndexFor(p.Spot*math.Exp(p.Drift*math.Max(tau, 0)))] = 1
		return probs, nil
	}

	sd := p.Sigma * math.Sqrt(tau)
	mean := math.Log(p.Spot) + (p.Drift-0.5*p.Sigma*p.Sigma)*tau

	for i, c := range cells {
		lo, hi := zOf(c.Lower, mean, sd), zOf(c.Upper, mean, sd)

		// Difference within whichever tail the cell sits in. Subtracting two
		// CDFs that are both near 1 destroys the small number they differ by:
		// beyond 6 sigma that error reaches whole percent.
		if lo >= 0 {
			probs[i] = upperTail(lo) - upperTail(hi)
			continue
		}
		probs[i] = normalCDF(hi) - normalCDF(lo)
	}
	return probs, nil
}

// PayoutShare is the fraction of the pool paid out after rake.
func PayoutShare(rakeBps int) float64 { return 1 - float64(rakeBps)/bpsDenominator }

// Probabilities below ~5.6e-309 cannot be inverted in float64 and yield +Inf.
func FairMultipliers(probs []float64, rakeBps int) ([]float64, error) {
	if rakeBps < 0 || rakeBps >= bpsDenominator {
		return nil, fmt.Errorf("pricing: rake %d bps outside [0, %d)", rakeBps, bpsDenominator)
	}

	payoutShare := PayoutShare(rakeBps)
	multipliers := make([]float64, len(probs))
	for i, p := range probs {
		if p <= 0 {
			multipliers[i] = math.Inf(1)
			continue
		}
		multipliers[i] = payoutShare / p
	}
	return multipliers, nil
}

func normalCDF(z float64) float64 { return 0.5 * math.Erfc(-z/math.Sqrt2) }

func upperTail(z float64) float64 { return 0.5 * math.Erfc(z/math.Sqrt2) }

func zOf(price, mean, sd float64) float64 {
	if price <= 0 {
		return math.Inf(-1)
	}
	return (math.Log(price) - mean) / sd
}
