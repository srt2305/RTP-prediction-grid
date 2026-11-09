package feed

import "time"

type Tick struct {
	Symbol string
	Price  float64
	At     time.Time
}
