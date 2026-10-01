//go:build integration

package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

func TestOpenWallet_with_balance_commits_opening_ledger_and_events(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "1000.00")
	if w.Version != 1 || f.balance(t, w.ID) != 100000 || f.ledgerCount(t, w.ID) != 1 {
		t.Fatalf("wallet=%+v ledger=%d", w, f.ledgerCount(t, w.ID))
	}
	if got := f.outboxTypes(t, w.ID); !slices.Equal(got, []string{"WagerTransactionProcessed", "WalletBalanceChanged"}) {
		t.Fatalf("outbox = %v", got)
	}
	if n := queryInt(t, f.db.Owner, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING' AND status = 'PROCESSED'`, w.ID); n != 1 {
		t.Fatalf("openings = %d", n)
	}
}

func TestOpenWallet_with_zero_balance_creates_no_opening_nor_events(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "0.00")
	if f.ledgerCount(t, w.ID) != 0 || len(f.outboxTypes(t, w.ID)) != 0 ||
		queryInt(t, f.db.Owner, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1`, w.ID) != 0 {
		t.Fatal("zero opening produced financial records")
	}
}

func TestOpenWallet_twice_for_same_player_and_currency_conflicts(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "10.00")
	_, err := f.wallets.Open(context.Background(), app.OpenWalletCommand{PlayerID: w.PlayerID, InitialBalance: amount(t, "5.00", "BRL")})
	if !errors.Is(err, app.ErrWalletExists) {
		t.Fatalf("err = %v", err)
	}
	if _, err := f.wallets.Open(context.Background(), app.OpenWalletCommand{PlayerID: w.PlayerID, InitialBalance: amount(t, "5.00", "USD")}); err != nil {
		t.Fatalf("another currency must be allowed: %v", err)
	}
}

func TestBet_debits_and_emits_processed_and_balance_changed(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "1000.00")
	r := f.mustSubmit(t, w, op{extID: "transaction-123"})
	if r.Replay || r.Transaction.Status != wagering.StatusProcessed || r.Transaction.Result.Balance.Amount() != "975.00" {
		t.Fatalf("result = %+v", r)
	}
	if f.balance(t, w.ID) != 97500 || f.ledgerCount(t, w.ID) != 2 {
		t.Fatal("balance or ledger not updated")
	}
	want := []string{"WagerTransactionProcessed", "WalletBalanceChanged", "WagerTransactionProcessed", "WalletBalanceChanged"}
	if got := f.outboxTypes(t, w.ID); !slices.Equal(got, want) {
		t.Fatalf("outbox = %v", got)
	}
}

func TestWin_credits_and_Loss_concludes_without_movement(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "100.00")
	f.mustSubmit(t, w, op{kind: "WIN", amount: "50.00", extID: "win-1"})
	loss := f.mustSubmit(t, w, op{kind: "LOSS", amount: "0.00", extID: "loss-1"})
	if f.balance(t, w.ID) != 15000 || f.ledgerCount(t, w.ID) != 2 {
		t.Fatalf("balance=%d ledger=%d", f.balance(t, w.ID), f.ledgerCount(t, w.ID))
	}
	if loss.Transaction.Status != wagering.StatusProcessed || loss.Transaction.Result.WalletVersion != 2 {
		t.Fatalf("LOSS must not change the wallet version: %+v", loss.Transaction.Result)
	}
	types := f.outboxTypes(t, w.ID)
	if types[len(types)-1] != "WagerTransactionProcessed" || len(types) != 5 {
		t.Fatalf("LOSS must emit only WagerTransactionProcessed: %v", types)
	}
}

func TestReplay_returns_the_balance_observed_originally(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "1000.00")
	first := f.mustSubmit(t, w, op{extID: "tx-1"})
	f.mustSubmit(t, w, op{extID: "tx-2", amount: "100.00"})
	replay := f.mustSubmit(t, w, op{extID: "tx-1"})
	if !replay.Replay || replay.Transaction.ID != first.Transaction.ID || replay.Transaction.Result.Balance.Amount() != "975.00" {
		t.Fatalf("replay = %+v", replay)
	}
	if f.balance(t, w.ID) != 87500 || f.ledgerCount(t, w.ID) != 3 {
		t.Fatal("replay moved money")
	}
}

func TestIdempotency_conflicts(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "1000.00")
	f.mustSubmit(t, w, op{extID: "tx-1"})
	if _, err := f.submit(t, w, op{extID: "tx-1", amount: "26.00"}); !errors.Is(err, app.ErrIdempotencyConflict) {
		t.Fatalf("same key, different payload: %v", err)
	}
	if _, err := f.submit(t, w, op{extID: "tx-1", key: "another-key"}); !errors.Is(err, app.ErrDuplicateExternalTransaction) {
		t.Fatalf("same operation, another key: %v", err)
	}
	if f.ledgerCount(t, w.ID) != 2 {
		t.Fatal("conflicts moved money")
	}
}

func TestBusiness_rejections_are_persisted_and_auditable(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "10.00")
	cases := []struct {
		o    op
		code wagering.FailureCode
	}{
		{op{extID: "bet-big", amount: "10.01"}, wagering.FailureInsufficientFunds},
		{op{extID: "bet-usd", currency: "USD"}, wagering.FailureCurrencyMismatch},
		{op{extID: "bet-other-player", amount: "1.00", playerID: uuid.New()}, wagering.FailureWalletPlayerMismatch},
	}
	for _, c := range cases {
		r := f.mustSubmit(t, w, c.o)
		if r.Transaction.Status != wagering.StatusRejected || r.Transaction.FailureCode != c.code {
			t.Fatalf("%s: %+v", c.o.extID, r.Transaction)
		}
	}
	if f.balance(t, w.ID) != 1000 || f.ledgerCount(t, w.ID) != 1 {
		t.Fatal("rejection moved money")
	}
	if n := queryInt(t, f.db.Owner, `SELECT count(*) FROM outbox_events WHERE partition_key = $1 AND event_type = 'WagerTransactionRejected'`, w.ID.String()); n != 3 {
		t.Fatalf("rejection events = %d", n)
	}
}

func TestSubmit_correctable_errors_persist_nothing(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "10.00")
	ghost := w
	ghost.ID = uuid.New()
	if _, err := f.submit(t, ghost, op{extID: "tx-ghost"}); !errors.Is(err, app.ErrWalletNotFound) {
		t.Fatalf("unknown wallet: %v", err)
	}
	req := request(t, w, op{extID: "tx-spoof"})
	_, err := f.wagering.Submit(context.Background(), app.SubmitCommand{Request: req, AuthenticatedProvider: "provider-b"})
	if !errors.Is(err, app.ErrProviderMismatch) {
		t.Fatalf("provider mismatch: %v", err)
	}
	if n := queryInt(t, f.db.Owner, `SELECT count(*) FROM wager_transactions WHERE origin = 'EXTERNAL'`); n != 0 {
		t.Fatalf("persisted %d external transactions", n)
	}
}
