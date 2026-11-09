package pool

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

func totalOf(payouts []Payout) int64 {
	var sum int64
	for _, p := range payouts {
		sum += p.Amount
	}
	return sum
}

func TestSettleEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		stakes      []Stake
		winningCell int
		rakeBps     int
		want        Settlement
	}{
		{
			name:        "empty pool voids",
			stakes:      nil,
			winningCell: 3,
			rakeBps:     250,
			want:        Settlement{Outcome: OutcomeVoid},
		},
		{
			name: "nobody on the winning cell refunds everyone in full",
			stakes: []Stake{
				{ID: "a", CellIndex: 1, Amount: 1000},
				{ID: "b", CellIndex: 2, Amount: 500},
			},
			winningCell: 7,
			rakeBps:     250,
			want: Settlement{
				Outcome:     OutcomeRefunded,
				TotalStaked: 1500,
				Rake:        0,
				Payouts:     []Payout{{"a", 1000}, {"b", 500}},
			},
		},
		{
			name:        "sole participant cannot beat the rake",
			stakes:      []Stake{{ID: "a", CellIndex: 4, Amount: 10000}},
			winningCell: 4,
			rakeBps:     250,
			want: Settlement{
				Outcome:     OutcomePaid,
				TotalStaked: 10000,
				Rake:        250,
				Payouts:     []Payout{{"a", 9750}},
			},
		},
		{
			name: "losers fund the winner",
			stakes: []Stake{
				{ID: "a", CellIndex: 4, Amount: 1000},
				{ID: "b", CellIndex: 5, Amount: 3000},
			},
			winningCell: 4,
			rakeBps:     500,
			want: Settlement{
				Outcome:     OutcomePaid,
				TotalStaked: 4000,
				Rake:        200,
				Payouts:     []Payout{{"a", 3800}},
			},
		},
		{
			name: "indivisible remainder goes to the largest fractional claim",
			stakes: []Stake{
				{ID: "a", CellIndex: 1, Amount: 1},
				{ID: "b", CellIndex: 1, Amount: 1},
				{ID: "c", CellIndex: 1, Amount: 1},
				{ID: "d", CellIndex: 2, Amount: 1},
			},
			winningCell: 1,
			rakeBps:     0,
			want: Settlement{
				Outcome:     OutcomePaid,
				TotalStaked: 4,
				Rake:        0,
				Payouts:     []Payout{{"a", 2}, {"b", 1}, {"c", 1}},
			},
		},
		{
			name:        "zero rake pays the whole pool out",
			stakes:      []Stake{{ID: "a", CellIndex: 0, Amount: 777}},
			winningCell: 0,
			rakeBps:     0,
			want: Settlement{
				Outcome:     OutcomePaid,
				TotalStaked: 777,
				Rake:        0,
				Payouts:     []Payout{{"a", 777}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Settle(tt.stakes, tt.winningCell, tt.rakeBps)
			if err != nil {
				t.Fatalf("Settle: %v", err)
			}
			if got.Outcome != tt.want.Outcome || got.TotalStaked != tt.want.TotalStaked || got.Rake != tt.want.Rake {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if len(got.Payouts) != len(tt.want.Payouts) {
				t.Fatalf("got payouts %+v, want %+v", got.Payouts, tt.want.Payouts)
			}
			for i := range got.Payouts {
				if got.Payouts[i] != tt.want.Payouts[i] {
					t.Fatalf("payout %d: got %+v, want %+v", i, got.Payouts[i], tt.want.Payouts[i])
				}
			}
		})
	}
}

func TestSettleRejectsBadInput(t *testing.T) {
	tests := []struct {
		name        string
		stakes      []Stake
		winningCell int
		rakeBps     int
	}{
		{"rake at 100 percent", nil, 0, 10000},
		{"negative rake", nil, 0, -1},
		{"negative winning cell", nil, -1, 250},
		{"non-positive stake", []Stake{{ID: "a", CellIndex: 0, Amount: 0}}, 0, 250},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Settle(tt.stakes, tt.winningCell, tt.rakeBps); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

// The pool is conserved exactly: nothing is created, nothing is dropped in the
// rounding, and the house never pays more in than it takes out.
func TestPoolIsConserved(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 13))
	for range 5000 {
		cells := 1 + r.IntN(12)
		stakes := make([]Stake, 1+r.IntN(40))
		for i := range stakes {
			stakes[i] = Stake{
				ID:        fmt.Sprintf("s%03d", i),
				CellIndex: r.IntN(cells),
				Amount:    1 + r.Int64N(1_000_000),
			}
		}
		rakeBps := r.IntN(bpsDenominator)
		winning := r.IntN(cells)

		got, err := Settle(stakes, winning, rakeBps)
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}

		var staked int64
		for _, s := range stakes {
			staked += s.Amount
		}
		paid := totalOf(got.Payouts)

		if got.Outcome == OutcomeVoid {
			t.Fatalf("non-empty pool voided: %+v", got)
		}
		if paid+got.Rake != staked {
			t.Fatalf("paid %d + rake %d != staked %d (outcome %s)", paid, got.Rake, staked, got.Outcome)
		}
		if got.Rake > staked*int64(rakeBps)/bpsDenominator {
			t.Fatalf("rake %d exceeds %d bps of %d", got.Rake, rakeBps, staked)
		}
		for _, p := range got.Payouts {
			if p.Amount < 0 {
				t.Fatalf("negative payout %+v", p)
			}
		}
	}
}

func TestPayoutIsProportionalToStake(t *testing.T) {
	stakes := []Stake{
		{ID: "a", CellIndex: 1, Amount: 250_000},
		{ID: "b", CellIndex: 1, Amount: 750_000},
		{ID: "c", CellIndex: 2, Amount: 1_000_000},
	}
	got, err := Settle(stakes, 1, 250)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}

	net := float64(got.TotalStaked - got.Rake)
	for _, p := range got.Payouts {
		var stake int64
		for _, s := range stakes {
			if s.ID == p.StakeID {
				stake = s.Amount
			}
		}
		want := net * float64(stake) / 1_000_000
		if math.Abs(float64(p.Amount)-want) > 1 {
			t.Fatalf("stake %s: paid %d, want about %.0f", p.StakeID, p.Amount, want)
		}
	}
}

// The house takes its rake whatever the outcome, which is the whole point of
// pari-mutuel settlement: no cell can produce a house loss.
func TestHouseNeverLoses(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 19))
	for range 2000 {
		stakes := make([]Stake, 1+r.IntN(20))
		for i := range stakes {
			stakes[i] = Stake{
				ID:        fmt.Sprintf("s%03d", i),
				CellIndex: r.IntN(6),
				Amount:    1 + r.Int64N(500_000),
			}
		}
		for winning := range 6 {
			got, err := Settle(stakes, winning, 250)
			if err != nil {
				t.Fatalf("Settle: %v", err)
			}
			if got.Rake < 0 {
				t.Fatalf("negative rake %d", got.Rake)
			}
			if totalOf(got.Payouts) > got.TotalStaked {
				t.Fatalf("paid out %d on a pool of %d", totalOf(got.Payouts), got.TotalStaked)
			}
		}
	}
}

// A market that never gets an official price must return every stake. The
// ledger has already debited them, so anything less destroys money.
func TestRefundReturnsEveryStake(t *testing.T) {
	stakes := []Stake{
		{ID: "a", CellIndex: 3, Amount: 25_000},
		{ID: "b", CellIndex: 3, Amount: 75_001},
		{ID: "c", CellIndex: 9, Amount: 1},
	}

	got := Refund(stakes)
	if got.Outcome != OutcomeRefunded {
		t.Fatalf("outcome %q, want %q", got.Outcome, OutcomeRefunded)
	}
	if got.Rake != 0 {
		t.Fatalf("rake %d, want 0", got.Rake)
	}
	if paid := totalOf(got.Payouts); paid != got.TotalStaked || paid != 100_002 {
		t.Fatalf("refunded %d of %d staked", paid, got.TotalStaked)
	}
	for _, p := range got.Payouts {
		for _, s := range stakes {
			if s.ID == p.StakeID && p.Amount != s.Amount {
				t.Fatalf("stake %s staked %d, refunded %d", s.ID, s.Amount, p.Amount)
			}
		}
	}
}

func TestRefundOfEmptyPoolVoids(t *testing.T) {
	if got := Refund(nil); got.Outcome != OutcomeVoid || got.TotalStaked != 0 {
		t.Fatalf("got %+v, want a void with nothing staked", got)
	}
}

// Payouts are proportional to stake, so splitting a position into many — across
// stakes or across accounts — must not pay better than holding it whole. Only
// the largest-remainder rounding can differ, and by at most one minor unit.
func TestSplittingGainsAtMostOneMinorUnit(t *testing.T) {
	r := rand.New(rand.NewPCG(23, 29))
	worst := int64(0)

	for range 20000 {
		mine := 10_000 + r.Int64N(500_000)
		rival := 10_000 + r.Int64N(500_000)
		parts := 1 + r.IntN(50)
		rakeBps := r.IntN(1000)

		whole, err := Settle([]Stake{
			{ID: "mine", CellIndex: 1, Amount: mine},
			{ID: "rival", CellIndex: 1, Amount: rival},
		}, 1, rakeBps)
		if err != nil {
			t.Fatal(err)
		}

		split := make([]Stake, 0, parts+1)
		each, rem := mine/int64(parts), mine%int64(parts)
		for i := range parts {
			amount := each
			if int64(i) < rem {
				amount++
			}
			if amount == 0 {
				continue
			}
			split = append(split, Stake{ID: fmt.Sprintf("m%04d", i), CellIndex: 1, Amount: amount})
		}
		split = append(split, Stake{ID: "rival", CellIndex: 1, Amount: rival})

		got, err := Settle(split, 1, rakeBps)
		if err != nil {
			t.Fatal(err)
		}

		var asWhole, asSplit int64
		for _, p := range whole.Payouts {
			if p.StakeID == "mine" {
				asWhole = p.Amount
			}
		}
		for _, p := range got.Payouts {
			if p.StakeID != "rival" {
				asSplit += p.Amount
			}
		}

		if d := asSplit - asWhole; d > worst {
			worst = d
		}
		if worst > 1 {
			t.Fatalf("splitting %d into %d parts gained %d minor units against a rival of %d at %d bps",
				mine, parts, worst, rival, rakeBps)
		}
	}
	t.Logf("worst advantage from splitting: %d minor unit", worst)
}
