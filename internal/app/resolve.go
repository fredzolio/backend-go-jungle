package app

import (
	"context"
	"errors"
	"strings"

	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/platform/faults"
)

// ResolveDue retries PENDING_REFERENCE transactions whose next attempt is due and
// returns how many it examined and settled (concluded or rescheduled).
//
// Safe with any number of concurrent resolvers (other processes included):
// candidates are listed without locks, then each one is handled in its own SQL
// transaction that locks the wallet first (SKIP LOCKED: a busy wallet is left for
// the next poll) and then the transaction, re-checking that it is still due. The
// lock order wallet -> transaction matches Submit, so the two never deadlock.
// Nothing is held between polls, so a crashed resolver leaves no lease behind:
// its rolled-back work is simply picked up again.
func (uc *Wagering) ResolveDue(ctx context.Context, limit int) (int, error) {
	var due []DueTransaction
	err := uc.d.UoW.Do(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		due, err = tx.Transactions().DuePendingReferences(ctx, uc.d.Clock.Now(), limit)
		return err
	})
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, d := range due {
		if ctx.Err() != nil {
			return settled, ctx.Err()
		}
		done, err := uc.resolveOne(ctx, d)
		if err != nil {
			return settled, err
		}
		if done {
			settled++
		}
	}
	return settled, nil
}

func (uc *Wagering) resolveOne(ctx context.Context, d DueTransaction) (bool, error) {
	settled, outcome := false, ""
	err := uc.d.UoW.Do(ctx, func(ctx context.Context, tx Tx) error {
		w, ok, err := tx.Wallets().TryGetForUpdate(ctx, d.WalletID)
		if err != nil || !ok {
			return err
		}
		t, err := tx.Transactions().GetForUpdate(ctx, d.ID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		now := uc.d.Clock.Now()
		if t.Status() != wagering.StatusPendingReference || t.Snapshot().NextAttemptAt.After(now) {
			return nil // another resolver (or a wake-up race) already handled it
		}
		ext := t.External()
		meta := Meta{CorrelationID: t.ID().String(), CausationID: ext.ReferenceExternalTransactionID, Channel: "resolver"}
		if err := uc.settle(ctx, settlement{tx: tx, t: t, w: w, meta: meta}); err != nil {
			return err
		}
		faults.Point("resolver.before_commit")
		settled = true
		outcome = "rescheduled"
		if t.Status().IsTerminal() {
			outcome = "concluded_" + strings.ToLower(string(t.Status()))
		}
		return nil
	})
	if settled && err == nil {
		uc.d.metrics().ReferenceAttempt(outcome)
	}
	if isContention(err) {
		uc.d.metrics().ConcurrencyConflict("resolve")
	}
	return settled, err
}
