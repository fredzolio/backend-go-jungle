package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fredzolio/backend-go-jungle/internal/app"
)

// UnitOfWork implements app.UnitOfWork over a pgx pool. Lock and statement
// timeouts are session defaults set on every connection (see NewPool), so a
// blocked transaction fails fast as app.ErrTransient instead of hanging.
type UnitOfWork struct{ pool *pgxpool.Pool }

// NewUnitOfWork builds the unit of work.
func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork { return &UnitOfWork{pool: pool} }

var _ app.UnitOfWork = (*UnitOfWork)(nil)

// Do runs fn in a READ COMMITTED transaction; per-wallet consistency comes from
// row locks (SELECT ... FOR NO KEY UPDATE), not from the isolation level.
func (u *UnitOfWork) Do(ctx context.Context, fn func(context.Context, app.Tx) error) error {
	return u.run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, fn)
}

// Snapshot runs fn in a REPEATABLE READ, READ ONLY transaction.
func (u *UnitOfWork) Snapshot(ctx context.Context, fn func(context.Context, app.Tx) error) error {
	return u.run(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, fn)
}

func (u *UnitOfWork) run(ctx context.Context, opts pgx.TxOptions, fn func(context.Context, app.Tx) error) error {
	err := pgx.BeginTxFunc(ctx, u.pool, opts, func(tx pgx.Tx) error {
		return fn(ctx, stores{q: tx})
	})
	return classify("transaction", err)
}

// stores binds every store to one pgx transaction.
type stores struct{ q pgx.Tx }

func (s stores) Wallets() app.WalletStore           { return walletStore(s) }
func (s stores) Transactions() app.TransactionStore { return transactionStore(s) }
func (s stores) Ledger() app.LedgerStore            { return ledgerStore(s) }
func (s stores) Outbox() app.OutboxStore            { return outboxStore(s) }
func (s stores) Inbox() app.InboxStore              { return inboxStore(s) }
