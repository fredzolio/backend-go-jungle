package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

// Wagering implements provider operations (shared by HTTP and SQS).
type Wagering struct {
	d      Deps
	policy ReferencePolicy
}

// NewWagering builds the wagering use cases.
func NewWagering(d Deps, policy ReferencePolicy) *Wagering { return &Wagering{d: d, policy: policy} }

// SubmitCommand is one provider operation, already parsed and validated.
type SubmitCommand struct {
	Request wagering.ExternalRequest
	// AuthenticatedProvider is derived from the caller identity (token claim or
	// SQS sender); it must match the payload providerId.
	AuthenticatedProvider string
	Meta                  Meta
	// Within, when set, runs inside the same SQL transaction before commit
	// (the SQS consumer completes its inbox record here).
	Within func(ctx context.Context, tx Tx, result SubmitResult) error
}

// SubmitResult is the persisted outcome. Replay reports an idempotent replay:
// the stored result is returned and nothing is applied again.
type SubmitResult struct {
	Transaction wagering.Snapshot
	Replay      bool
}

// Submit processes an operation synchronously in a single SQL transaction:
//  1. INSERT the PENDING row, relying on the unique indexes (no check-then-insert);
//     on conflict, return the stored result (replay) or a conflict error.
//  2. Lock the wallet (per-wallet serialization point).
//  3. Evaluate the business rules and apply the decision: ledger + balance +
//     status + outbox events, or park it as PENDING_REFERENCE.
//
// No intermediate PENDING is ever committed: a crash before commit leaves nothing.
func (uc *Wagering) Submit(ctx context.Context, cmd SubmitCommand) (SubmitResult, error) {
	f := cmd.Request.Fields()
	if cmd.AuthenticatedProvider == "" || cmd.AuthenticatedProvider != f.ProviderID {
		return SubmitResult{}, ErrProviderMismatch
	}
	var result SubmitResult
	err := uc.d.UoW.Do(ctx, func(ctx context.Context, tx Tx) error {
		t, err := wagering.NewExternal(uc.d.IDs.New(), cmd.Request, uc.d.Clock.Now())
		if err != nil {
			return err
		}
		inserted, err := tx.Transactions().InsertExternal(ctx, t)
		switch {
		case errors.Is(err, ErrNotFound):
			return ErrWalletNotFound
		case err != nil:
			return err
		case !inserted:
			result, err = uc.replay(ctx, tx, cmd.Request)
		default:
			result, err = uc.process(ctx, tx, t, cmd.Meta)
		}
		if err != nil {
			return err
		}
		if cmd.Within != nil {
			return cmd.Within(ctx, tx, result)
		}
		return nil
	})
	if err != nil {
		return SubmitResult{}, err
	}
	return result, nil
}

func (uc *Wagering) process(ctx context.Context, tx Tx, t *wagering.Transaction, meta Meta) (SubmitResult, error) {
	w, err := tx.Wallets().GetForUpdate(ctx, t.WalletID())
	if err != nil {
		return SubmitResult{}, err
	}
	if err := uc.settle(ctx, settlement{tx: tx, t: t, w: w, meta: meta}); err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{Transaction: t.Snapshot()}, nil
}

// replay resolves an insert conflict. The same idempotency key with the same
// payload hash is a replay; a different hash is a conflict. When the key is new
// but (provider, externalTransactionId) exists, the operation was submitted with
// another key and is never applied again.
func (uc *Wagering) replay(ctx context.Context, tx Tx, req wagering.ExternalRequest) (SubmitResult, error) {
	f := req.Fields()
	existing, err := tx.Transactions().FindByIdempotencyKey(ctx, f.ProviderID, f.IdempotencyKey)
	switch {
	case err == nil:
		if existing.External().PayloadHash != req.PayloadHash() {
			return SubmitResult{}, ErrIdempotencyConflict
		}
		return SubmitResult{Transaction: existing.Snapshot(), Replay: true}, nil
	case !errors.Is(err, ErrNotFound):
		return SubmitResult{}, err
	}
	if _, err := tx.Transactions().FindByExternalID(ctx, f.ProviderID, f.ExternalTransactionID); err == nil {
		return SubmitResult{}, ErrDuplicateExternalTransaction
	} else if !errors.Is(err, ErrNotFound) {
		return SubmitResult{}, err
	}
	return SubmitResult{}, fmt.Errorf("%w: insert conflict without a matching transaction", ErrIntegrity)
}
