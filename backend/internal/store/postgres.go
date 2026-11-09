package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/feed"
	"github.com/srt2305/RTP-prediction-grid/backend/internal/pool"
)

var ErrNoTicks = errors.New("store: no ticks for symbol at or before that time")

type Postgres struct {
	pool *pgxpool.Pool
}

func OpenPostgres(ctx context.Context, dsn string, log *slog.Logger) (*Postgres, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect for migration: %w", err)
	}
	if err := Migrate(ctx, conn, log); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	if err := conn.Close(ctx); err != nil {
		return nil, fmt.Errorf("close migration connection: %w", err)
	}

	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	return &Postgres{pool: p}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) SaveTicks(ctx context.Context, ticks []feed.Tick) error {
	rows := make([][]any, len(ticks))
	for i, t := range ticks {
		rows[i] = []any{t.Symbol, t.At, t.Price}
	}
	_, err := p.pool.CopyFrom(ctx, pgx.Identifier{"tick"},
		[]string{"symbol", "ts", "price"}, pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("copy %d ticks: %w", len(ticks), err)
	}
	return nil
}

// PriceAt returns the last trade at or before at.
func (p *Postgres) PriceAt(ctx context.Context, symbol string, at time.Time) (float64, error) {
	var price float64
	err := p.pool.QueryRow(ctx,
		`SELECT price FROM tick WHERE symbol = $1 AND ts <= $2 ORDER BY ts DESC LIMIT 1`,
		symbol, at).Scan(&price)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNoTicks
	}
	if err != nil {
		return 0, fmt.Errorf("read price for %s at %s: %w", symbol, at, err)
	}
	return price, nil
}

func (p *Postgres) HasTicksAfter(ctx context.Context, symbol string, at time.Time) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM tick WHERE symbol = $1 AND ts > $2)`, symbol, at).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check ticks after %s: %w", at, err)
	}
	return exists, nil
}

func (p *Postgres) LatestPrice(ctx context.Context, symbol string) (float64, error) {
	return p.PriceAt(ctx, symbol, time.Now().UTC())
}

type MarketRow struct {
	ID           string
	Symbol       string
	State        string
	FloorPrice   float64
	CellWidth    float64
	BoundedCells int
	RakeBps      int
	OpensAt      time.Time
	AmendsAt     time.Time
	FreezesAt    time.Time
	ExpiresAt    time.Time
}

func (p *Postgres) CreateMarket(ctx context.Context, m MarketRow) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO market (id, symbol, state, floor_price, cell_width, bounded_cells,
		                     rake_bps, opens_at, amends_at, freezes_at, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		m.ID, m.Symbol, m.State, m.FloorPrice, m.CellWidth, m.BoundedCells,
		m.RakeBps, m.OpensAt, m.AmendsAt, m.FreezesAt, m.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert market %s: %w", m.ID, err)
	}
	return nil
}

func (p *Postgres) SetMarketState(ctx context.Context, id, state string) error {
	_, err := p.pool.Exec(ctx, `UPDATE market SET state = $2 WHERE id = $1`, id, state)
	if err != nil {
		return fmt.Errorf("update market %s state: %w", id, err)
	}
	return nil
}

func (p *Postgres) Stakes(ctx context.Context, marketID string) ([]pool.Stake, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id::text, cell_index, amount_minor FROM stake
		 WHERE market_id = $1 AND cancelled_at IS NULL ORDER BY id`, marketID)
	if err != nil {
		return nil, fmt.Errorf("query stakes for %s: %w", marketID, err)
	}
	defer rows.Close()

	var stakes []pool.Stake
	for rows.Next() {
		var s pool.Stake
		if err := rows.Scan(&s.ID, &s.CellIndex, &s.Amount); err != nil {
			return nil, fmt.Errorf("scan stake: %w", err)
		}
		stakes = append(stakes, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stakes: %w", err)
	}
	return stakes, nil
}

var ErrStakeNotCancellable = errors.New("store: stake is already cancelled or does not exist")

// Soft-deleted, not removed: a cancelled position moved everyone else's odds
// while it stood, so it stays auditable.
func (p *Postgres) CancelStake(ctx context.Context, stakeID, marketID string, at time.Time) (int, int64, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin cancel %s: %w", stakeID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var cell int
	var amount int64
	var accountID string
	err = tx.QueryRow(ctx,
		`UPDATE stake SET cancelled_at = $3
		 WHERE id = $1 AND market_id = $2 AND cancelled_at IS NULL
		 RETURNING cell_index, amount_minor, account_id::text`,
		stakeID, marketID, at).Scan(&cell, &amount, &accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, fmt.Errorf("%w: %s", ErrStakeNotCancellable, stakeID)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("cancel stake %s: %w", stakeID, err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO ledger (account_id, kind, amount_minor, market_id, stake_id, at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		accountID, KindCancel, amount, marketID, stakeID, at); err != nil {
		return 0, 0, fmt.Errorf("credit cancelled stake %s: %w", stakeID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit cancel %s: %w", stakeID, err)
	}
	return cell, amount, nil
}

// One transaction: a market is never observably settled with payouts missing.
func (p *Postgres) RecordSettlement(ctx context.Context, marketID, state string, price float64, cell int, s pool.Settlement) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin settlement for %s: %w", marketID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE market SET state = $2, settled_price = $3, settled_cell = $4, outcome = $5, rake_minor = $6
		 WHERE id = $1`,
		marketID, state, price, cell, string(s.Outcome), s.Rake); err != nil {
		return fmt.Errorf("update settled market %s: %w", marketID, err)
	}

	if err := writePayouts(ctx, tx, marketID, s); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordVoid settles a market that never got an official price. Stakes are
// refunded rather than forfeited: the ledger already debited them, so leaving
// them unpaid would destroy the money and break the audit invariant.
func (p *Postgres) RecordVoid(ctx context.Context, marketID, state string, s pool.Settlement) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin void for %s: %w", marketID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE market SET state = $2, outcome = $3, rake_minor = 0 WHERE id = $1`,
		marketID, state, string(s.Outcome)); err != nil {
		return fmt.Errorf("update voided market %s: %w", marketID, err)
	}
	if err := writePayouts(ctx, tx, marketID, s); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func writePayouts(ctx context.Context, tx pgx.Tx, marketID string, s pool.Settlement) error {
	kind := KindPayout
	if s.Outcome == pool.OutcomeRefunded {
		kind = KindRefund
	}
	for _, payout := range s.Payouts {
		if _, err := tx.Exec(ctx,
			`INSERT INTO payout (stake_id, market_id, amount_minor) VALUES ($1,$2,$3)`,
			payout.StakeID, marketID, payout.Amount); err != nil {
			return fmt.Errorf("insert payout for stake %s: %w", payout.StakeID, err)
		}
		if payout.Amount == 0 {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ledger (account_id, kind, amount_minor, market_id, stake_id)
			 SELECT account_id, $2, $3, $4, id FROM stake WHERE id = $1`,
			payout.StakeID, kind, payout.Amount, marketID); err != nil {
			return fmt.Errorf("credit payout for stake %s: %w", payout.StakeID, err)
		}
	}
	return nil
}

type PricePoint struct {
	At    time.Time `json:"at"`
	Price float64   `json:"price"`
}

// PricePath downsamples ticks with time_bucket so a chart gets an even series
// regardless of how bursty the feed was.
func (p *Postgres) PricePath(ctx context.Context, symbol string, from, to time.Time, points int) ([]PricePoint, error) {
	if points < 2 {
		points = 2
	}
	bucket := to.Sub(from) / time.Duration(points)
	if bucket < time.Millisecond {
		bucket = time.Millisecond
	}

	rows, err := p.pool.Query(ctx,
		`SELECT time_bucket($4::interval, ts) AS slot, last(price, ts)
		   FROM tick WHERE symbol = $1 AND ts >= $2 AND ts <= $3
		  GROUP BY slot ORDER BY slot`, symbol, from, to, bucket)
	if err != nil {
		return nil, fmt.Errorf("read price path for %s: %w", symbol, err)
	}
	defer rows.Close()

	path := []PricePoint{}
	for rows.Next() {
		var pt PricePoint
		if err := rows.Scan(&pt.At, &pt.Price); err != nil {
			return nil, fmt.Errorf("scan price point: %w", err)
		}
		path = append(path, pt)
	}
	return path, rows.Err()
}

type MarketDetail struct {
	ID           string    `json:"id"`
	Symbol       string    `json:"symbol"`
	State        string    `json:"state"`
	FloorPrice   float64   `json:"floorPrice"`
	CellWidth    float64   `json:"cellWidth"`
	BoundedCells int       `json:"boundedCells"`
	RakeBps      int       `json:"rakeBps"`
	OpensAt      time.Time `json:"opensAt"`
	AmendsAt     time.Time `json:"amendsAt"`
	FreezesAt    time.Time `json:"freezesAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	SettledPrice *float64  `json:"settledPrice"`
	SettledCell  *int      `json:"settledCell"`
	Outcome      *string   `json:"outcome"`
	RakeMinor    *int64    `json:"rakeMinor"`
}

func (p *Postgres) Market(ctx context.Context, id string) (MarketDetail, error) {
	var m MarketDetail
	err := p.pool.QueryRow(ctx,
		`SELECT id::text, symbol, state, floor_price, cell_width, bounded_cells, rake_bps,
		        opens_at, amends_at, freezes_at, expires_at,
		        settled_price, settled_cell, outcome, rake_minor
		   FROM market WHERE id = $1`, id).Scan(
		&m.ID, &m.Symbol, &m.State, &m.FloorPrice, &m.CellWidth, &m.BoundedCells, &m.RakeBps,
		&m.OpensAt, &m.AmendsAt, &m.FreezesAt, &m.ExpiresAt,
		&m.SettledPrice, &m.SettledCell, &m.Outcome, &m.RakeMinor)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, fmt.Errorf("store: no market %s", id)
	}
	if err != nil {
		return m, fmt.Errorf("read market %s: %w", id, err)
	}
	return m, nil
}

type StakeOutcome struct {
	StakedMinor int64
	PaidMinor   int64
	Cell        int
	Resolved    bool
}

func (p *Postgres) StakeOutcome(ctx context.Context, stakeID string) (StakeOutcome, error) {
	var out StakeOutcome
	var paid *int64
	err := p.pool.QueryRow(ctx,
		`SELECT s.amount_minor, s.cell_index, pay.amount_minor, m.state IN ('settled', 'voided')
		   FROM stake s
		   JOIN market m ON m.id = s.market_id
		   LEFT JOIN payout pay ON pay.stake_id = s.id
		  WHERE s.id = $1`, stakeID).Scan(&out.StakedMinor, &out.Cell, &paid, &out.Resolved)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("read outcome for %s: %w", stakeID, err)
	}
	if paid != nil {
		out.PaidMinor = *paid
	}
	return out, nil
}

// SettledTotals rebuilds per-cell totals from the stake rows. Redis is dropped
// at settlement, so a chart of a finished market has to read the record.
func (p *Postgres) SettledTotals(ctx context.Context, marketID string) (map[int]int64, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT cell_index, sum(amount_minor) FROM stake
		  WHERE market_id = $1 AND cancelled_at IS NULL GROUP BY cell_index`, marketID)
	if err != nil {
		return nil, fmt.Errorf("read settled totals for %s: %w", marketID, err)
	}
	defer rows.Close()

	totals := map[int]int64{}
	for rows.Next() {
		var cell int
		var amount int64
		if err := rows.Scan(&cell, &amount); err != nil {
			return nil, fmt.Errorf("scan settled total: %w", err)
		}
		totals[cell] = amount
	}
	return totals, rows.Err()
}

// SettledMarkets returns markets that reached a terminal state — settled or
// voided — newest first.
func (p *Postgres) SettledMarkets(ctx context.Context, limit int) ([]MarketDetail, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id::text, symbol, state, floor_price, cell_width, bounded_cells, rake_bps,
		        opens_at, amends_at, freezes_at, expires_at,
		        settled_price, settled_cell, outcome, rake_minor
		   FROM market WHERE state IN ('settled', 'voided')
		  ORDER BY expires_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("read settled markets: %w", err)
	}
	defer rows.Close()

	markets := []MarketDetail{}
	for rows.Next() {
		var m MarketDetail
		if err := rows.Scan(&m.ID, &m.Symbol, &m.State, &m.FloorPrice, &m.CellWidth,
			&m.BoundedCells, &m.RakeBps, &m.OpensAt, &m.AmendsAt, &m.FreezesAt, &m.ExpiresAt,
			&m.SettledPrice, &m.SettledCell, &m.Outcome, &m.RakeMinor); err != nil {
			return nil, fmt.Errorf("scan settled market: %w", err)
		}
		markets = append(markets, m)
	}
	return markets, rows.Err()
}

// RecentlySettled returns markets that expired within the window, so a client
// can watch a column resolve in place instead of having it vanish.
func (p *Postgres) RecentlySettled(ctx context.Context, symbol string, within time.Duration) ([]MarketDetail, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id::text, symbol, state, floor_price, cell_width, bounded_cells, rake_bps,
		        opens_at, amends_at, freezes_at, expires_at,
		        settled_price, settled_cell, outcome, rake_minor
		   FROM market
		  WHERE symbol = $1 AND state IN ('settled', 'voided') AND expires_at > now() - $2::interval
		  ORDER BY expires_at`, symbol, within)
	if err != nil {
		return nil, fmt.Errorf("read recently settled: %w", err)
	}
	defer rows.Close()

	markets := []MarketDetail{}
	for rows.Next() {
		var m MarketDetail
		if err := rows.Scan(&m.ID, &m.Symbol, &m.State, &m.FloorPrice, &m.CellWidth,
			&m.BoundedCells, &m.RakeBps, &m.OpensAt, &m.AmendsAt, &m.FreezesAt, &m.ExpiresAt,
			&m.SettledPrice, &m.SettledCell, &m.Outcome, &m.RakeMinor); err != nil {
			return nil, fmt.Errorf("scan recently settled: %w", err)
		}
		markets = append(markets, m)
	}
	return markets, rows.Err()
}
