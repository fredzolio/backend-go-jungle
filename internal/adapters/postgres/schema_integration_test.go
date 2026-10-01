//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/migrations"
)

func sqlState(t *testing.T, err error) string {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a PostgreSQL error, got %v", err)
	}
	return pgErr.Code
}

func execErr(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql, args...)
	return sqlState(t, err)
}

func TestMigrations_apply_revert_and_reapply(t *testing.T) {
	dsn := env.DSN("jungle_owner", env.EmptyDatabase(t))
	ctx := context.Background()
	for _, cmd := range []string{"up", "reset", "up"} {
		if err := postgres.Migrate(ctx, dsn, cmd, &bytes.Buffer{}); err != nil {
			t.Fatalf("migrate %s: %v", cmd, err)
		}
	}
	var status bytes.Buffer
	if err := postgres.Migrate(ctx, dsn, "status", &status); err != nil {
		t.Fatal(err)
	}
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(status.String(), "applied") != len(files) || strings.Contains(status.String(), "pending") {
		t.Fatalf("status after reapply:\n%s", status.String())
	}
}

func TestWallet_invariants_are_enforced_by_the_database(t *testing.T) {
	db := env.NewDatabase(t)
	s := seedWallet(t, newUoW(db), "100.00")
	cases := map[string]struct {
		sql  string
		code string
	}{
		"negative balance":          {`UPDATE wallets SET balance_minor = -1, version = version + 1 WHERE id = $1`, "23514"},
		"balance without version":   {`UPDATE wallets SET balance_minor = 1 WHERE id = $1`, "23001"},
		"version without balance":   {`UPDATE wallets SET version = version + 1 WHERE id = $1`, "23001"},
		"identity change":           {`UPDATE wallets SET player_id = gen_random_uuid() WHERE id = $1`, "23001"},
		"delete":                    {`DELETE FROM wallets WHERE id = $1`, "23001"},
		"duplicate player+currency": {`INSERT INTO wallets SELECT gen_random_uuid(), player_id, currency, 0, 1, now(), now() FROM wallets WHERE id = $1`, "23505"},
	}
	for name, c := range cases {
		if got := execErr(t, db.Owner, c.sql, s.WalletID); got != c.code {
			t.Fatalf("%s: SQLSTATE %s, want %s", name, got, c.code)
		}
	}
}

func TestLedger_is_append_only_for_owner_and_app(t *testing.T) {
	db := env.NewDatabase(t)
	s := seedWallet(t, newUoW(db), "100.00")
	for _, sql := range []string{
		`UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE wallet_id = $1`,
		`DELETE FROM wallet_ledger_entries WHERE wallet_id = $1`,
	} {
		if got := execErr(t, db.Owner, sql, s.WalletID); got != "23001" {
			t.Fatalf("owner %q: SQLSTATE %s, want trigger rejection 23001", sql, got)
		}
		if got := execErr(t, db.App, sql, s.WalletID); got != "42501" {
			t.Fatalf("app %q: SQLSTATE %s, want insufficient_privilege 42501", sql, got)
		}
	}
	if got := execErr(t, db.Owner, `TRUNCATE wallet_ledger_entries CASCADE`); got != "23001" {
		t.Fatalf("truncate: SQLSTATE %s", got)
	}
}

func TestLedger_entry_must_match_its_transaction(t *testing.T) {
	db := env.NewDatabase(t)
	s := seedWallet(t, newUoW(db), "100.00")
	bet := externalTx(t, s, "BET", "10.00", "bet-fk")
	if err := newUoW(db).Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
		_, err := tx.Transactions().InsertExternal(ctx, bet)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, currency, amount_minor,
		balance_before_minor, balance_after_minor, wallet_version, created_at) VALUES ($1, $2, $3, $4, 'BRL', $5, $6, $7, $8, now())`
	cases := map[string]struct {
		args []any
		code string
	}{
		// the BET is 10.00: an entry debiting 50.00 for it violates the composite FK
		"amount differs from transaction": {[]any{uuid.New(), s.WalletID, bet.ID(), "DEBIT", 5000, 10000, 5000, 2}, "23503"},
		"arithmetic mismatch":             {[]any{uuid.New(), s.WalletID, bet.ID(), "DEBIT", 1000, 10000, 9001, 2}, "23514"},
		"second entry for a transaction":  {[]any{uuid.New(), s.WalletID, s.OpeningID, "CREDIT", 10000, 10000, 20000, 2}, "23505"},
	}
	for name, c := range cases {
		if got := execErr(t, db.Owner, insert, c.args...); got != c.code {
			t.Fatalf("%s: SQLSTATE %s, want %s", name, got, c.code)
		}
	}
}

func TestLedger_chain_breaks_are_rejected(t *testing.T) {
	db := env.NewDatabase(t)
	s := seedWallet(t, newUoW(db), "100.00")
	ctx := context.Background()
	// A PROCESSED-able bet of 10.00 to reference from the entry.
	bet := externalTx(t, s, "BET", "10.00", "bet-chain")
	if err := newUoW(db).Do(ctx, func(ctx context.Context, tx app.Tx) error {
		_, err := tx.Transactions().InsertExternal(ctx, bet)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := db.Owner.Exec(ctx, `INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, currency, amount_minor,
		balance_before_minor, balance_after_minor, wallet_version, created_at)
		VALUES ($1, $2, $3, 'DEBIT', 'BRL', 1000, 5000, 4000, 2, now())`, uuid.New(), s.WalletID, bet.ID())
	if got := sqlState(t, err); got != "23000" {
		t.Fatalf("before != previous after: SQLSTATE %s, want 23000", got)
	}
}

func TestWallet_must_match_its_ledger_at_commit(t *testing.T) {
	db := env.NewDatabase(t)
	s := seedWallet(t, newUoW(db), "100.00")
	err := pgx.BeginFunc(context.Background(), db.Owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE wallets SET balance_minor = 999999, version = version + 1 WHERE id = $1`, s.WalletID)
		return err
	})
	if got := sqlState(t, err); got != "23000" {
		t.Fatalf("balance change without ledger entry committed? SQLSTATE %s", got)
	}
}

func TestTransactions_invariants_are_enforced_by_the_database(t *testing.T) {
	db := env.NewDatabase(t)
	s := seedWallet(t, newUoW(db), "100.00")
	cases := map[string]struct {
		sql  string
		code string
	}{
		"terminal row update":    {`UPDATE wager_transactions SET updated_at = now() WHERE id = $1`, "23001"},
		"delete":                 {`DELETE FROM wager_transactions WHERE id = $1`, "23001"},
		"second opening":         {`INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, currency, amount_minor, created_at, updated_at) SELECT gen_random_uuid(), 'INTERNAL', 'OPENING', 'PENDING', wallet_id, player_id, currency, 1, now(), now() FROM wager_transactions WHERE id = $1`, "23505"},
		"internal with provider": {`INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, currency, amount_minor, provider_id, created_at, updated_at) SELECT gen_random_uuid(), 'INTERNAL', 'OPENING', 'PENDING', wallet_id, player_id, currency, 1, 'p', now(), now() FROM wager_transactions WHERE id = $1`, "23514"},
	}
	for name, c := range cases {
		if got := execErr(t, db.Owner, c.sql, s.OpeningID); got != c.code {
			t.Fatalf("%s: SQLSTATE %s, want %s", name, got, c.code)
		}
	}
}

// REFUND and ROLLBACK of the same BET may never both succeed (double return).
func TestTransactions_a_reference_is_reversed_successfully_at_most_once(t *testing.T) {
	db := env.NewDatabase(t)
	s := seedWallet(t, newUoW(db), "100.00")
	ctx := context.Background()
	reversal := `INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, currency, amount_minor,
		provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
		reference_external_transaction_id, reference_transaction_id, result_balance_minor, result_wallet_version,
		created_at, updated_at, processed_at)
		VALUES (gen_random_uuid(), 'EXTERNAL', $1, 'PROCESSED', $2, $3, 'BRL', 100, 'provider-a', $4, $4,
		repeat('a', 64), 'r', 'g', 'bet-ref', $5, 0, 2, now(), now(), now())`
	if _, err := db.Owner.Exec(ctx, reversal, "REFUND", s.WalletID, s.PlayerID, "refund-1", s.OpeningID); err != nil {
		t.Fatal(err)
	}
	_, err := db.Owner.Exec(ctx, reversal, "ROLLBACK", s.WalletID, s.PlayerID, "rollback-1", s.OpeningID)
	if got := sqlState(t, err); got != "23505" {
		t.Fatalf("second successful reversal: SQLSTATE %s, want 23505", got)
	}
}
