//go:build integration

package app_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
	"github.com/fredzolio/backend-go-jungle/internal/testsupport/pgtest"
)

var (
	env       *pgtest.Env
	silentLog = slog.New(slog.NewTextHandler(io.Discard, nil))
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	if env, err = pgtest.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if err := env.Stop(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

// fixture is one isolated database with the use cases wired to it.
type fixture struct {
	clock    app.Clock
	wallets  *app.Wallets
	wagering *app.Wagering
	db       pgtest.DB
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db := env.NewDatabase(t)
	deps := app.Deps{UoW: postgres.NewUnitOfWork(db.App), Clock: app.SystemClock{}, IDs: app.UUIDv7{}}
	return fixture{db: db, clock: deps.Clock, wallets: app.NewWallets(deps), wagering: app.NewWagering(deps, app.DefaultReferencePolicy)}
}

func amount(t *testing.T, value, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(value, currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f fixture) open(t *testing.T, balance string) wallet.Snapshot {
	t.Helper()
	w, err := f.wallets.Open(context.Background(), app.OpenWalletCommand{PlayerID: uuid.New(), InitialBalance: amount(t, balance, "BRL")})
	if err != nil {
		t.Fatalf("open wallet: %v", err)
	}
	return w
}

// op describes an operation; zero values get sensible defaults.
type op struct {
	kind, amount, currency, extID, key, ref, provider, round string
	playerID                                                 uuid.UUID
}

func request(t *testing.T, w wallet.Snapshot, o op) wagering.ExternalRequest {
	t.Helper()
	def := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	provider := def(o.provider, "provider-a")
	player := w.PlayerID
	if o.playerID != uuid.Nil {
		player = o.playerID
	}
	req, err := wagering.NewExternalRequest(wagering.RawRequest{
		ProviderID: provider, ExternalTransactionID: o.extID, IdempotencyKey: def(o.key, provider+":"+o.extID),
		PlayerID: player.String(), WalletID: w.ID.String(), RoundID: def(o.round, "round-1"), GameID: "fortune-chimp",
		Kind: def(o.kind, "BET"), Money: amount(t, def(o.amount, "25.00"), def(o.currency, "BRL")),
		ReferenceExternalTransactionID: o.ref,
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return req
}

func (f fixture) submit(t *testing.T, w wallet.Snapshot, o op) (app.SubmitResult, error) {
	t.Helper()
	req := request(t, w, o)
	return f.wagering.Submit(context.Background(), app.SubmitCommand{Request: req, AuthenticatedProvider: req.Fields().ProviderID})
}

func (f fixture) mustSubmit(t *testing.T, w wallet.Snapshot, o op) app.SubmitResult {
	t.Helper()
	r, err := f.submit(t, w, o)
	if err != nil {
		t.Fatalf("submit %+v: %v", o, err)
	}
	return r
}

func queryInt(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f fixture) balance(t *testing.T, walletID uuid.UUID) int64 {
	return queryInt(t, f.db.Owner, `SELECT balance_minor FROM wallets WHERE id = $1`, walletID)
}

func (f fixture) ledgerCount(t *testing.T, walletID uuid.UUID) int64 {
	return queryInt(t, f.db.Owner, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID)
}

func (f fixture) outboxTypes(t *testing.T, walletID uuid.UUID) []string {
	t.Helper()
	rows, err := f.db.Owner.Query(context.Background(),
		`SELECT event_type FROM outbox_events WHERE partition_key = $1 ORDER BY seq`, walletID.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}
