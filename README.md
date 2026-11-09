# RTP Prediction Grid

A digital option pricing and pari-mutuel settlement engine over live asset
prices. A grid discretises a price/time chart into cells; each cell is a binary
option paying out if the underlying settles inside it at window close.
Positions are settled from the realised pool: the house takes a fixed rake and
distributes the remainder proportionally among correct positions.

Pricing and settlement are independent by design. The pricing model estimates
fair probabilities and publishes indicative multipliers; settlement divides the
realised pool using only the stakes taken and the oracle's official price. The
house therefore carries no directional exposure — a badly wrong volatility
estimate produces mispriced *indications*, never a house loss.

See [PLAN.md](PLAN.md) for the architecture, the low-level design, and the
decisions behind both.

## Running it

All commands run from the repository root.

```sh
make up      # TimescaleDB + Redis
make serve   # engine on :8080, REST + WebSocket
make web     # Next.js client on :3000, in a second terminal
```

Then open http://localhost:3000. Add `-live` to `serve` to ingest real Binance
trades instead of the simulator.

Other entry points:

```sh
make grid    # cell probabilities and fair multipliers, no I/O
make demo    # one market lifecycle end to end, with the phase checks
make series  # several expiries open at once, drawn in the terminal
make audit   # assert deposited == spendable + escrowed + rake
make test    # unit tests, race detector
make lint    # gofmt, go vet, golangci-lint
```

`grid` is pure computation and needs nothing running:

```
$ go run . grid -spot 65000 -sigma 0.6 -tau 1m -width 50 -cells 12
  cell                range     probability    multiplier
     5  64900.00 - 64950.00        0.144892         6.73x
     6  64950.00 - 65000.00        0.323826         3.01x
     7  65000.00 - 65050.00        0.323525         3.01x
     8  65050.00 - 65100.00        0.144719         6.74x
                      total  1.000000000000
```

`demo` runs the whole lifecycle — open, stake, freeze, expire, settle, pay out
— with real persistence, and prints the resulting book:

```
settled at 65027.94 in cell 11, outcome "paid"
pool $2200.00, rake $55.00 at 250 bps, paid out $2145.00

  account  cell   staked      paid      net
    alice    11  $250.00   $536.25  +286.25
      bob    11  $750.00  $1608.75  +858.75
    carol    12  $400.00     $0.00  -400.00
    house                   $55.00   +55.00
```

Requires Go 1.25+, Docker Compose, and `golangci-lint` v2 for the lint target
(`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6`).
Connection settings come from `RTP_POSTGRES_DSN` and `RTP_REDIS_ADDR`.

## Status

The engine is complete end to end: lognormal pricing, pari-mutuel settlement,
exchange feed and oracle, concurrent market series, an account ledger with
escrow, and an HTTP surface with a WebSocket grid stream. A Next.js client
renders the live grid and stakes back through the API.

There is no authentication. A browser wallet is the intended answer, since a
signed message supplies the account key and the funding rail together.

Next is the quant-analysis layer: calibration backtest, Monte Carlo bankroll
comparison, and a volatility-surface check.

## Disclaimer

Reference implementation for educational and portfolio purposes. It has not
been reviewed for real-money deployment; any legal or regulatory responsibility
for such use rests with the deployer.
