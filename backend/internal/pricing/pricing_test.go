package pricing

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

const trials = 2000

func randomCase(r *rand.Rand) (Grid, Params) {
	spot := 100 * math.Exp(4*r.Float64())
	width := spot * (0.0005 + 0.02*r.Float64())
	p := Params{
		Spot:  spot,
		Sigma: 0.05 + 3*r.Float64(),
		Drift: -0.5 + r.Float64(),
		Tau:   time.Duration(1+r.IntN(3600)) * time.Second,
	}
	return GridAround(spot, width, 2+2*r.IntN(30)), p
}

func TestProbabilitiesSumToOne(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range trials {
		g, p := randomCase(r)
		probs, err := CellProbabilities(g, p)
		if err != nil {
			t.Fatalf("CellProbabilities(%+v, %+v): %v", g, p, err)
		}

		sum := 0.0
		for i, prob := range probs {
			if prob < 0 || prob > 1 || math.IsNaN(prob) {
				t.Fatalf("cell %d probability %v outside [0,1] for %+v %+v", i, prob, g, p)
			}
			sum += prob
		}
		if math.Abs(sum-1) > 1e-12 {
			t.Fatalf("probabilities sum to %.18f, want 1 (grid %+v, params %+v)", sum, g, p)
		}
	}
}

func TestFairMultiplierIsInverseProbability(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range trials {
		g, p := randomCase(r)
		probs, err := CellProbabilities(g, p)
		if err != nil {
			t.Fatalf("CellProbabilities: %v", err)
		}
		multipliers, err := FairMultipliers(probs, 0)
		if err != nil {
			t.Fatalf("FairMultipliers: %v", err)
		}

		for i, prob := range probs {
			if math.IsInf(multipliers[i], 1) {
				if prob > 1/math.MaxFloat64 {
					t.Fatalf("cell %d: infinite multiplier for invertible probability %g", i, prob)
				}
				continue
			}
			if got := multipliers[i] * prob; math.Abs(got-1) > 1e-9 {
				t.Fatalf("cell %d: multiplier*probability = %.15f, want 1", i, got)
			}
		}
	}
}

func TestRakeReducesMultiplierByRakeShare(t *testing.T) {
	probs := []float64{0.25, 0.5, 0.25}
	multipliers, err := FairMultipliers(probs, 250)
	if err != nil {
		t.Fatalf("FairMultipliers: %v", err)
	}
	for i, prob := range probs {
		want := 0.975 / prob
		if math.Abs(multipliers[i]-want) > 1e-12 {
			t.Fatalf("cell %d: got %v, want %v", i, multipliers[i], want)
		}
	}
}

// Every published boundary must bin into the cell it opens, or a settlement
// price landing exactly on one pays a different cell than the grid displayed.
func TestCellBoundariesBinIntoTheirOwnCell(t *testing.T) {
	r := rand.New(rand.NewPCG(31, 37))
	for range trials {
		g, _ := randomCase(r)
		for _, c := range g.Cells() {
			if c.Index == 0 || c.Index == g.TotalCells()-1 {
				continue
			}
			if got := g.CellIndexFor(c.Lower); got != c.Index {
				t.Fatalf("boundary %.17g of cell %d binned as %d (grid %+v)",
					c.Lower, c.Index, got, g)
			}
			if got := g.CellIndexFor(math.Nextafter(c.Upper, 0)); got != c.Index {
				t.Fatalf("top of cell %d binned as %d (grid %+v)", c.Index, got, g)
			}
		}
	}
}

func TestCellIndexRoundTrips(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for range trials {
		g, _ := randomCase(r)
		for _, c := range g.Cells() {
			probe := c.Lower + g.CellWidth/2
			if c.Index == 0 {
				probe = g.Floor / 2
			}
			if math.IsInf(c.Upper, 1) {
				probe = g.Ceiling() * 1.5
			}
			if got := g.CellIndexFor(probe); got != c.Index {
				t.Fatalf("price %v mapped to cell %d, want %d (grid %+v)", probe, got, c.Index, g)
			}
		}
	}
}

func TestMassConcentratesAtExpiry(t *testing.T) {
	g := GridAround(50000, 100, 20)
	p := Params{Spot: 50000, Sigma: 0.8, Drift: 0, Tau: 0}

	probs, err := CellProbabilities(g, p)
	if err != nil {
		t.Fatalf("CellProbabilities: %v", err)
	}
	if got := probs[g.CellIndexFor(50000)]; got != 1 {
		t.Fatalf("at tau=0 the spot cell has probability %v, want 1", got)
	}
}

// Tail cells are unbounded, and a bound that cannot be encoded as JSON reaches
// the client as an empty body rather than an error.
func TestTailCellsAreUnbounded(t *testing.T) {
	g := GridAround(65000, 50, 20)
	cells := g.Cells()

	if bottom := cells[0]; bottom.Lower != 0 {
		t.Errorf("bottom tail starts at %v, want 0", bottom.Lower)
	}
	if top := cells[len(cells)-1]; !math.IsInf(top.Upper, 1) {
		t.Errorf("top tail ends at %v, want +Inf", top.Upper)
	}
	for _, c := range cells[1 : len(cells)-1] {
		if math.IsInf(c.Lower, 0) || math.IsInf(c.Upper, 0) {
			t.Fatalf("bounded cell %d has an infinite bound: %+v", c.Index, c)
		}
	}
}

// Textbook checks a reader can verify by hand: the one-sigma band holds ~68.3%
// of the mass, two sigma ~95.4%, three sigma ~99.7%.
func TestSigmaBandsHoldTextbookMass(t *testing.T) {
	p := Params{Spot: 65000, Sigma: 0.6, Tau: time.Minute}
	tau := p.years()
	sd := p.Sigma * math.Sqrt(tau)
	mean := math.Log(p.Spot) + (p.Drift-0.5*p.Sigma*p.Sigma)*tau

	// Only whole cells inside the band are counted, which drops about half a
	// cell at each edge. 400 cells per sigma keeps that under 0.001.
	const cellsPerSigma = 400
	width := p.Spot * sd / cellsPerSigma
	g := GridAround(p.Spot, width, 8*cellsPerSigma)
	probs, err := CellProbabilities(g, p)
	if err != nil {
		t.Fatalf("CellProbabilities: %v", err)
	}

	for _, want := range []struct {
		sigmas, mass float64
	}{{1, 0.6827}, {2, 0.9545}, {3, 0.9973}} {
		lo := math.Exp(mean - want.sigmas*sd)
		hi := math.Exp(mean + want.sigmas*sd)

		var inside float64
		for i, c := range g.Cells() {
			if c.Lower >= lo && c.Upper <= hi {
				inside += probs[i]
			}
		}
		if math.Abs(inside-want.mass) > 0.002 {
			t.Errorf("%.0f sigma band holds %.4f, want about %.4f", want.sigmas, inside, want.mass)
		} else {
			t.Logf("%.0f sigma band holds %.4f (textbook %.4f)", want.sigmas, inside, want.mass)
		}
	}
}

// A stake priced at the fair multiplier returns its stake less the rake, in
// expectation. That is the whole definition of the house edge here.
func TestFairMultiplierLeavesExactlyTheRake(t *testing.T) {
	g := GridAround(65000, 50, 40)
	probs, err := CellProbabilities(g, Params{Spot: 65000, Sigma: 0.6, Tau: time.Minute})
	if err != nil {
		t.Fatalf("CellProbabilities: %v", err)
	}

	for _, rakeBps := range []int{0, 100, 250, 1000} {
		multipliers, err := FairMultipliers(probs, rakeBps)
		if err != nil {
			t.Fatalf("FairMultipliers: %v", err)
		}

		want := 1 - float64(rakeBps)/bpsDenominator
		for i, prob := range probs {
			if prob < 1e-6 || math.IsInf(multipliers[i], 1) {
				continue
			}
			if ev := prob * multipliers[i]; math.Abs(ev-want) > 1e-12 {
				t.Fatalf("rake %d bps, cell %d: expected value %.15f, want %.15f",
					rakeBps, i, ev, want)
			}
		}
	}
}

// Far-tail cells must keep their relative accuracy, not collapse to the
// granularity of float64 near 1.
func TestFarTailProbabilitiesStayResolved(t *testing.T) {
	p := Params{Spot: 65000, Sigma: 0.35, Tau: time.Minute}
	g := GridAround(p.Spot, 12, 60)

	probs, err := CellProbabilities(g, p)
	if err != nil {
		t.Fatalf("CellProbabilities: %v", err)
	}

	sd := p.Sigma * math.Sqrt(p.years())
	mean := math.Log(p.Spot) + (p.Drift-0.5*p.Sigma*p.Sigma)*p.years()

	var checked int
	for i, c := range g.Cells() {
		if math.IsInf(c.Upper, 1) || c.Lower == 0 {
			continue
		}
		z := (math.Log(c.Lower) - mean) / sd
		if z < 5 {
			continue
		}
		checked++

		want := upperTail(z) - upperTail((math.Log(c.Upper)-mean)/sd)
		if probs[i] == 0 && want > 0 {
			t.Errorf("cell %d at %.1f sigma collapsed to zero, want %.3e", i, z, want)
			continue
		}
		if rel := math.Abs(probs[i]-want) / want; rel > 1e-12 {
			t.Errorf("cell %d at %.1f sigma: relative error %.2e", i, z, rel)
		}
	}
	if checked == 0 {
		t.Fatal("no cells beyond 5 sigma to check")
	}
	t.Logf("%d cells beyond 5 sigma retained full relative precision", checked)
}

// Splitting the computation at the median must not cost the sum-to-one
// guarantee the whole grid depends on.
func TestSumPrecisionAcrossRegimes(t *testing.T) {
	cases := []Params{
		{Spot: 65000, Sigma: 0.05, Tau: 10 * time.Second},
		{Spot: 65000, Sigma: 0.6, Tau: time.Minute},
		{Spot: 65000, Sigma: 3.0, Tau: time.Hour},
		{Spot: 0.00031, Sigma: 1.2, Tau: 30 * time.Second},
		{Spot: 1_250_000, Sigma: 0.9, Tau: 5 * time.Minute},
	}

	worst := 0.0
	for _, p := range cases {
		g := GridAround(p.Spot, p.Spot*0.002, 80)
		probs, err := CellProbabilities(g, p)
		if err != nil {
			t.Fatalf("CellProbabilities(%+v): %v", p, err)
		}

		sum := 0.0
		for _, prob := range probs {
			sum += prob
		}
		if d := math.Abs(sum - 1); d > worst {
			worst = d
		}
		if math.Abs(sum-1) > 1e-13 {
			t.Errorf("spot %g sigma %.2f: probabilities sum to %.17f", p.Spot, p.Sigma, sum)
		}
	}
	t.Logf("worst deviation from 1 across regimes: %.2e", worst)
}

// The invariant tests above only prove self-consistency: uniform 1/N
// probabilities would satisfy both. These check the numbers are right, by
// simulating the process the model claims to describe and binning where it
// lands.

const (
	samples  = 400_000
	minCount = 200 // below this an expected count is too small to judge
	maxZ     = 4.5
)

// steppedTerminalPrice evolves GBM in n discrete steps. The log-Euler scheme is
// exact for GBM, so any n must agree with the closed form to within MC error.
func steppedTerminalPrice(r *rand.Rand, p Params, steps int) float64 {
	dt := p.years() / float64(steps)
	drift := (p.Drift - 0.5*p.Sigma*p.Sigma) * dt
	shock := p.Sigma * math.Sqrt(dt)

	price := p.Spot
	for range steps {
		price *= math.Exp(drift + shock*r.NormFloat64())
	}
	return price
}

func binCounts(r *rand.Rand, g Grid, p Params, steps int) []int {
	counts := make([]int, g.TotalCells())
	for range samples {
		counts[g.CellIndexFor(steppedTerminalPrice(r, p, steps))]++
	}
	return counts
}

// zScore is the standardised deviation of an observed count from a binomial
// expectation, so cells of wildly different probability are judged on one scale.
func zScore(observed int, prob float64) float64 {
	expected := prob * samples
	sd := math.Sqrt(samples * prob * (1 - prob))
	return (float64(observed) - expected) / sd
}

func TestProbabilitiesMatchSimulatedPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("simulates 400k paths per case")
	}

	// Cell width tracks one standard deviation of the move: too wide and the
	// whole distribution lands in two cells, too narrow and none carry mass.
	cases := []struct {
		name  string
		steps int
		p     Params
	}{
		{"one step, no drift", 1, Params{Spot: 65000, Sigma: 0.6, Tau: time.Minute}},
		{"sixty steps, no drift", 60, Params{Spot: 65000, Sigma: 0.6, Tau: time.Minute}},
		{"positive drift", 60, Params{Spot: 65000, Sigma: 0.6, Drift: 0.4, Tau: time.Minute}},
		{"negative drift", 60, Params{Spot: 65000, Sigma: 0.6, Drift: -0.4, Tau: time.Minute}},
		{"low vol", 60, Params{Spot: 65000, Sigma: 0.15, Tau: 5 * time.Minute}},
		{"high vol", 60, Params{Spot: 65000, Sigma: 2.5, Tau: 30 * time.Second}},
		{"long window", 240, Params{Spot: 65000, Sigma: 0.6, Tau: time.Hour}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sd := tc.p.Spot * tc.p.Sigma * math.Sqrt(tc.p.years())
			g := GridAround(tc.p.Spot, sd/2, 40)
			probs, err := CellProbabilities(g, tc.p)
			if err != nil {
				t.Fatalf("CellProbabilities: %v", err)
			}

			counts := binCounts(rand.New(rand.NewPCG(41, 43)), g, tc.p, tc.steps)

			var judged int
			for i, prob := range probs {
				if prob*samples < minCount {
					continue
				}
				judged++
				if z := zScore(counts[i], prob); math.Abs(z) > maxZ {
					t.Errorf("cell %d: predicted %.5f, simulated %.5f over %d paths (z = %+.2f)",
						i, prob, float64(counts[i])/samples, samples, z)
				}
			}
			if judged < 5 {
				t.Fatalf("only %d cells carried enough mass to judge", judged)
			}
			t.Logf("%d cells within %.1f sigma of simulation", judged, maxZ)
		})
	}
}

// With zero drift the process is a martingale: E[S_T] = S_0 regardless of
// volatility or horizon. Getting the -sigma^2/2 term wrong breaks this and
// nothing else in the suite would notice.
func TestZeroDriftIsMartingale(t *testing.T) {
	if testing.Short() {
		t.Skip("simulates 400k paths per volatility")
	}

	for _, sigma := range []float64{0.1, 0.6, 1.5, 3.0} {
		p := Params{Spot: 65000, Sigma: sigma, Tau: 10 * time.Minute}
		r := rand.New(rand.NewPCG(7, 9))

		var sum float64
		for range samples {
			sum += steppedTerminalPrice(r, p, 40)
		}
		mean := sum / samples

		// Standard error of the mean of a lognormal with these parameters.
		v := sigma * sigma * p.years()
		se := p.Spot * math.Sqrt((math.Exp(v)-1)/samples)
		if z := (mean - p.Spot) / se; math.Abs(z) > maxZ {
			t.Errorf("sigma %.2f: E[S_T] = %.2f, want %.2f (z = %+.2f)", sigma, mean, p.Spot, z)
		}
	}
}

// The distribution's median sits below spot by exp(-sigma^2*tau/2): more paths
// finish below the mean than above it, which is why the cell holding spot is
// not the most probable cell at high volatility.
func TestMedianBelowSpotWithoutDrift(t *testing.T) {
	p := Params{Spot: 65000, Sigma: 2.0, Tau: time.Hour}
	g := GridAround(p.Spot, 50, 400)

	probs, err := CellProbabilities(g, p)
	if err != nil {
		t.Fatalf("CellProbabilities: %v", err)
	}

	var below float64
	spotCell := g.CellIndexFor(p.Spot)
	for i := range spotCell {
		below += probs[i]
	}
	if below <= 0.5 {
		t.Fatalf("only %.4f of the mass sits below spot, want more than half", below)
	}

	wantMedian := p.Spot * math.Exp(-0.5*p.Sigma*p.Sigma*p.years())
	t.Logf("mass below spot %.4f, theoretical median %.2f against spot %.2f",
		below, wantMedian, p.Spot)
}
