//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

func TestOpenWallet_round_trips_wallet_opening_and_ledger(t *testing.T) {
	db := env.NewDatabase(t)
	uow := newUoW(db)
	s := seedWallet(t, uow, "1000.00")
	err := uow.Snapshot(context.Background(), func(ctx context.Context, tx app.Tx) error {
		w, err := tx.Wallets().Get(ctx, s.WalletID)
		if err != nil {
			return err
		}
		opening, err := tx.Transactions().Get(ctx, s.OpeningID)
		if err != nil {
			return err
		}
		entries, err := tx.Ledger().List(ctx, s.WalletID, 0, 10)
		if err != nil {
			return err
		}
		if w.Balance().Amount() != "1000.00" || w.Version() != 1 || opening.Status() != wagering.StatusProcessed ||
			opening.Result().Balance.Amount() != "1000.00" || len(entries) != 1 || entries[0].Data().WalletVersion != 1 {
			t.Fatalf("wallet=%+v opening=%+v entries=%d", w.Snapshot(), opening.Snapshot(), len(entries))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInsertWallet_duplicate_player_currency_is_ErrWalletExists(t *testing.T) {
	db := env.NewDatabase(t)
	uow := newUoW(db)
	s := seedWallet(t, uow, "0.00")
	err := uow.Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
		w, _, err := wallet.Open(wallet.Opening{ID: uuid.New(), PlayerID: s.PlayerID, InitialBalance: brl(t, "0.00"), At: t0})
		if err != nil {
			return err
		}
		return tx.Wallets().Insert(ctx, w)
	})
	if !errors.Is(err, app.ErrWalletExists) {
		t.Fatalf("err = %v", err)
	}
}

func TestInsertExternal_is_idempotent_and_scoped_by_provider(t *testing.T) {
	db := env.NewDatabase(t)
	uow := newUoW(db)
	s := seedWallet(t, uow, "100.00")
	first := externalTx(t, s, "BET", "10.00", "tx-1")
	var inserted []bool
	for _, candidate := range []*wagering.Transaction{first, externalTx(t, s, "BET", "10.00", "tx-1")} {
		err := uow.Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
			ok, err := tx.Transactions().InsertExternal(ctx, candidate)
			inserted = append(inserted, ok)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !inserted[0] || inserted[1] {
		t.Fatalf("inserted = %v, want [true false]", inserted)
	}
	err := uow.Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
		got, err := tx.Transactions().FindByIdempotencyKey(ctx, "provider-a", "provider-a:tx-1")
		if err != nil {
			return err
		}
		if got.ID() != first.ID() {
			t.Fatalf("found %s, want original %s", got.ID(), first.ID())
		}
		if _, err := tx.Transactions().FindByIdempotencyKey(ctx, "provider-b", "provider-a:tx-1"); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("other provider sees the key: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInsertExternal_for_unknown_wallet_is_ErrNotFound(t *testing.T) {
	db := env.NewDatabase(t)
	tx := externalTx(t, seeded{WalletID: uuid.New(), PlayerID: uuid.New()}, "BET", "1.00", "tx-x")
	err := newUoW(db).Do(context.Background(), func(ctx context.Context, store app.Tx) error {
		_, err := store.Transactions().InsertExternal(ctx, tx)
		return err
	})
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestWalletUpdate_with_stale_version_is_ErrConcurrentUpdate(t *testing.T) {
	db := env.NewDatabase(t)
	uow := newUoW(db)
	s := seedWallet(t, uow, "0.00")
	err := uow.Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
		w, err := tx.Wallets().GetForUpdate(ctx, s.WalletID)
		if err != nil {
			return err
		}
		if _, err := w.Credit(wallet.Movement{EntryID: uuid.New(), TransactionID: uuid.New(), Amount: brl(t, "1.00"), At: t0}); err != nil {
			return err
		}
		return tx.Wallets().Update(ctx, w, 7)
	})
	if !errors.Is(err, app.ErrConcurrentUpdate) {
		t.Fatalf("err = %v", err)
	}
}

// A writer blocked on another writer's wallet lock fails fast as transient.
func TestWalletLock_contention_beyond_lock_timeout_is_ErrTransient(t *testing.T) {
	db := env.NewDatabase(t)
	uow := newUoW(db)
	s := seedWallet(t, uow, "0.00")
	held, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = uow.Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
			if _, err := tx.Wallets().GetForUpdate(ctx, s.WalletID); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer close(release)
	start := time.Now()
	err := uow.Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
		_, err := tx.Wallets().GetForUpdate(ctx, s.WalletID)
		return err
	})
	if !errors.Is(err, app.ErrTransient) || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}

func TestLedgerList_paginates_by_wallet_version(t *testing.T) {
	db := env.NewDatabase(t)
	uow := newUoW(db)
	s := seedWallet(t, uow, "100.00")
	for i, amount := range []string{"10.00", "20.00"} {
		bet := externalTx(t, s, "BET", amount, "bet-"+amount)
		err := uow.Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
			if _, err := tx.Transactions().InsertExternal(ctx, bet); err != nil {
				return err
			}
			w, err := tx.Wallets().GetForUpdate(ctx, s.WalletID)
			if err != nil {
				return err
			}
			before := w.Version()
			entry, err := w.Debit(wallet.Movement{EntryID: uuid.New(), TransactionID: bet.ID(), Amount: bet.Money(), At: t0.Add(time.Duration(i+1) * time.Second)})
			if err != nil {
				return err
			}
			if err := tx.Wallets().Update(ctx, w, before); err != nil {
				return err
			}
			if err := tx.Ledger().Insert(ctx, entry); err != nil {
				return err
			}
			if err := bet.Process(wagering.Result{Balance: w.Balance(), WalletVersion: w.Version()}, uuid.Nil, t0); err != nil {
				return err
			}
			return tx.Transactions().Update(ctx, bet)
		})
		if err != nil {
			t.Fatalf("debit %s: %v", amount, err)
		}
	}
	err := uow.Snapshot(context.Background(), func(ctx context.Context, tx app.Tx) error {
		page1, err := tx.Ledger().List(ctx, s.WalletID, 0, 2)
		if err != nil {
			return err
		}
		page2, err := tx.Ledger().List(ctx, s.WalletID, page1[len(page1)-1].Data().WalletVersion, 2)
		if err != nil {
			return err
		}
		if len(page1) != 2 || len(page2) != 1 || page2[0].Data().BalanceAfter.Amount() != "70.00" {
			t.Fatalf("page1=%d page2=%d", len(page1), len(page2))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
