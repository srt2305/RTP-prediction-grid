package cli

import (
	"flag"
	"fmt"
	"math"
	"os"
	"text/tabwriter"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/pricing"
)

func runGrid(args []string) error {
	fs := flag.NewFlagSet("grid", flag.ExitOnError)
	spot := fs.Float64("spot", 65000, "spot price in dollars")
	sigma := fs.Float64("sigma", 0.6, "annualised volatility, decimal")
	drift := fs.Float64("drift", 0, "annualised drift, decimal")
	width := fs.Float64("width", 50, "cell width in dollars")
	cells := fs.Int("cells", 20, "number of bounded cells")
	rake := fs.Int("rake", 250, "rake in basis points")
	tau := fs.Duration("tau", time.Minute, "time to expiry")
	if err := fs.Parse(args); err != nil {
		return err
	}

	grid := pricing.GridAround(*spot, *width, *cells)
	params := pricing.Params{Spot: *spot, Sigma: *sigma, Drift: *drift, Tau: *tau}

	probs, err := pricing.CellProbabilities(grid, params)
	if err != nil {
		return err
	}
	multipliers, err := pricing.FairMultipliers(probs, *rake)
	if err != nil {
		return err
	}

	fmt.Printf("spot %.2f  sigma %.4f  tau %s  rake %d bps\n", *spot, *sigma, *tau, *rake)
	fmt.Printf("grid [%.2f, %.2f) in %d cells of %.2f, plus two open tails\n\n",
		grid.Floor, grid.Ceiling(), grid.BoundedCells, grid.CellWidth)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "cell\trange\tprobability\tmultiplier\t")

	sum := 0.0
	for i, c := range grid.Cells() {
		sum += probs[i]
		fmt.Fprintf(w, "%d\t%s\t%.6f\t%s\t\n", c.Index, cellRange(c), probs[i], multiplier(multipliers[i]))
	}
	fmt.Fprintf(w, "\ttotal\t%.12f\t\t\n", sum)
	return w.Flush()
}

func cellRange(c pricing.Cell) string {
	switch {
	case c.Lower == 0:
		return fmt.Sprintf("< %.2f", c.Upper)
	case math.IsInf(c.Upper, 1):
		return fmt.Sprintf(">= %.2f", c.Lower)
	}
	return fmt.Sprintf("%.2f - %.2f", c.Lower, c.Upper)
}

func multiplier(m float64) string {
	switch {
	case math.IsInf(m, 1):
		return "-"
	case m >= 10000:
		return fmt.Sprintf("%.1ex", m)
	}
	return fmt.Sprintf("%.2fx", m)
}
