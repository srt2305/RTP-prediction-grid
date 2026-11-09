package market

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// With Interval shorter than Window, several markets are live at once on
// staggered expiries. Each keeps its own pool; nothing crosses expiries.
type Series struct {
	Runner   *Runner
	Template Template
	Interval time.Duration
	Log      *slog.Logger

	mu   sync.Mutex
	open map[string]Market
}

func (s *Series) Run(ctx context.Context) error {
	if err := s.Template.Validate(); err != nil {
		return err
	}
	if s.Interval <= 0 {
		return errors.New("market: series interval must be positive")
	}

	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()

	var running sync.WaitGroup
	defer running.Wait()

	for {
		// A market that fails to open is skipped, not fatal: the feed may be
		// briefly behind, and ending the series would strand the whole engine
		// with no way back.
		if err := s.openOne(ctx, &running); err != nil && ctx.Err() == nil {
			s.Log.Error("could not open market, retrying next interval",
				"symbol", s.Template.Symbol, "err", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Series) openOne(ctx context.Context, running *sync.WaitGroup) error {
	spot, err := s.Runner.Store.LatestPrice(ctx, s.Template.Symbol)
	if err != nil {
		return fmt.Errorf("series: opening %s: %w", s.Template.Symbol, err)
	}

	m, err := s.Runner.Open(ctx, s.Template, spot, time.Now().UTC())
	if err != nil {
		return err
	}
	s.track(m)

	running.Add(1)
	go func() {
		defer running.Done()
		defer s.untrack(m.ID)

		if _, err := s.Runner.Run(ctx, m); err != nil && ctx.Err() == nil {
			s.Log.Error("market failed", "market", m.ID, "err", err)
		}
	}()
	return nil
}

// Live returns open markets, nearest expiry first.
func (s *Series) Live() []Market {
	s.mu.Lock()
	defer s.mu.Unlock()

	live := make([]Market, 0, len(s.open))
	for _, m := range s.open {
		live = append(live, m)
	}
	slices.SortFunc(live, func(a, b Market) int { return a.ExpiresAt.Compare(b.ExpiresAt) })
	return live
}

func (s *Series) track(m Market) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.open == nil {
		s.open = make(map[string]Market)
	}
	s.open[m.ID] = m
}

func (s *Series) untrack(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.open, id)
}
