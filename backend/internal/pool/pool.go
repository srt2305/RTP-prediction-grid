// Package pool implements pari-mutuel settlement. It never imports pricing:
// payouts come from realised stakes alone, so the house carries no exposure.
package pool

import (
	"cmp"
	"fmt"
	"math/bits"
	"slices"
)

const bpsDenominator = 10000

// Amounts are int64 minor units of the account currency (cents), never float64.
type Stake struct {
	ID        string
	CellIndex int
	Amount    int64
}

type Payout struct {
	StakeID string
	Amount  int64
}

type Outcome string

const (
	OutcomePaid     Outcome = "paid"
	OutcomeRefunded Outcome = "refunded"
	OutcomeVoid     Outcome = "void"
)

type Settlement struct {
	Outcome     Outcome
	TotalStaked int64
	Rake        int64
	Payouts     []Payout
}

// Settle guarantees sum(payouts) + rake == total staked. An unstaked winning
// cell refunds everyone and takes no rake; an empty pool voids.
func Settle(stakes []Stake, winningCell, rakeBps int) (Settlement, error) {
	if rakeBps < 0 || rakeBps >= bpsDenominator {
		return Settlement{}, fmt.Errorf("pool: rake %d bps outside [0, %d)", rakeBps, bpsDenominator)
	}
	if winningCell < 0 {
		return Settlement{}, fmt.Errorf("pool: winning cell %d is negative", winningCell)
	}

	var total, winningTotal int64
	var winners []Stake
	for _, s := range stakes {
		if s.Amount <= 0 {
			return Settlement{}, fmt.Errorf("pool: stake %q has non-positive amount %d", s.ID, s.Amount)
		}
		total += s.Amount
		if s.CellIndex == winningCell {
			winningTotal += s.Amount
			winners = append(winners, s)
		}
	}

	switch {
	case total == 0:
		return Settlement{Outcome: OutcomeVoid}, nil
	case winningTotal == 0:
		return Settlement{
			Outcome:     OutcomeRefunded,
			TotalStaked: total,
			Payouts:     refundAll(stakes),
		}, nil
	}

	rake := total * int64(rakeBps) / bpsDenominator
	return Settlement{
		Outcome:     OutcomePaid,
		TotalStaked: total,
		Rake:        rake,
		Payouts:     distribute(winners, winningTotal, total-rake),
	}, nil
}

// Refund returns every stake untouched, for a market that cannot settle at all.
// No rake is taken: the rake pays for dividing a pool, and none was divided.
func Refund(stakes []Stake) Settlement {
	var total int64
	for _, s := range stakes {
		total += s.Amount
	}
	if total == 0 {
		return Settlement{Outcome: OutcomeVoid}
	}
	return Settlement{
		Outcome:     OutcomeRefunded,
		TotalStaked: total,
		Payouts:     refundAll(stakes),
	}
}

func refundAll(stakes []Stake) []Payout {
	payouts := make([]Payout, len(stakes))
	for i, s := range stakes {
		payouts[i] = Payout{StakeID: s.ID, Amount: s.Amount}
	}
	sortPayouts(payouts)
	return payouts
}

func distribute(winners []Stake, winningTotal, net int64) []Payout {
	type share struct {
		payout    Payout
		remainder uint64
	}

	shares := make([]share, len(winners))
	var assigned int64
	for i, w := range winners {
		// net*w.Amount overflows int64 for large pools, so divide in 128 bits.
		hi, lo := bits.Mul64(uint64(net), uint64(w.Amount))
		whole, rem := bits.Div64(hi, lo, uint64(winningTotal))
		shares[i] = share{Payout{StakeID: w.ID, Amount: int64(whole)}, rem}
		assigned += int64(whole)
	}

	slices.SortFunc(shares, func(a, b share) int {
		if c := cmp.Compare(b.remainder, a.remainder); c != 0 {
			return c
		}
		return cmp.Compare(a.payout.StakeID, b.payout.StakeID)
	})
	for i := range net - assigned {
		shares[i].payout.Amount++
	}

	payouts := make([]Payout, len(shares))
	for i, s := range shares {
		payouts[i] = s.payout
	}
	sortPayouts(payouts)
	return payouts
}

func sortPayouts(payouts []Payout) {
	slices.SortFunc(payouts, func(a, b Payout) int { return cmp.Compare(a.StakeID, b.StakeID) })
}
