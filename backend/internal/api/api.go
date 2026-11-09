package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/market"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/store"
)

type Store interface {
	LatestPrice(ctx context.Context, symbol string) (float64, error)
	PriceAt(ctx context.Context, symbol string, at time.Time) (float64, error)
	Audit(ctx context.Context) (store.AuditTotals, error)
	LookupAccount(ctx context.Context, handle string) (store.Account, bool, error)
	EnsureAccount(ctx context.Context, handle string) (store.Account, error)
	Balance(ctx context.Context, accountID string) (available, escrowed int64, err error)
	Positions(ctx context.Context, accountID string, limit int) ([]store.Position, error)
	Ledger(ctx context.Context, accountID string, limit int) ([]store.LedgerEntry, error)
	Market(ctx context.Context, id string) (store.MarketDetail, error)
	SettledMarkets(ctx context.Context, limit int) ([]store.MarketDetail, error)
	RecentlySettled(ctx context.Context, symbol string, within time.Duration) ([]store.MarketDetail, error)
	PricePath(ctx context.Context, symbol string, from, to time.Time, points int) ([]store.PricePoint, error)
	SettledTotals(ctx context.Context, marketID string) (map[int]int64, error)
	StakeOutcome(ctx context.Context, stakeID string) (store.StakeOutcome, error)
}

type Server struct {
	Series *market.Series
	Store  Store
	Live   *store.LiveTotals
	Sigma  float64
	Log    *slog.Logger

	mu     sync.Mutex
	anchor float64
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/grid", s.grid)
	mux.HandleFunc("GET /api/accounts/{handle}", s.account)
	mux.HandleFunc("GET /api/accounts/{handle}/ledger", s.ledger)
	mux.HandleFunc("POST /api/stakes", s.placeStake)
	mux.HandleFunc("DELETE /api/stakes/{id}", s.cancelStake)
	mux.HandleFunc("GET /api/markets/{id}/watch", s.watch)
	mux.HandleFunc("GET /api/performance", s.performance)
	mux.HandleFunc("GET /ws", s.stream)
	return withCORS(mux)
}

// The engine and the Next.js dev server sit on different ports, so the browser
// treats every call as cross-origin.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, status int, err error) {
	if status >= 500 {
		s.Log.Error("request failed", "status", status, "err", err)
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	symbol := s.Series.Template.Symbol

	out := map[string]any{"symbol": symbol, "openMarkets": len(s.Series.Live())}

	spot, err := s.Store.LatestPrice(ctx, symbol)
	if err != nil {
		out["feed"] = "no ticks"
		writeJSON(w, http.StatusServiceUnavailable, out)
		return
	}
	out["spot"] = spot

	audit, err := s.Store.Audit(ctx)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	out["conserved"] = audit.Spendable+audit.Escrowed+audit.Rake == audit.Deposited
	out["depositedMinor"] = audit.Deposited
	out["rakeMinor"] = audit.Rake
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) grid(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	handle := r.PathValue("handle")
	account, found, err := s.Store.LookupAccount(ctx, handle)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{
			"handle": handle, "availableMinor": 0, "escrowedMinor": 0,
			"positions": []store.Position{},
		})
		return
	}
	availableMinor, escrowedMinor, err := s.Store.Balance(ctx, account.ID)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	positions, err := s.Store.Positions(ctx, account.ID, 40)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id": account.ID, "handle": account.Handle,
		"availableMinor": availableMinor, "escrowedMinor": escrowedMinor,
		"positions": positions,
	})
}

func (s *Server) ledger(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	account, found, err := s.Store.LookupAccount(ctx, r.PathValue("handle"))
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, []store.LedgerEntry{})
		return
	}
	entries, err := s.Store.Ledger(ctx, account.ID, 60)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if entries == nil {
		entries = []store.LedgerEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

type stakeRequest struct {
	MarketID    string  `json:"marketId"`
	Account     string  `json:"account"`
	Cell        int     `json:"cell"`
	AmountMinor int64   `json:"amountMinor"`
	MinMultiple float64 `json:"minMultiple"`
}

func (s *Server) placeStake(w http.ResponseWriter, r *http.Request) {
	var req stakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}

	m, ok := s.liveMarket(req.MarketID)
	if !ok {
		s.fail(w, http.StatusNotFound, errors.New("no open market with that id"))
		return
	}

	ctx := r.Context()
	account, err := s.Store.EnsureAccount(ctx, req.Account)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	id, err := s.Series.Runner.PlaceStake(ctx, m, account.ID, req.Cell, req.AmountMinor, req.MinMultiple, time.Now().UTC())
	if err != nil {
		s.fail(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"stakeId": id})
}

func (s *Server) cancelStake(w http.ResponseWriter, r *http.Request) {
	marketID := r.URL.Query().Get("marketId")
	m, ok := s.liveMarket(marketID)
	if !ok {
		// Not live in the Series doesn't mean it never existed: a market that
		// has fully settled is untracked, and "already resolved" is a more
		// useful answer than a bare not-found.
		if detail, err := s.Store.Market(r.Context(), marketID); err == nil {
			s.fail(w, http.StatusConflict, fmt.Errorf("market already %s, cannot cancel", detail.State))
			return
		}
		s.fail(w, http.StatusNotFound, errors.New("no open market with that id"))
		return
	}
	if err := s.Series.Runner.CancelPosition(r.Context(), m, r.PathValue("id"), time.Now().UTC()); err != nil {
		s.fail(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errBadCell = errors.New("cell outside the market grid")

func intParam(r *http.Request, name string) (int, error) {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return 0, fmt.Errorf("query parameter %q must be an integer: %w", name, err)
	}
	return v, nil
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, store.ErrInsufficientFunds):
		return http.StatusPaymentRequired
	case errors.Is(err, market.ErrStakingClosed), errors.Is(err, market.ErrAmendmentClosed),
		errors.Is(err, store.ErrPriceMoved):
		return http.StatusConflict
	case errors.Is(err, store.ErrStakeNotCancellable):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func (s *Server) liveMarket(id string) (market.Market, bool) {
	for _, m := range s.Series.Live() {
		if m.ID == id {
			return m, true
		}
	}
	return market.Market{}, false
}
