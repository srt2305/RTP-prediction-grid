package feed

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

const calendarYear = 365 * 24 * time.Hour

// Speed compresses the simulated clock: at 60, one second of ticks advances the
// process by a minute.
type Simulator struct {
	Start    float64
	Sigma    float64
	Drift    float64
	Interval time.Duration
	Speed    float64
	Seed     uint64
}

func (s Simulator) Subscribe(ctx context.Context, symbol string) (<-chan Tick, error) {
	if s.Start <= 0 || s.Sigma < 0 || s.Interval <= 0 {
		return nil, errors.New("feed: simulator needs positive start, interval and non-negative sigma")
	}

	speed := math.Max(s.Speed, 1)
	dt := float64(s.Interval) * speed / float64(calendarYear)
	drift := (s.Drift - 0.5*s.Sigma*s.Sigma) * dt
	shock := s.Sigma * math.Sqrt(dt)

	ticks := make(chan Tick)
	go func() {
		defer close(ticks)

		r := rand.New(rand.NewPCG(s.Seed, s.Seed+1))
		ticker := time.NewTicker(s.Interval)
		defer ticker.Stop()

		price := s.Start
		for {
			select {
			case <-ctx.Done():
				return
			case at := <-ticker.C:
				price *= math.Exp(drift + shock*r.NormFloat64())
				select {
				case ticks <- Tick{Symbol: symbol, Price: price, At: at.UTC()}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ticks, nil
}

func (s Simulator) MarketTime(wall time.Duration) time.Duration {
	return time.Duration(float64(wall) * math.Max(s.Speed, 1))
}
