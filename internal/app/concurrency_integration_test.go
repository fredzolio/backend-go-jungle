//go:build integration

package app_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

// fanOut runs n submissions released at the same instant.
func fanOut(t *testing.T, n int, submit func(i int) (app.SubmitResult, error)) ([]app.SubmitResult, []error) {
	t.Helper()
	results, errs := make([]app.SubmitResult, n), make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			results[i], errs[i] = submit(i)
		})
	}
	close(start)
	wg.Wait()
	return results, errs
}

// Mandatory scenario 1: the same bet sent 50 times in parallel debits once.
func TestSameBet_50_times_in_parallel_debits_exactly_once(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "1000.00")
	results, errs := fanOut(t, 50, func(int) (app.SubmitResult, error) { return f.submit(t, w, op{extID: "same-bet"}) })
	applied := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("submission %d: %v", i, errs[i])
		}
		if !r.Replay {
			applied++
		}
		if r.Transaction.Status != wagering.StatusProcessed || r.Transaction.Result.Balance.Amount() != "975.00" {
			t.Fatalf("submission %d saw %+v", i, r.Transaction.Result)
		}
	}
	if applied != 1 || f.ledgerCount(t, w.ID) != 2 || f.balance(t, w.ID) != 97500 {
		t.Fatalf("applied=%d ledger=%d balance=%d", applied, f.ledgerCount(t, w.ID), f.balance(t, w.ID))
	}
}

// Mandatory scenario 2: two distinct 80.00 bets on 100.00 — one wins the race.
func TestTwo_bets_of_80_on_100_one_processed_one_rejected(t *testing.T) {
	f := newFixture(t)
	for round := range 10 { // repeat to exercise different interleavings
		w := f.open(t, "100.00")
		results, errs := fanOut(t, 2, func(i int) (app.SubmitResult, error) {
			return f.submit(t, w, op{extID: fmt.Sprintf("bet-%d-%d", round, i), amount: "80.00"})
		})
		statuses := map[wagering.Status]int{}
		for i, r := range results {
			if errs[i] != nil {
				t.Fatalf("round %d: %v", round, errs[i])
			}
			statuses[r.Transaction.Status]++
			if r.Transaction.Status == wagering.StatusRejected && r.Transaction.FailureCode != wagering.FailureInsufficientFunds {
				t.Fatalf("round %d: code %s", round, r.Transaction.FailureCode)
			}
		}
		debits := queryInt(t, f.db.Owner, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, w.ID)
		if statuses[wagering.StatusProcessed] != 1 || statuses[wagering.StatusRejected] != 1 || f.balance(t, w.ID) != 2000 || debits != 1 {
			t.Fatalf("round %d: statuses=%v balance=%d debits=%d", round, statuses, f.balance(t, w.ID), debits)
		}
		// Re-sending both changes nothing.
		for i := range 2 {
			if r := f.mustSubmit(t, w, op{extID: fmt.Sprintf("bet-%d-%d", round, i), amount: "80.00"}); !r.Replay {
				t.Fatalf("round %d: resend was not a replay", round)
			}
		}
		if f.balance(t, w.ID) != 2000 {
			t.Fatalf("round %d: resend changed the balance", round)
		}
	}
}

// Mandatory scenario 3: wallets advance independently. While wallet A is locked by
// a long transaction, wallet B is processed immediately; A's writer waits only for A.
func TestWallets_are_processed_independently(t *testing.T) {
	f := newFixture(t)
	a, b := f.open(t, "100.00"), f.open(t, "100.00")
	uow := postgres.NewUnitOfWork(f.db.App)
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = uow.Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
			if _, err := tx.Wallets().GetForUpdate(ctx, a.ID); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	start := time.Now()
	if r := f.mustSubmit(t, b, op{extID: "b-bet"}); r.Transaction.Status != wagering.StatusProcessed {
		t.Fatalf("wallet B: %+v", r.Transaction)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("wallet B waited %s behind wallet A", elapsed)
	}
	if _, err := f.submit(t, a, op{extID: "a-bet"}); !errors.Is(err, app.ErrTransient) {
		t.Fatalf("wallet A writer should time out on A's lock: %v", err)
	}
	close(release)
	<-done
	if r := f.mustSubmit(t, a, op{extID: "a-bet"}); r.Transaction.Status != wagering.StatusProcessed || r.Replay {
		t.Fatalf("wallet A after release: %+v", r)
	}
}

// Many wallets with many bets concurrently: every wallet ends exactly right.
func TestMany_wallets_concurrently_stay_consistent(t *testing.T) {
	f := newFixture(t)
	const wallets, betsPerWallet = 8, 10
	ws := make([]wallet.Snapshot, wallets)
	for i := range ws {
		ws[i] = f.open(t, "100.00")
	}
	_, errs := fanOut(t, wallets*betsPerWallet, func(i int) (app.SubmitResult, error) {
		return f.submit(t, ws[i%wallets], op{extID: fmt.Sprintf("bet-%d", i), amount: "10.00"})
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("submission %d: %v", i, err)
		}
	}
	for _, w := range ws {
		if f.balance(t, w.ID) != 0 || f.ledgerCount(t, w.ID) != betsPerWallet+1 {
			t.Fatalf("wallet %s: balance=%d ledger=%d", w.ID, f.balance(t, w.ID), f.ledgerCount(t, w.ID))
		}
	}
}
