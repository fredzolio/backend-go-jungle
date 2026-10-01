//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
	"github.com/fredzolio/backend-go-jungle/internal/testsupport/pgtest"
)

var env *pgtest.Env

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

var t0 = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type seeded struct {
	WalletID, PlayerID, OpeningID uuid.UUID
}

// seedWallet opens a wallet exactly like the use case will: wallet, PROCESSED
// OPENING and its ledger entry in one commit.
func seedWallet(t *testing.T, uow app.UnitOfWork, balance string) seeded {
	t.Helper()
	s := seeded{WalletID: uuid.New(), PlayerID: uuid.New(), OpeningID: uuid.New()}
	err := uow.Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
		w, entry, err := wallet.Open(wallet.Opening{ID: s.WalletID, PlayerID: s.PlayerID, InitialBalance: brl(t, balance),
			OpeningTransaction: s.OpeningID, OpeningEntry: uuid.New(), At: t0})
		if err != nil {
			return err
		}
		if err := tx.Wallets().Insert(ctx, w); err != nil {
			return err
		}
		if entry == nil {
			return nil
		}
		opening, err := wagering.NewOpening(s.OpeningID, s.WalletID, s.PlayerID, brl(t, balance), t0)
		if err != nil {
			return err
		}
		if err := opening.Process(wagering.Result{Balance: w.Balance(), WalletVersion: w.Version()}, uuid.Nil, t0); err != nil {
			return err
		}
		if err := tx.Transactions().InsertOpening(ctx, opening); err != nil {
			return err
		}
		return tx.Ledger().Insert(ctx, *entry)
	})
	if err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	return s
}

// externalTx builds a PENDING external transaction for the seeded wallet.
func externalTx(t *testing.T, s seeded, kind, amount, extID string) *wagering.Transaction {
	t.Helper()
	raw := wagering.RawRequest{
		ProviderID: "provider-a", ExternalTransactionID: extID, IdempotencyKey: "provider-a:" + extID,
		PlayerID: s.PlayerID.String(), WalletID: s.WalletID.String(), RoundID: "round-1", GameID: "game-1",
		Kind: kind, Money: brl(t, amount),
	}
	if kind == "REFUND" || kind == "ROLLBACK" {
		raw.ReferenceExternalTransactionID = "bet-ref"
	}
	req, err := wagering.NewExternalRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := wagering.NewExternal(uuid.New(), req, t0)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func newUoW(db pgtest.DB) *postgres.UnitOfWork { return postgres.NewUnitOfWork(db.App) }
