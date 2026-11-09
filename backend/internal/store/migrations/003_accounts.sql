CREATE TABLE account (
    id         uuid PRIMARY KEY,
    handle     text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Balance is derived by summing the ledger rather than stored, so a balance can
-- never disagree with the entries that produced it.
CREATE TABLE ledger (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id   uuid NOT NULL REFERENCES account (id),
    kind         text NOT NULL,
    amount_minor bigint NOT NULL,
    market_id    uuid REFERENCES market (id),
    stake_id     uuid REFERENCES stake (id),
    at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ledger_account_idx ON ledger (account_id, at DESC);

INSERT INTO account (id, handle)
SELECT gen_random_uuid(), handle FROM (SELECT DISTINCT account AS handle FROM stake) existing;

ALTER TABLE stake ADD COLUMN account_id uuid REFERENCES account (id);
UPDATE stake s SET account_id = a.id FROM account a WHERE a.handle = s.account;
ALTER TABLE stake ALTER COLUMN account_id SET NOT NULL;
ALTER TABLE stake DROP COLUMN account;

CREATE INDEX stake_account_idx ON stake (account_id);
