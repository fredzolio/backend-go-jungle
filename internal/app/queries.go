package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

// ErrTransactionNotFound: no such transaction (or not visible to the caller).
var ErrTransactionNotFound = errors.New("app: transaction not found")

// Queries are read-only use cases; each runs on one consistent snapshot.
type Queries struct{ uow UnitOfWork }

// NewQueries builds the read use cases.
func NewQueries(d Deps) *Queries { return &Queries{uow: d.UoW} }

// Wallet returns the current wallet state.
func (q *Queries) Wallet(ctx context.Context, id uuid.UUID) (wallet.Snapshot, error) {
	var out wallet.Snapshot
	err := q.uow.Snapshot(ctx, func(ctx context.Context, tx Tx) error {
		w, err := tx.Wallets().Get(ctx, id)
		if err != nil {
			return walletNotFound(err)
		}
		out = w.Snapshot()
		return nil
	})
	return out, err
}

// LedgerPage is one page of entries in wallet_version order.
type LedgerPage struct {
	Entries []wallet.LedgerEntry
	// More reports that entries after the last one exist.
	More bool
}

// Ledger lists entries with wallet_version > afterVersion (stable order).
func (q *Queries) Ledger(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) (LedgerPage, error) {
	var page LedgerPage
	err := q.uow.Snapshot(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.Wallets().Get(ctx, walletID); err != nil {
			return walletNotFound(err)
		}
		entries, err := tx.Ledger().List(ctx, walletID, afterVersion, limit+1)
		if err != nil {
			return err
		}
		page.More = len(entries) > limit
		page.Entries = entries[:min(limit, len(entries))]
		return nil
	})
	return page, err
}

// Transaction returns a transaction by internal id.
func (q *Queries) Transaction(ctx context.Context, id uuid.UUID) (wagering.Snapshot, error) {
	return q.transaction(ctx, func(ctx context.Context, s TransactionStore) (*wagering.Transaction, error) { return s.Get(ctx, id) })
}

// ProviderTransaction returns a provider transaction by external id.
func (q *Queries) ProviderTransaction(ctx context.Context, providerID, externalID string) (wagering.Snapshot, error) {
	return q.transaction(ctx, func(ctx context.Context, s TransactionStore) (*wagering.Transaction, error) {
		return s.FindByExternalID(ctx, providerID, externalID)
	})
}

func (q *Queries) transaction(ctx context.Context, find func(context.Context, TransactionStore) (*wagering.Transaction, error)) (wagering.Snapshot, error) {
	var out wagering.Snapshot
	err := q.uow.Snapshot(ctx, func(ctx context.Context, tx Tx) error {
		t, err := find(ctx, tx.Transactions())
		if errors.Is(err, ErrNotFound) {
			return ErrTransactionNotFound
		}
		if err != nil {
			return err
		}
		out = t.Snapshot()
		return nil
	})
	return out, err
}

func walletNotFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: %w", ErrWalletNotFound, err)
	}
	return err
}

// PendingReferences counts transactions waiting for their reference (gauge).
func (q *Queries) PendingReferences(ctx context.Context) (int64, error) {
	var n int64
	err := q.uow.Snapshot(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		n, err = tx.Transactions().CountPendingReferences(ctx)
		return err
	})
	return n, err
}
