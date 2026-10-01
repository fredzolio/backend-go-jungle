package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

type transactionStore stores

// InsertExternal relies on the unique indexes, not on a prior SELECT: concurrent
// identical requests block on the in-flight row and, once it commits, see
// ON CONFLICT DO NOTHING (inserted=false) and read the winner.
func (s transactionStore) InsertExternal(ctx context.Context, t *wagering.Transaction) (bool, error) {
	ts := t.Snapshot()
	e := ts.External
	if e == nil {
		return false, fmt.Errorf("insert external: %w: transaction %s has no external metadata", app.ErrIntegrity, ts.ID)
	}
	tag, err := s.q.Exec(ctx, `INSERT INTO wager_transactions
		(id, origin, kind, status, wallet_id, player_id, currency, amount_minor, provider_id, external_transaction_id,
		 idempotency_key, payload_hash, round_id, game_id, reference_external_transaction_id, attempts, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT DO NOTHING`,
		ts.ID, ts.Origin, ts.Kind, ts.Status, ts.WalletID, ts.PlayerID, ts.Money.Currency().Code(), ts.Money.Minor(),
		e.ProviderID, e.ExternalTransactionID, e.IdempotencyKey, e.PayloadHash, e.RoundID, e.GameID,
		nullString(e.ReferenceExternalTransactionID), ts.Attempts, ts.CreatedAt.UTC(), ts.UpdatedAt.UTC())
	if violates(err, codeForeignKeyViolation, "wager_transactions_wallet_fkey") {
		return false, fmt.Errorf("wallet %s: %w", ts.WalletID, app.ErrNotFound)
	}
	if err != nil {
		return false, classify("insert external transaction", err)
	}
	return tag.RowsAffected() == 1, nil
}

// InsertOpening writes the internal OPENING as it stands (normally already
// PROCESSED, committed together with the wallet and its ledger entry).
func (s transactionStore) InsertOpening(ctx context.Context, t *wagering.Transaction) error {
	ts := t.Snapshot()
	var resultBalance, resultVersion *int64
	if r := ts.Result; r != nil {
		b, v := r.Balance.Minor(), r.WalletVersion
		resultBalance, resultVersion = &b, &v
	}
	_, err := s.q.Exec(ctx, `INSERT INTO wager_transactions
		(id, origin, kind, status, wallet_id, player_id, currency, amount_minor, result_balance_minor,
		 result_wallet_version, attempts, created_at, updated_at, processed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		ts.ID, ts.Origin, ts.Kind, ts.Status, ts.WalletID, ts.PlayerID, ts.Money.Currency().Code(), ts.Money.Minor(),
		resultBalance, resultVersion, ts.Attempts, ts.CreatedAt.UTC(), ts.UpdatedAt.UTC(), nullTime(ts.ProcessedAt))
	return classify("insert opening transaction", err)
}

func (s transactionStore) Get(ctx context.Context, id uuid.UUID) (*wagering.Transaction, error) {
	return s.one(ctx, `SELECT `+transactionColumns+` FROM wager_transactions WHERE id = $1`, id)
}

func (s transactionStore) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.Transaction, error) {
	return s.one(ctx, `SELECT `+transactionColumns+` FROM wager_transactions
		WHERE origin = 'EXTERNAL' AND provider_id = $1 AND idempotency_key = $2`, providerID, key)
}

func (s transactionStore) FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error) {
	return s.one(ctx, `SELECT `+transactionColumns+` FROM wager_transactions
		WHERE origin = 'EXTERNAL' AND provider_id = $1 AND external_transaction_id = $2`, providerID, externalID)
}

// FindForUpdate locks the referenced transaction so two reversals of it serialize.
// Lock order is always wallet first, then transactions, so no deadlock cycle exists.
func (s transactionStore) FindForUpdate(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error) {
	return s.one(ctx, `SELECT `+transactionColumns+` FROM wager_transactions
		WHERE origin = 'EXTERNAL' AND provider_id = $1 AND external_transaction_id = $2 FOR UPDATE`, providerID, externalID)
}

func (s transactionStore) GetForUpdate(ctx context.Context, id uuid.UUID) (*wagering.Transaction, error) {
	return s.one(ctx, `SELECT `+transactionColumns+` FROM wager_transactions WHERE id = $1 FOR UPDATE`, id)
}

func (s transactionStore) DuePendingReferences(ctx context.Context, now time.Time, limit int) ([]app.DueTransaction, error) {
	rows, err := s.q.Query(ctx, `SELECT id, wallet_id FROM wager_transactions
		WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= $1
		ORDER BY next_attempt_at LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, classify("due pending references", err)
	}
	defer rows.Close()
	var out []app.DueTransaction
	for rows.Next() {
		var d app.DueTransaction
		if err := rows.Scan(&d.ID, &d.WalletID); err != nil {
			return nil, classify("scan due", err)
		}
		out = append(out, d)
	}
	return out, classify("due pending references", rows.Err())
}

func (s transactionStore) one(ctx context.Context, query string, args ...any) (*wagering.Transaction, error) {
	t, err := scanTransaction(s.q.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrNotFound
	}
	if err != nil {
		return nil, classify("get transaction", err)
	}
	return t, nil
}

func (s transactionStore) HasProcessedReversal(ctx context.Context, referenceID uuid.UUID) (bool, error) {
	var exists bool
	err := s.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wager_transactions
		WHERE reference_transaction_id = $1 AND status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK'))`, referenceID).Scan(&exists)
	return exists, classify("check reversal", err)
}

func (s transactionStore) Update(ctx context.Context, t *wagering.Transaction) error {
	ts := t.Snapshot()
	var resultBalance, resultVersion *int64
	if r := ts.Result; r != nil {
		b, v := r.Balance.Minor(), r.WalletVersion
		resultBalance, resultVersion = &b, &v
	}
	tag, err := s.q.Exec(ctx, `UPDATE wager_transactions SET
		status = $2, reference_transaction_id = $3, failure_code = $4, result_balance_minor = $5,
		result_wallet_version = $6, attempts = $7, next_attempt_at = $8, reference_deadline = $9,
		updated_at = $10, processed_at = $11
		WHERE id = $1`,
		ts.ID, ts.Status, nullUUID(ts.ReferenceTransactionID), nullString(string(ts.FailureCode)), resultBalance,
		resultVersion, ts.Attempts, nullTime(ts.NextAttemptAt), nullTime(ts.ReferenceDeadline),
		ts.UpdatedAt.UTC(), nullTime(ts.ProcessedAt))
	if err != nil {
		return classify("update transaction", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("transaction %s: %w", ts.ID, app.ErrNotFound)
	}
	return nil
}

func (s transactionStore) WakeDependents(ctx context.Context, providerID, externalID string, at time.Time) error {
	_, err := s.q.Exec(ctx, `UPDATE wager_transactions SET next_attempt_at = $3
		WHERE status = 'PENDING_REFERENCE' AND provider_id = $1 AND reference_external_transaction_id = $2
		  AND next_attempt_at > $3`, providerID, externalID, at.UTC())
	return classify("wake dependents", err)
}
