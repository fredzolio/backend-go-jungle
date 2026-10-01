// Package app holds the use cases and the ports they need. Adapters implement the
// ports; the domain stays unaware of both.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

// Infrastructure-level errors returned by adapters, classifiable with errors.Is.
var (
	// ErrNotFound: the requested record does not exist.
	ErrNotFound = errors.New("app: not found")
	// ErrWalletExists: a wallet for (player, currency) already exists.
	ErrWalletExists = errors.New("app: wallet already exists")
	// ErrConcurrentUpdate: an optimistic version guard lost a race.
	ErrConcurrentUpdate = errors.New("app: concurrent update")
	// ErrTransient: retryable infrastructure failure (connection, timeout, lock
	// timeout, serialization or deadlock). Nothing was committed.
	ErrTransient = errors.New("app: transient infrastructure failure")
	// ErrIntegrity: a database invariant rejected the write. Permanent: retrying
	// the same input fails again; it indicates a bug or tampering.
	ErrIntegrity = errors.New("app: integrity violation")
)

// UnitOfWork runs fn inside one SQL transaction (READ COMMITTED). The transaction
// commits only if fn returns nil.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	// Snapshot runs fn in a REPEATABLE READ, READ ONLY transaction: every read sees
	// the same consistent view (used by reconciliation).
	Snapshot(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx exposes the stores bound to one SQL transaction.
type Tx interface {
	Wallets() WalletStore
	Transactions() TransactionStore
	Ledger() LedgerStore
	Outbox() OutboxStore
}

// WalletStore persists the Wallet aggregate.
type WalletStore interface {
	Insert(ctx context.Context, w *wallet.Wallet) error
	Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	// GetForUpdate locks the wallet row until the transaction ends (per-wallet
	// serialization point; wallets never wait on each other).
	GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	// Update writes balance/version guarded by expectedVersion.
	Update(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error
}

// TransactionStore persists wager transactions.
type TransactionStore interface {
	// InsertExternal inserts a PENDING external transaction unless one with the
	// same (provider, externalTransactionId) or (provider, idempotencyKey) exists;
	// inserted=false means the caller must read the existing one.
	InsertExternal(ctx context.Context, t *wagering.Transaction) (inserted bool, err error)
	InsertOpening(ctx context.Context, t *wagering.Transaction) error
	Get(ctx context.Context, id uuid.UUID) (*wagering.Transaction, error)
	FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.Transaction, error)
	FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error)
	// FindForUpdate locks a provider transaction by external id (reference resolution).
	FindForUpdate(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error)
	// HasProcessedReversal reports whether the transaction already has a PROCESSED REFUND/ROLLBACK.
	HasProcessedReversal(ctx context.Context, referenceID uuid.UUID) (bool, error)
	// Update persists state changes of an open transaction.
	Update(ctx context.Context, t *wagering.Transaction) error
	// WakeDependents makes transactions waiting on (provider, externalId) due now.
	WakeDependents(ctx context.Context, providerID, externalID string, at time.Time) error
}

// LedgerStore appends and reads ledger entries.
type LedgerStore interface {
	Insert(ctx context.Context, e wallet.LedgerEntry) error
	// List returns entries with wallet_version > afterVersion in ascending order.
	List(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) ([]wallet.LedgerEntry, error)
}

// OutboxRecord is one integration event to publish after commit.
type OutboxRecord struct {
	OccurredAt    time.Time
	AggregateType string
	AggregateID   string
	PartitionKey  string
	EventType     string
	Payload       []byte
	EventVersion  int
	EventID       uuid.UUID
}

// OutboxStore records events in the same transaction as the state that caused them.
type OutboxStore interface {
	Insert(ctx context.Context, records ...OutboxRecord) error
}
