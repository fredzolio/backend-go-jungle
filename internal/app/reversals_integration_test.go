//go:build integration

package app_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

// fakeClock is a controllable clock shared by every instance of a test.
type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var testPolicy = app.ReferencePolicy{InitialBackoff: time.Second, MaxBackoff: 8 * time.Second, TTL: time.Minute}

func newClockedFixture(t *testing.T) (fixture, *fakeClock) {
	t.Helper()
	f := newFixture(t)
	clock := &fakeClock{now: time.Now().UTC().Truncate(time.Microsecond)}
	deps := app.Deps{UoW: postgres.NewUnitOfWork(f.db.App), Clock: clock, IDs: app.UUIDv7{}}
	f.wallets, f.wagering, f.clock = app.NewWallets(deps), app.NewWagering(deps, testPolicy), clock
	return f, clock
}

// otherInstance simulates another process: same database, its own use case objects.
func (f fixture) otherInstance(clock app.Clock) *app.Wagering {
	return app.NewWagering(app.Deps{UoW: postgres.NewUnitOfWork(f.db.App), Clock: clock, IDs: app.UUIDv7{}}, testPolicy)
}

func (f fixture) status(t *testing.T, extID string) (wagering.Status, string) {
	t.Helper()
	var status string
	var code *string
	err := f.db.Owner.QueryRow(context.Background(),
		`SELECT status, failure_code FROM wager_transactions WHERE external_transaction_id = $1`, extID).Scan(&status, &code)
	if err != nil {
		t.Fatal(err)
	}
	if code == nil {
		return wagering.Status(status), ""
	}
	return wagering.Status(status), *code
}

func resolve(t *testing.T, uc *app.Wagering) int {
	t.Helper()
	n, err := uc.ResolveDue(context.Background(), 100)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return n
}

// Mandatory scenario 7 (first half): REFUND before its BET is resolved later.
func TestRefund_before_its_bet_waits_and_resolves_after_the_bet(t *testing.T) {
	f, _ := newClockedFixture(t)
	w := f.open(t, "100.00")
	pending := f.mustSubmit(t, w, op{kind: "REFUND", extID: "refund-1", ref: "bet-1"})
	if pending.Transaction.Status != wagering.StatusPendingReference {
		t.Fatalf("refund = %s", pending.Transaction.Status)
	}
	if got := f.outboxTypes(t, w.ID); got[len(got)-1] != "WagerTransactionPendingReference" {
		t.Fatalf("outbox = %v", got)
	}
	f.mustSubmit(t, w, op{kind: "BET", extID: "bet-1"}) // wakes the refund (due now)
	if n := resolve(t, f.otherInstance(f.clock)); n != 1 {
		t.Fatalf("resolved %d", n)
	}
	if st, _ := f.status(t, "refund-1"); st != wagering.StatusProcessed || f.balance(t, w.ID) != 10000 || f.ledgerCount(t, w.ID) != 3 {
		t.Fatalf("refund=%s balance=%d ledger=%d", st, f.balance(t, w.ID), f.ledgerCount(t, w.ID))
	}
}

// Mandatory scenario 7 (second half): without its reference, expiry rejects it.
func TestReversal_without_reference_is_rejected_after_ttl(t *testing.T) {
	f, clock := newClockedFixture(t)
	w := f.open(t, "100.00")
	f.mustSubmit(t, w, op{kind: "ROLLBACK", extID: "rb-1", ref: "never-arrives"})

	clock.Advance(2 * time.Second) // first retry: still waiting, rescheduled, no new event
	if n := resolve(t, f.wagering); n != 1 {
		t.Fatalf("resolved %d", n)
	}
	if st, _ := f.status(t, "rb-1"); st != wagering.StatusPendingReference {
		t.Fatalf("status = %s", st)
	}
	attempts := queryInt(t, f.db.Owner, `SELECT attempts FROM wager_transactions WHERE external_transaction_id = 'rb-1'`)
	pendingEvents := queryInt(t, f.db.Owner, `SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionPendingReference'`)
	if attempts != 2 || pendingEvents != 1 {
		t.Fatalf("attempts=%d pendingEvents=%d", attempts, pendingEvents)
	}

	clock.Advance(testPolicy.TTL)
	resolve(t, f.wagering)
	if st, code := f.status(t, "rb-1"); st != wagering.StatusRejected || code != string(wagering.FailureReferenceNotFound) {
		t.Fatalf("status=%s code=%s", st, code)
	}
	if got := f.outboxTypes(t, w.ID); got[len(got)-1] != "WagerTransactionRejected" {
		t.Fatalf("outbox = %v", got)
	}
}

func TestReversal_of_a_rejected_reference_is_rejected_immediately(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "10.00")
	f.mustSubmit(t, w, op{extID: "bet-big", amount: "50.00"}) // INSUFFICIENT_FUNDS
	r := f.mustSubmit(t, w, op{kind: "REFUND", extID: "refund-big", amount: "50.00", ref: "bet-big"})
	if r.Transaction.Status != wagering.StatusRejected || r.Transaction.FailureCode != wagering.FailureReferenceNotProcessed {
		t.Fatalf("refund = %+v", r.Transaction)
	}
}

func TestReversal_rules_against_the_database(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "100.00")
	f.mustSubmit(t, w, op{extID: "bet-1"})                                        // 75
	f.mustSubmit(t, w, op{kind: "REFUND", extID: "refund-1", ref: "bet-1"})       // 100
	f.mustSubmit(t, w, op{kind: "ROLLBACK", extID: "rb-refund", ref: "refund-1"}) // 75: undo the refund
	cases := []struct {
		o    op
		code wagering.FailureCode
	}{
		{op{kind: "REFUND", extID: "refund-again", ref: "bet-1"}, wagering.FailureAlreadyReversed},
		{op{kind: "ROLLBACK", extID: "rb-bet", ref: "bet-1"}, wagering.FailureAlreadyReversed},
		{op{kind: "ROLLBACK", extID: "rb-rb", ref: "rb-refund"}, wagering.FailureReferenceKindInvalid},
		{op{kind: "REFUND", extID: "refund-partial", amount: "10.00", ref: "bet-1"}, wagering.FailureReversalAmountMismatch},
		{op{kind: "REFUND", extID: "refund-other-round", round: "round-2", ref: "bet-1"}, wagering.FailureReferenceMismatch},
	}
	for _, c := range cases {
		r := f.mustSubmit(t, w, c.o)
		if r.Transaction.Status != wagering.StatusRejected || r.Transaction.FailureCode != c.code {
			t.Fatalf("%s: %s %s", c.o.extID, r.Transaction.Status, r.Transaction.FailureCode)
		}
	}
	if f.balance(t, w.ID) != 7500 || f.ledgerCount(t, w.ID) != 4 {
		t.Fatalf("balance=%d ledger=%d", f.balance(t, w.ID), f.ledgerCount(t, w.ID))
	}
}

func TestRollback_of_a_win_beyond_balance_uses_reversal_code(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "0.00")
	f.mustSubmit(t, w, op{kind: "WIN", extID: "win-1", amount: "50.00"})
	f.mustSubmit(t, w, op{extID: "bet-1", amount: "40.00"}) // balance 10
	r := f.mustSubmit(t, w, op{kind: "ROLLBACK", extID: "rb-win", amount: "50.00", ref: "win-1"})
	if r.Transaction.FailureCode != wagering.FailureReversalInsufficientFunds {
		t.Fatalf("rollback = %+v", r.Transaction)
	}
}

// REFUND and ROLLBACK of the same BET racing: exactly one returns the money.
func TestRefund_and_rollback_racing_on_the_same_bet_return_money_once(t *testing.T) {
	f := newFixture(t)
	for round := range 5 {
		w := f.open(t, "100.00")
		bet := fmt.Sprintf("bet-%d", round)
		f.mustSubmit(t, w, op{extID: bet})
		kinds := []string{"REFUND", "ROLLBACK"}
		results, errs := fanOut(t, 2, func(i int) (app.SubmitResult, error) {
			return f.submit(t, w, op{kind: kinds[i], extID: fmt.Sprintf("%s-%d", kinds[i], round), ref: bet})
		})
		processed := 0
		for i, r := range results {
			if errs[i] != nil {
				t.Fatal(errs[i])
			}
			if r.Transaction.Status == wagering.StatusProcessed {
				processed++
			} else if r.Transaction.FailureCode != wagering.FailureAlreadyReversed {
				t.Fatalf("loser code %s", r.Transaction.FailureCode)
			}
		}
		if processed != 1 || f.balance(t, w.ID) != 10000 {
			t.Fatalf("round %d: processed=%d balance=%d", round, processed, f.balance(t, w.ID))
		}
	}
}

// Several resolver instances race over the same backlog: each pending
// transaction is concluded exactly once.
func TestConcurrent_resolvers_conclude_each_pending_once(t *testing.T) {
	f, clock := newClockedFixture(t)
	const wallets = 12
	ws := make([]wallet.Snapshot, wallets)
	for i := range ws {
		ws[i] = f.open(t, "100.00")
		f.mustSubmit(t, ws[i], op{kind: "REFUND", extID: fmt.Sprintf("refund-%d", i), ref: fmt.Sprintf("bet-%d", i)})
	}
	for i := range ws { // bets arrive; this instance does not resolve
		f.mustSubmit(t, ws[i], op{extID: fmt.Sprintf("bet-%d", i)})
	}
	var wg sync.WaitGroup
	for range 3 {
		uc := f.otherInstance(clock)
		wg.Go(func() {
			for range 5 {
				if _, err := uc.ResolveDue(context.Background(), 4); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	for i, w := range ws {
		if st, _ := f.status(t, fmt.Sprintf("refund-%d", i)); st != wagering.StatusProcessed {
			t.Fatalf("refund-%d = %s", i, st)
		}
		if f.balance(t, w.ID) != 10000 || f.ledgerCount(t, w.ID) != 3 {
			t.Fatalf("wallet %d: balance=%d ledger=%d", i, f.balance(t, w.ID), f.ledgerCount(t, w.ID))
		}
	}
}
