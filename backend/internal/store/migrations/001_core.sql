CREATE TABLE tick (
    symbol text NOT NULL,
    ts     timestamptz NOT NULL,
    price  double precision NOT NULL
);

SELECT create_hypertable('tick', 'ts');
CREATE INDEX tick_symbol_ts_idx ON tick (symbol, ts DESC);

CREATE TABLE market (
    id            uuid PRIMARY KEY,
    symbol        text NOT NULL,
    state         text NOT NULL,
    floor_price   double precision NOT NULL,
    cell_width    double precision NOT NULL,
    bounded_cells integer NOT NULL,
    rake_bps      integer NOT NULL,
    opens_at      timestamptz NOT NULL,
    freezes_at    timestamptz NOT NULL,
    expires_at    timestamptz NOT NULL,
    settled_price double precision,
    settled_cell  integer,
    outcome       text,
    rake_minor    bigint
);

CREATE TABLE stake (
    id           uuid PRIMARY KEY,
    market_id    uuid NOT NULL REFERENCES market (id),
    account      text NOT NULL,
    cell_index   integer NOT NULL,
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    placed_at    timestamptz NOT NULL
);

CREATE INDEX stake_market_idx ON stake (market_id);

CREATE TABLE payout (
    stake_id     uuid PRIMARY KEY REFERENCES stake (id),
    market_id    uuid NOT NULL REFERENCES market (id),
    amount_minor bigint NOT NULL CHECK (amount_minor >= 0)
);

CREATE INDEX payout_market_idx ON payout (market_id);
