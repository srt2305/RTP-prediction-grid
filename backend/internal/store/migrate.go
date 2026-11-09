package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

//go:embed all:migrations
var migrations embed.FS

// Each migration commits with the row recording it, so a failure leaves nothing
// half-applied.
func Migrate(ctx context.Context, conn *pgx.Conn, log *slog.Logger) error {
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migration (
		name       text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	entries, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}

	for _, path := range entries {
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migration WHERE name = $1)`, path,
		).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", path, err)
		}
		if applied {
			continue
		}

		body, err := migrations.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", path, err)
		}
		if err := applyMigration(ctx, conn, path, string(body)); err != nil {
			return err
		}
		log.Info("migration applied", "name", path)
	}
	return nil
}

func applyMigration(ctx context.Context, conn *pgx.Conn, name, body string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, body); err != nil {
		return fmt.Errorf("apply %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migration (name) VALUES ($1)`, name); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return tx.Commit(ctx)
}
