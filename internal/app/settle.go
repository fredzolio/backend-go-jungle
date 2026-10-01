package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/events"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

// settlement is one open transaction being decided, with its wallet locked.
type settlement struct {
	tx   Tx
	t    *wagering.Transaction
	w    *wallet.Wallet
	meta Meta
}

// settle evaluates and applies the decision for an open (PENDING or
// PENDING_REFERENCE) transaction. Lock order: wallet (caller), then the referenced
// transaction; every path takes them in this order, so no deadlock cycle exists.
func (uc *Wagering) settle(ctx context.Context, s settlement) error {
	ref, err := uc.reference(ctx, s)
	if err != nil {
		return err
	}
	decision, err := wagering.Evaluate(s.t, s.w, ref)
	if err != nil {
		return err
	}
	now := uc.d.Clock.Now()
	switch d := decision.(type) {
	case wagering.Move:
		return uc.applyMove(ctx, s, d)
	case wagering.NoMove:
		return uc.conclude(ctx, s, func() error { return s.t.Process(uc.observed(s.w), uuid.Nil, now) }, nil)
	case wagering.Reject:
		return uc.conclude(ctx, s, func() error { return s.t.Reject(d.Code, ptr(uc.observed(s.w)), now) }, nil)
	case wagering.AwaitReference:
		if s.t.ReferenceExpired(now) {
			// Waiting window over: absent reference => REFERENCE_NOT_FOUND; present
			// but never concluded => REFERENCE_NOT_PROCESSED.
			code := wagering.FailureReferenceNotFound
			if ref.Tx != nil {
				code = wagering.FailureReferenceNotProcessed
			}
			return uc.conclude(ctx, s, func() error { return s.t.Reject(code, ptr(uc.observed(s.w)), now) }, nil)
		}
		return uc.park(ctx, s)
	default:
		return fmt.Errorf("%w: unknown decision %T", ErrIntegrity, decision)
	}
}

// reference loads (and locks) the referenced transaction when the request names one.
func (uc *Wagering) reference(ctx context.Context, s settlement) (wagering.Reference, error) {
	ext := s.t.External()
	if ext.ReferenceExternalTransactionID == "" {
		return wagering.Reference{}, nil
	}
	refTx, err := s.tx.Transactions().FindForUpdate(ctx, ext.ProviderID, ext.ReferenceExternalTransactionID)
	if errors.Is(err, ErrNotFound) {
		return wagering.Reference{}, nil
	}
	if err != nil {
		return wagering.Reference{}, err
	}
	ref := wagering.Reference{Tx: refTx}
	if s.t.Kind().IsReversal() {
		if ref.AlreadyReversed, err = s.tx.Transactions().HasProcessedReversal(ctx, refTx.ID()); err != nil {
			return wagering.Reference{}, err
		}
	}
	return ref, nil
}

func (uc *Wagering) applyMove(ctx context.Context, s settlement, d wagering.Move) error {
	now := uc.d.Clock.Now()
	expected := s.w.Version()
	entry, err := s.w.Apply(d.Direction, wallet.Movement{EntryID: uc.d.IDs.New(), TransactionID: s.t.ID(), Amount: d.Amount, At: now})
	if err != nil {
		return err
	}
	if err := s.tx.Wallets().Update(ctx, s.w, expected); err != nil {
		return err
	}
	if err := s.tx.Ledger().Insert(ctx, entry); err != nil {
		return err
	}
	return uc.conclude(ctx, s, func() error { return s.t.Process(uc.observed(s.w), d.Reference, now) }, &entry)
}

// conclude applies a terminal transition, persists it with its events and wakes
// operations waiting on this one as their reference.
func (uc *Wagering) conclude(ctx context.Context, s settlement, transition func() error, entry *wallet.LedgerEntry) error {
	if err := transition(); err != nil {
		return err
	}
	if err := s.tx.Transactions().Update(ctx, s.t); err != nil {
		return err
	}
	var records []OutboxRecord
	var err error
	if s.t.Status() == wagering.StatusRejected {
		records, err = uc.rejectedEvent(s)
	} else {
		records, err = uc.d.movementEvents(s.meta, s.t, entry)
	}
	if err != nil {
		return err
	}
	if err := s.tx.Outbox().Insert(ctx, records...); err != nil {
		return err
	}
	ext := s.t.External()
	return s.tx.Transactions().WakeDependents(ctx, ext.ProviderID, ext.ExternalTransactionID, uc.d.Clock.Now())
}

func (uc *Wagering) rejectedEvent(s settlement) ([]OutboxRecord, error) {
	at := s.t.Snapshot().UpdatedAt
	env, err := events.NewTransactionRejected(s.meta.event(uc.d.IDs, at, s.t.ID().String()), s.t)
	if err != nil {
		return nil, err
	}
	rec, err := outboxRecord(env, "WagerTransaction", s.t.WalletID())
	return []OutboxRecord{rec}, err
}

// park moves PENDING to PENDING_REFERENCE (first time: emits the event) or
// schedules the next attempt of an already parked transaction.
func (uc *Wagering) park(ctx context.Context, s settlement) error {
	now := uc.d.Clock.Now()
	first := s.t.Status() == wagering.StatusPending
	deadline := now.Add(uc.policy.TTL)
	if !first {
		deadline = s.t.Snapshot().ReferenceDeadline
	}
	// Never sleep past the deadline: the attempt at the deadline concludes it.
	next := uc.policy.NextAttempt(now, s.t.Snapshot().Attempts)
	if next.After(deadline) {
		next = deadline
	}
	if err := s.t.AwaitReference(next, deadline, now); err != nil {
		return err
	}
	if err := s.tx.Transactions().Update(ctx, s.t); err != nil {
		return err
	}
	if !first {
		return nil
	}
	env, err := events.NewTransactionPendingReference(s.meta.event(uc.d.IDs, now, s.t.ID().String()), s.t)
	if err != nil {
		return err
	}
	rec, err := outboxRecord(env, "WagerTransaction", s.t.WalletID())
	if err != nil {
		return err
	}
	return s.tx.Outbox().Insert(ctx, rec)
}

// observed is the wallet state reported to the provider and on every replay.
func (uc *Wagering) observed(w *wallet.Wallet) wagering.Result {
	return wagering.Result{Balance: w.Balance(), WalletVersion: w.Version()}
}

func ptr[T any](v T) *T { return &v }
