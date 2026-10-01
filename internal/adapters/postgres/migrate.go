package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/fredzolio/backend-go-jungle/migrations"
)

// MigrateCommands lists the supported `jungle migrate` sub-commands.
const MigrateCommands = "up | down | reset | status"

// Migrate applies or reverts the embedded migrations through dsn (owner role).
//
//	up      apply every pending migration
//	down    revert the latest migration
//	reset   revert every migration (version 0)
//	status  print applied / pending migrations
func Migrate(ctx context.Context, dsn, command string, out io.Writer) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse dsn: %w", err)
	}
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	return migrate(ctx, db, command, out)
}

func migrate(ctx context.Context, db *sql.DB, command string, out io.Writer) error {
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("migrations provider: %w", err)
	}
	switch command {
	case "up":
		results, upErr := provider.Up(ctx)
		for _, r := range results {
			fmt.Fprintf(out, "applied %s (%s)\n", r.Source.Path, r.Duration)
		}
		return upErr
	case "down":
		r, downErr := provider.Down(ctx)
		if r != nil {
			fmt.Fprintf(out, "reverted %s\n", r.Source.Path)
		}
		return downErr
	case "reset":
		results, resetErr := provider.DownTo(ctx, 0)
		for _, r := range results {
			fmt.Fprintf(out, "reverted %s\n", r.Source.Path)
		}
		return resetErr
	case "status":
		statuses, statusErr := provider.Status(ctx)
		for _, s := range statuses {
			fmt.Fprintf(out, "%-8s %s\n", s.State, s.Source.Path)
		}
		return statusErr
	default:
		return fmt.Errorf("unknown migrate command %q (expected: %s)", command, MigrateCommands)
	}
}
