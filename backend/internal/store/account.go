package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInsufficientFunds = errors.New("store: insufficient available balance")
	ErrPriceMoved        = errors.New("store: cell pays less than the floor you set")
)

const bpsDenominator = 10000

// Amounts are signed: a stake debits, everything else credits.
const (
	KindDeposit = "deposit"
	KindStake   = "stake"
	KindPayout  = "payout"
	KindRefund  = "refund"
	KindCancel  = "cancel"
)

type Account struct {
	ID     string
	Handle string
}

type LedgerEntry struct {
	Kind     string    `json:"kind"`
	Amount   int64     `json:"amountMinor"`
	MarketID string    `json:"marketId"`
	At       time.Time `json:"at"`
}

// LookupAccount finds an account without creating one, so read-only requests
// cannot mint rows for every crawler or mistyped handle.
func (p *Postgres) LookupAccount(ctx context.Context, handle string) (Account, bool, error) {
	a := Account{Handle: handle}
	err := p.pool.QueryRow(ctx, `SELECT id::text FROM account WHERE handle = $1`, handle).Scan(&a.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, fmt.Errorf("look up account %q: %w", handle, err)
	}
	return a, true, nil
}

func (p *Postgres) EnsureAccount(ctx context.Context, handle string) (Account, error) {
	a := Account{Handle: handle}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO account (id, handle) VALUES ($1, $2)
		 ON CONFLICT (handle) DO UPDATE SET handle = EXCLUDED.handle
		 RETURNING id::text`, uuid.NewString(), handle).Scan(&a.ID)
	if err != nil {
		return Account{}, fmt.Errorf("ensure account %q: %w", handle, err)
	}
	return a, nil
}

func (p *Postgres) Deposit(ctx context.Context, accountID string, amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("store: deposit %d must be positive", amount)
	}
	_, err := p.pool.Exec(ctx,
		`INSERT INTO ledger (account_id, kind, amount_minor) VALUES ($1, $2, $3)`,
		accountID, KindDeposit, amount)
	if err != nil {
		return fmt.Errorf("deposit to %s: %w", accountID, err)
	}
	return nil
}

func (p *Postgres) Balance(ctx context.Context, accountID string) (available, escrowed int64, err error) {
	err = p.pool.QueryRow(ctx,
		`SELECT coalesce(sum(amount_minor), 0) FROM ledger WHERE account_id = $1`,
		accountID).Scan(&available)
	if err != nil {
		return 0, 0, fmt.Errorf("read balance of %s: %w", accountID, err)
	}

	err = p.pool.QueryRow(ctx,
		`SELECT coalesce(sum(s.amount_minor), 0)
		 FROM stake s JOIN market m ON m.id = s.market_id
		 WHERE s.account_id = $1 AND s.cancelled_at IS NULL AND m.state IN ('open', 'frozen')`,
		accountID).Scan(&escrowed)
	if err != nil {
		return 0, 0, fmt.Errorf("read escrow of %s: %w", accountID, err)
	}
	return available, escrowed, nil
}

func (p *Postgres) Ledger(ctx context.Context, accountID string, limit int) ([]LedgerEntry, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT kind, amount_minor, coalesce(market_id::text, ''), at
		 FROM ledger WHERE account_id = $1 ORDER BY at DESC, id DESC LIMIT $2`,
		accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("read ledger of %s: %w", accountID, err)
	}
	defer rows.Close()

	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.Kind, &e.Amount, &e.MarketID, &e.At); err != nil {
			return nil, fmt.Errorf("scan ledger entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Debit and stake commit together, so a stake never exists without the money
// behind it. The account row is locked first or concurrent stakes both pass.
//
// minMultiple is a floor on what this cell would pay once the stake is in,
// checked against the stake rows inside the same transaction. Checking outside
// it would be advisory only: another stake landing in between would dilute the
// cell below the floor the caller was promised. The market row is locked too,
// for the same reason at the transaction level: under READ COMMITTED, two
// concurrent stakes into the same cell would otherwise both read the
// pre-insert pool and both pass a floor that neither actually clears.
func (p *Postgres) PlaceStake(ctx context.Context, stakeID, marketID, accountID string,
	cell int, amount int64, minMultiple float64, rakeBps int, at time.Time) error {

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin stake %s: %w", stakeID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM account WHERE id = $1 FOR UPDATE`,
		accountID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("store: no account %s", accountID)
		}
		return fmt.Errorf("lock account %s: %w", accountID, err)
	}

	var available int64
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(sum(amount_minor), 0) FROM ledger WHERE account_id = $1`,
		accountID).Scan(&available); err != nil {
		return fmt.Errorf("read balance of %s: %w", accountID, err)
	}
	if available < amount {
		return fmt.Errorf("%w: %s has %d, needs %d", ErrInsufficientFunds, accountID, available, amount)
	}

	if minMultiple > 0 {
		var lockedMarket string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM market WHERE id = $1 FOR UPDATE`,
			marketID).Scan(&lockedMarket); err != nil {
			return fmt.Errorf("lock market %s: %w", marketID, err)
		}

		var poolTotal, cellTotal int64
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(sum(amount_minor), 0),
			        coalesce(sum(amount_minor) FILTER (WHERE cell_index = $2), 0)
			   FROM stake WHERE market_id = $1 AND cancelled_at IS NULL`,
			marketID, cell).Scan(&poolTotal, &cellTotal); err != nil {
			return fmt.Errorf("read pool of %s: %w", marketID, err)
		}

		net := float64(poolTotal+amount) * (1 - float64(rakeBps)/bpsDenominator)
		got := net / float64(cellTotal+amount)
		if got < minMultiple {
			return fmt.Errorf("%w: cell %d would pay %.4f, floor is %.4f",
				ErrPriceMoved, cell, got, minMultiple)
		}
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO stake (id, market_id, account_id, cell_index, amount_minor, placed_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		stakeID, marketID, accountID, cell, amount, at); err != nil {
		return fmt.Errorf("insert stake %s: %w", stakeID, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ledger (account_id, kind, amount_minor, market_id, stake_id, at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		accountID, KindStake, -amount, marketID, stakeID, at); err != nil {
		return fmt.Errorf("debit for stake %s: %w", stakeID, err)
	}
	return tx.Commit(ctx)
}

type AuditTotals struct {
	Deposited int64
	Spendable int64
	Escrowed  int64
	Rake      int64
}

// Escrow counts only stakes with a matching ledger debit; one without is money
// that never left a balance.
func (p *Postgres) Audit(ctx context.Context) (AuditTotals, error) {
	var a AuditTotals
	err := p.pool.QueryRow(ctx, `
		SELECT
		  (SELECT coalesce(sum(amount_minor), 0) FROM ledger WHERE kind = $1),
		  (SELECT coalesce(sum(amount_minor), 0) FROM ledger),
		  (SELECT coalesce(sum(s.amount_minor), 0) FROM stake s
		     JOIN market m ON m.id = s.market_id
		    WHERE s.cancelled_at IS NULL AND m.state IN ('open', 'frozen')
		      AND EXISTS (SELECT 1 FROM ledger l WHERE l.stake_id = s.id AND l.kind = $2)),
		  (SELECT coalesce(sum(rake_minor), 0) FROM market)
	`, KindDeposit, KindStake).Scan(&a.Deposited, &a.Spendable, &a.Escrowed, &a.Rake)
	if err != nil {
		return AuditTotals{}, fmt.Errorf("audit totals: %w", err)
	}
	return a, nil
}

type Position struct {
	StakeID     string    `json:"stakeId"`
	MarketID    string    `json:"marketId"`
	CellIndex   int       `json:"cellIndex"`
	AmountMinor int64     `json:"amountMinor"`
	PlacedAt    time.Time `json:"placedAt"`
	Cancelled   bool      `json:"cancelled"`
	State       string    `json:"marketState"`
	PaidMinor   int64     `json:"paidMinor"`
	Lower       *float64  `json:"lower"`
	Upper       *float64  `json:"upper"`
	SettledCell *int      `json:"settledCell"`
	AmendsAt    time.Time `json:"amendsAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

func (p *Postgres) Positions(ctx context.Context, accountID string, limit int) ([]Position, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT s.id::text, s.market_id::text, s.cell_index, s.amount_minor, s.placed_at,
		        s.cancelled_at IS NOT NULL, m.state, coalesce(pay.amount_minor, 0),
		        CASE WHEN s.cell_index BETWEEN 1 AND m.bounded_cells
		             THEN m.floor_price + m.cell_width * (s.cell_index - 1) END,
		        CASE WHEN s.cell_index BETWEEN 1 AND m.bounded_cells
		             THEN m.floor_price + m.cell_width * s.cell_index END,
		        m.settled_cell, m.amends_at, m.expires_at
		   FROM stake s
		   JOIN market m ON m.id = s.market_id
		   LEFT JOIN payout pay ON pay.stake_id = s.id
		  WHERE s.account_id = $1
		  ORDER BY s.placed_at DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("read positions of %s: %w", accountID, err)
	}
	defer rows.Close()

	positions := []Position{}
	for rows.Next() {
		var pos Position
		if err := rows.Scan(&pos.StakeID, &pos.MarketID, &pos.CellIndex, &pos.AmountMinor,
			&pos.PlacedAt, &pos.Cancelled, &pos.State, &pos.PaidMinor,
			&pos.Lower, &pos.Upper, &pos.SettledCell, &pos.AmendsAt, &pos.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		positions = append(positions, pos)
	}
	return positions, rows.Err()
}
