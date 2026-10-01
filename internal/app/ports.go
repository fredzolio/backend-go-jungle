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
	Inbox() InboxStore
}

// WalletStore persists the Wallet aggregate.
type WalletStore interface {
	Insert(ctx context.Context, w *wallet.Wallet) error
	Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	// GetForUpdate locks the wallet row until the transaction ends (per-wallet
	// serialization point; wallets never wait on each other).
	GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	// TryGetForUpdate locks the wallet unless another transaction holds it
	// (SKIP LOCKED); ok=false means "busy, try later". Used by background workers.
	TryGetForUpdate(ctx context.Context, id uuid.UUID) (w *wallet.Wallet, ok bool, err error)
	// Update writes balance/version guarded by expectedVersion.
	Update(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error
	// IDsAfter pages wallet ids in ascending order (reconciliation sweep).
	IDsAfter(ctx context.Context, after uuid.UUID, limit int) ([]uuid.UUID, error)
}

// DueTransaction identifies a PENDING_REFERENCE transaction ready for a retry.
type DueTransaction struct {
	ID       uuid.UUID
	WalletID uuid.UUID
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
	// GetForUpdate locks a transaction by id.
	GetForUpdate(ctx context.Context, id uuid.UUID) (*wagering.Transaction, error)
	// DuePendingReferences lists PENDING_REFERENCE transactions due at now, oldest
	// first, without locking (the caller locks wallet then transaction).
	DuePendingReferences(ctx context.Context, now time.Time, limit int) ([]DueTransaction, error)
	// CountPendingReferences counts transactions waiting for their reference.
	CountPendingReferences(ctx context.Context) (int64, error)
}

// LedgerStore appends and reads ledger entries.
type LedgerStore interface {
	Insert(ctx context.Context, e wallet.LedgerEntry) error
	// List returns entries with wallet_version > afterVersion in ascending order.
	List(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) ([]wallet.LedgerEntry, error)
	// Summary rebuilds the balance from the ledger and checks chain continuity.
	Summary(ctx context.Context, walletID uuid.UUID) (LedgerSummary, error)
}

// LedgerSummary is the ledger-side view used by reconciliation.
type LedgerSummary struct {
	// BalanceMinor = sum(credits) - sum(debits), in minor units.
	BalanceMinor int64
	Entries      int64
	// FirstBreak is the first wallet_version whose balance_before differs from the
	// previous balance_after (0 when the chain is intact).
	FirstBreak int64
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

// InboxRecord is the durable identity of one consumed message.
type InboxRecord struct {
	ReceivedAt    time.Time
	ConsumerName  string
	MessageID     string
	PayloadHash   string
	TransactionID uuid.UUID
}

// InboxStore deduplicates messages per consumer.
type InboxStore interface {
	// Complete records the message as handled; inserted=false returns the hash
	// stored by the first delivery.
	Complete(ctx context.Context, r InboxRecord) (inserted bool, storedHash string, err error)
}

// ClaimedEvent is an outbox event leased by one relay. ClaimID fences the lease:
// only the holder of the current claim can confirm it.
type ClaimedEvent struct {
	PartitionKey string
	EventType    string
	Payload      []byte
	EventVersion int
	Attempts     int
	ID           uuid.UUID
	ClaimID      uuid.UUID
}

// OutboxRelayStore is used by the relay outside any business transaction.
type OutboxRelayStore interface {
	// Claim leases up to limit publishable events: only the oldest pending event
	// of each partition (wallet) is eligible, so per-wallet order is preserved
	// across any number of relays. Expired leases are reclaimable.
	Claim(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]ClaimedEvent, error)
	// MarkPublished confirms a publication; false means the lease was lost.
	MarkPublished(ctx context.Context, id, claimID uuid.UUID, at time.Time) (bool, error)
	// Reschedule releases the lease after a failed publication (dead parks it).
	Reschedule(ctx context.Context, e ClaimedEvent, next time.Time, lastErr string, dead bool) error
	// Backlog reports the age of the oldest pending event and the pending count.
	Backlog(ctx context.Context, now time.Time) (oldest time.Duration, pending int64, err error)
}

// EventPublisher publishes events to the integration destination, reporting an
// error per event id (nil = accepted by the broker).
type EventPublisher interface {
	Publish(ctx context.Context, events []ClaimedEvent) map[uuid.UUID]error
}
