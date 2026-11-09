package pricing

import (
	"errors"
	"math"
)

// Two open-ended tail cells extend the partition to (0, +Inf) so cell
// probabilities sum to exactly 1.
type Grid struct {
	Floor        float64
	CellWidth    float64
	BoundedCells int
}

type Cell struct {
	Index        int
	Lower, Upper float64
}

func GridAround(spot, cellWidth float64, boundedCells int) Grid {
	anchor := math.Floor(spot/cellWidth) * cellWidth
	return Grid{
		Floor:        anchor - cellWidth*float64(boundedCells/2),
		CellWidth:    cellWidth,
		BoundedCells: boundedCells,
	}
}

func (g Grid) Validate() error {
	switch {
	case !(g.CellWidth > 0):
		return errors.New("pricing: cell width must be positive")
	case g.BoundedCells < 1:
		return errors.New("pricing: grid needs at least one bounded cell")
	case !(g.Floor > 0):
		return errors.New("pricing: grid floor must be positive")
	}
	return nil
}

func (g Grid) Ceiling() float64 { return g.Floor + g.CellWidth*float64(g.BoundedCells) }

func (g Grid) TotalCells() int { return g.BoundedCells + 2 }

func (g Grid) Cells() []Cell {
	cells := make([]Cell, 0, g.TotalCells())
	cells = append(cells, Cell{Index: 0, Lower: 0, Upper: g.Floor})
	for i := range g.BoundedCells {
		lower := g.lowerOf(i)
		cells = append(cells, Cell{Index: i + 1, Lower: lower, Upper: g.lowerOf(i + 1)})
	}
	cells = append(cells, Cell{Index: g.TotalCells() - 1, Lower: g.Ceiling(), Upper: math.Inf(1)})
	return cells
}

func (g Grid) CellIndexFor(price float64) int {
	if price < g.Floor {
		return 0
	}
	if price >= g.Ceiling() {
		return g.TotalCells() - 1
	}

	// Cells() derives bounds by multiplication and division is not its exact
	// inverse in float64, so a price sitting on a boundary can divide into the
	// neighbouring cell. Settle picks the winning cell this way, so the index
	// is reconciled against the bound the grid actually published.
	i := min(int((price-g.Floor)/g.CellWidth), g.BoundedCells-1)
	if price < g.lowerOf(i) {
		i--
	} else if i+1 < g.BoundedCells && price >= g.lowerOf(i+1) {
		i++
	}
	return 1 + i
}

func (g Grid) lowerOf(bounded int) float64 {
	return g.Floor + g.CellWidth*float64(bounded)
}
