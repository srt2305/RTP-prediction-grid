package market_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/market"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/pool"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/store"
)

// These exercise the real Postgres and Redis. They skip when the stack is not
// up rather than failing, so `go test ./...` stays useful without docker.

const spot = 65000.0

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fixedOracle struct{ price float64 }

func (o fixedOracle) OfficialPrice(context.Context, string, time.Time) (float64, error) {
	return o.price, nil
}

type brokenOracle struct{ err error }

func (o brokenOracle) OfficialPrice(context.Context, string, time.Time) (float64, error) {
	return 0, o.err
}

func dsn(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func newRunner(t *testing.T, oracle market.SettlementOracle) (*market.Runner, *store.Postgres, *store.LiveTotals) {
	t.Helper()
	ctx := t.Context()

	pg, err := store.OpenPostgres(ctx, dsn("RTP_POSTGRES_DSN", "postgres://rtp:rtp@localhost:5432/rtp"), quiet)
	if err != nil {
		t.Skipf("postgres unavailable, run `make up`: %v", err)
	}
	t.Cleanup(pg.Close)

	live, err := store.OpenRedis(ctx, dsn("RTP_REDIS_ADDR", "localhost:6379"))
	if err != nil {
		t.Skipf("redis unavailable, run `make up`: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	return &market.Runner{Store: pg, Live: live, Oracle: oracle, Log: quiet}, pg, live
}

// funded creates an isolated account so tests never contend over a balance.
func funded(t *testing.T, pg *store.Postgres, amount int64) store.Account {
	t.Helper()
	a, err := pg.EnsureAccount(t.Context(), "test-"+uuid.NewString())
	if err != nil {
		t.Fatalf("EnsureAccount: %v", err)
	}
	if err := pg.Deposit(t.Context(), a.ID, amount); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	return a
}

func available(t *testing.T, pg *store.Postgres, accountID string) int64 {
	t.Helper()
	got, _, err := pg.Balance(t.Context(), accountID)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	return got
}

func template() market.Template {
	return market.Template{
		Symbol:       "TESTUSD",
		Window:       4 * time.Second,
		FreezeBefore: time.Second,
		AmendBefore:  2 * time.Second,
		CellWidth:    50,
		BoundedCells: 20,
		RakeBps:      250,
	}
}

func openMarket(t *testing.T, r *market.Runner) market.Market {
	t.Helper()
	m, err := r.Open(t.Context(), template(), spot, time.Now().UTC())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m
}

func TestSettlementPaysWinnersAndCreditsTheLedger(t *testing.T) {
	settlesAt := spot + 20 // lands in the cell holding spot
	r, pg, _ := newRunner(t, fixedOracle{settlesAt})
	m := openMarket(t, r)

	winner, loser := funded(t, pg, 500_00), funded(t, pg, 500_00)
	winCell := m.Grid.CellIndexFor(settlesAt)

	if _, err := r.PlaceStake(t.Context(), m, winner.ID, winCell, 100_00, 0, time.Now().UTC()); err != nil {
		t.Fatalf("PlaceStake winner: %v", err)
	}
	if _, err := r.PlaceStake(t.Context(), m, loser.ID, winCell+3, 300_00, 0, time.Now().UTC()); err != nil {
		t.Fatalf("PlaceStake loser: %v", err)
	}

	res, err := r.Settle(t.Context(), m)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if res.Settlement.Outcome != pool.OutcomePaid {
		t.Fatalf("outcome %q, want paid", res.Settlement.Outcome)
	}

	// $400 pool, 250 bps rake = $10, so the sole winner takes $390.
	if res.Settlement.Rake != 10_00 {
		t.Errorf("rake %d, want 1000", res.Settlement.Rake)
	}
	if got := available(t, pg, winner.ID); got != 500_00-100_00+390_00 {
		t.Errorf("winner balance %d, want %d", got, 500_00-100_00+390_00)
	}
	if got := available(t, pg, loser.ID); got != 500_00-300_00 {
		t.Errorf("loser balance %d, want %d", got, 500_00-300_00)
	}
}

// The bug this suite exists for: a market that cannot price must return every
// stake, because the ledger already debited them.
func TestVoidedMarketRefundsEveryStake(t *testing.T) {
	r, pg, live := newRunner(t, brokenOracle{errors.New("no ticks at expiry")})
	m := openMarket(t, r)

	accounts := []store.Account{funded(t, pg, 500_00), funded(t, pg, 500_00)}
	for i, a := range accounts {
		if _, err := r.PlaceStake(t.Context(), m, a.ID, 5+i, 120_00, 0, time.Now().UTC()); err != nil {
			t.Fatalf("PlaceStake: %v", err)
		}
		if got := available(t, pg, a.ID); got != 380_00 {
			t.Fatalf("balance after stake %d, want 38000", got)
		}
	}

	if _, err := r.Settle(t.Context(), m); err == nil {
		t.Fatal("Settle should report the oracle failure")
	}

	for _, a := range accounts {
		if got := available(t, pg, a.ID); got != 500_00 {
			t.Errorf("balance after void %d, want 50000 — the stake was not refunded", got)
		}
	}

	totals, err := live.Totals(t.Context(), m.ID)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if len(totals) != 0 {
		t.Errorf("live totals survived the void: %v", totals)
	}
}

func TestUnstakedWinningCellRefundsWithoutRake(t *testing.T) {
	settlesAt := spot + 20
	r, pg, _ := newRunner(t, fixedOracle{settlesAt})
	m := openMarket(t, r)

	a := funded(t, pg, 500_00)
	away := m.Grid.CellIndexFor(settlesAt) + 5
	if _, err := r.PlaceStake(t.Context(), m, a.ID, away, 200_00, 0, time.Now().UTC()); err != nil {
		t.Fatalf("PlaceStake: %v", err)
	}

	res, err := r.Settle(t.Context(), m)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if res.Settlement.Outcome != pool.OutcomeRefunded {
		t.Fatalf("outcome %q, want refunded", res.Settlement.Outcome)
	}
	if res.Settlement.Rake != 0 {
		t.Errorf("rake %d on a refund, want 0", res.Settlement.Rake)
	}
	if got := available(t, pg, a.ID); got != 500_00 {
		t.Errorf("balance %d, want 50000", got)
	}
}

func TestCancelRefundsAndUpdatesLiveTotals(t *testing.T) {
	r, pg, live := newRunner(t, fixedOracle{spot})
	m := openMarket(t, r)

	a := funded(t, pg, 500_00)
	id, err := r.PlaceStake(t.Context(), m, a.ID, 7, 150_00, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("PlaceStake: %v", err)
	}

	totals, err := live.Totals(t.Context(), m.ID)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if totals[7] != 150_00 {
		t.Fatalf("live total for cell 7 is %d, want 15000", totals[7])
	}

	if err := r.CancelPosition(t.Context(), m, id, time.Now().UTC()); err != nil {
		t.Fatalf("CancelPosition: %v", err)
	}
	if got := available(t, pg, a.ID); got != 500_00 {
		t.Errorf("balance after cancel %d, want 50000", got)
	}
	if totals, _ = live.Totals(t.Context(), m.ID); totals[7] != 0 {
		t.Errorf("live total for cell 7 is %d after cancel, want 0", totals[7])
	}

	if err := r.CancelPosition(t.Context(), m, id, time.Now().UTC()); err == nil {
		t.Error("cancelling twice should fail")
	}

	res, err := r.Settle(t.Context(), m)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if res.Settlement.Outcome != pool.OutcomeVoid {
		t.Errorf("outcome %q after the only stake was cancelled, want void", res.Settlement.Outcome)
	}
}

func TestStakeBeyondBalanceWritesNothing(t *testing.T) {
	r, pg, live := newRunner(t, fixedOracle{spot})
	m := openMarket(t, r)

	a := funded(t, pg, 100_00)
	if _, err := r.PlaceStake(t.Context(), m, a.ID, 6, 100_01, 0, time.Now().UTC()); !errors.Is(err, store.ErrInsufficientFunds) {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}
	if got := available(t, pg, a.ID); got != 100_00 {
		t.Errorf("balance moved to %d on a rejected stake", got)
	}

	totals, err := live.Totals(t.Context(), m.ID)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if len(totals) != 0 {
		t.Errorf("rejected stake reached live totals: %v", totals)
	}
}

// The account row is locked before the balance is read; without that two
// concurrent stakes both pass the check and the account goes negative.
func TestConcurrentStakesCannotOverdraw(t *testing.T) {
	r, pg, _ := newRunner(t, fixedOracle{spot})
	m := openMarket(t, r)

	const each = 60_00
	a := funded(t, pg, 100_00) // affords one, not two

	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = r.PlaceStake(context.Background(), m, a.ID, 6+i, each, 0, time.Now().UTC())
		}()
	}
	wg.Wait()

	var accepted int
	for _, err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, store.ErrInsufficientFunds) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if accepted != 1 {
		t.Errorf("%d of 8 concurrent stakes accepted, want 1", accepted)
	}
	if got := available(t, pg, a.ID); got < 0 {
		t.Errorf("balance went negative: %d", got)
	}
}

func TestPhaseGatesRejectLateActions(t *testing.T) {
	r, pg, _ := newRunner(t, fixedOracle{spot})

	tmpl := template()
	tmpl.Window = 2 * time.Second
	tmpl.AmendBefore = 1500 * time.Millisecond
	tmpl.FreezeBefore = time.Second

	m, err := r.Open(t.Context(), tmpl, spot, time.Now().UTC())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	a := funded(t, pg, 500_00)
	id, err := r.PlaceStake(t.Context(), m, a.ID, 6, 50_00, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("PlaceStake while amendable: %v", err)
	}

	time.Sleep(time.Until(m.AmendsAt) + 100*time.Millisecond)
	if err := r.CancelPosition(t.Context(), m, id, time.Now().UTC()); !errors.Is(err, market.ErrAmendmentClosed) {
		t.Errorf("cancel after amend deadline: got %v, want ErrAmendmentClosed", err)
	}
	if _, err := r.PlaceStake(t.Context(), m, a.ID, 7, 50_00, 0, time.Now().UTC()); err != nil {
		t.Errorf("staking should still be open between the deadlines: %v", err)
	}

	time.Sleep(time.Until(m.FreezesAt) + 100*time.Millisecond)
	if _, err := r.PlaceStake(t.Context(), m, a.ID, 8, 50_00, 0, time.Now().UTC()); !errors.Is(err, market.ErrStakingClosed) {
		t.Errorf("stake after freeze: got %v, want ErrStakingClosed", err)
	}
}

// Whatever the tests above did, the store must still balance. This asserts the
// whole store, so a run that genuinely destroyed money fails here even if the
// damage was done earlier — which is the point.
func TestMoneyIsConservedAcrossTheStore(t *testing.T) {
	_, pg, _ := newRunner(t, fixedOracle{spot})

	a, err := pg.Audit(t.Context())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if got := a.Spendable + a.Escrowed + a.Rake; got != a.Deposited {
		t.Fatalf("deposited %d but spendable %d + escrow %d + rake %d = %d",
			a.Deposited, a.Spendable, a.Escrowed, a.Rake, got)
	}
	t.Logf("deposited %d = spendable %d + escrow %d + rake %d",
		a.Deposited, a.Spendable, a.Escrowed, a.Rake)
}

// A payout floor must be enforced where the pool is written, not merely
// checked beforehand: another stake landing in between would dilute the cell
// below the floor the caller was promised.
//
// Dilution needs stake on other cells: when a cell is the whole pool it always
// pays exactly one minus the rake, however much joins it.
func TestPayoutFloorRejectsADilutedCell(t *testing.T) {
	r, pg, _ := newRunner(t, fixedOracle{spot})
	m := openMarket(t, r)

	elsewhere, first, second := funded(t, pg, 500_00), funded(t, pg, 500_00), funded(t, pg, 500_00)
	if _, err := r.PlaceStake(t.Context(), m, elsewhere.ID, 4, 300_00, 0, time.Now().UTC()); err != nil {
		t.Fatalf("seeding another cell: %v", err)
	}

	// $400 pool less 250 bps, all of cell 9 held by one stake: 3.90.
	if _, err := r.PlaceStake(t.Context(), m, first.ID, 9, 100_00, 3.5, time.Now().UTC()); err != nil {
		t.Fatalf("floor below the achievable multiple should pass: %v", err)
	}

	// Joining cell 9 halves the holder's share, so it now pays 2.44.
	if _, err := r.PlaceStake(t.Context(), m, second.ID, 9, 100_00, 3.5, time.Now().UTC()); !errors.Is(err, store.ErrPriceMoved) {
		t.Fatalf("want ErrPriceMoved once the cell is crowded, got %v", err)
	}
	if got := available(t, pg, second.ID); got != 500_00 {
		t.Errorf("rejected stake moved the balance to %d", got)
	}

	if _, err := r.PlaceStake(t.Context(), m, second.ID, 9, 100_00, 2.4, time.Now().UTC()); err != nil {
		t.Fatalf("stake within its floor should pass: %v", err)
	}
}

// Two stakes racing for the same cell must not both read the pool before
// either writes to it: that lets both pass a floor that neither actually
// clears once both land. The market row is locked for the same span as the
// pool read, so the second transaction can't even read totals until the
// first has committed or rolled back.
func TestConcurrentStakesCannotDiluteBelowTheFloor(t *testing.T) {
	r, pg, _ := newRunner(t, fixedOracle{spot})
	m := openMarket(t, r)

	elsewhere, first, second := funded(t, pg, 500_00), funded(t, pg, 500_00), funded(t, pg, 500_00)
	if _, err := r.PlaceStake(t.Context(), m, elsewhere.ID, 4, 300_00, 0, time.Now().UTC()); err != nil {
		t.Fatalf("seeding another cell: %v", err)
	}

	// Alone, either stake would clear 3.5 (3.90). Both landing in cell 9
	// together pays 2.4375 — below the floor both are racing to claim.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, acct := range []store.Account{first, second} {
		wg.Add(1)
		go func(i int, acct store.Account) {
			defer wg.Done()
			_, errs[i] = r.PlaceStake(t.Context(), m, acct.ID, 9, 100_00, 3.5, time.Now().UTC())
		}(i, acct)
	}
	wg.Wait()

	var passed, rejected int
	for _, err := range errs {
		switch {
		case err == nil:
			passed++
		case errors.Is(err, store.ErrPriceMoved):
			rejected++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if passed != 1 || rejected != 1 {
		t.Fatalf("got %d passed, %d rejected racing for the same cell; want exactly one of each", passed, rejected)
	}

	stakes, err := pg.Stakes(t.Context(), m.ID)
	if err != nil {
		t.Fatalf("Stakes: %v", err)
	}
	var cellTotal int64
	for _, s := range stakes {
		if s.CellIndex == 9 {
			cellTotal += s.Amount
		}
	}
	if cellTotal != 100_00 {
		t.Fatalf("cell 9 holds %d, want exactly one 100_00 stake to have landed", cellTotal)
	}
}

// A cell that is the whole pool pays one minus the rake no matter how much
// joins it, so a floor at that level must never block a stake.
func TestFloorAtRakeAlwaysClears(t *testing.T) {
	r, pg, _ := newRunner(t, fixedOracle{spot})
	m := openMarket(t, r)

	a := funded(t, pg, 500_00)
	for range 3 {
		if _, err := r.PlaceStake(t.Context(), m, a.ID, 11, 50_00, 0.975, time.Now().UTC()); err != nil {
			t.Fatalf("sole cell should always pay 0.975: %v", err)
		}
	}
}
