package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/srt2305/RTP-prediction-grid/backend/internal/store"
)

func Run(ctx context.Context, log *slog.Logger, args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}

	switch args[0] {
	case "grid":
		return runGrid(args[1:])
	case "demo":
		return runDemo(ctx, log, args[1:])
	case "serve":
		return runServe(ctx, log, args[1:])
	case "audit":
		return runAudit(ctx, log, args[1:])
	case "series":
		return runSeries(ctx, log, args[1:])
	case "live":
		return runLive(ctx, log, args[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: engine <command> [flags]

  grid   print cell probabilities and fair multipliers for a set of parameters
  demo   run one market lifecycle end to end against the simulated feed
  serve  run the engine and expose it over HTTP and WebSocket
  audit  check that every cent deposited is spendable, escrowed, or rake
  series ingest and run several markets at once — the price/time grid
  live   ingest exchange trades and roll markets against them

`)
}

func connect(ctx context.Context, cfg config, log *slog.Logger) (*store.Postgres, *store.LiveTotals, error) {
	pg, err := store.OpenPostgres(ctx, cfg.postgresDSN, log)
	if err != nil {
		return nil, nil, err
	}

	live, err := store.OpenRedis(ctx, cfg.redisAddr)
	if err != nil {
		pg.Close()
		return nil, nil, err
	}
	return pg, live, nil
}
