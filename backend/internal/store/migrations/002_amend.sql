ALTER TABLE stake ADD COLUMN cancelled_at timestamptz;
ALTER TABLE market ADD COLUMN amends_at timestamptz;

CREATE INDEX stake_market_active_idx ON stake (market_id) WHERE cancelled_at IS NULL;
