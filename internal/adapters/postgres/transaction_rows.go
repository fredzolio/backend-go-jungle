package postgres

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

const transactionColumns = `id, origin, kind, status, wallet_id, player_id, currency, amount_minor,
	provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
	reference_external_transaction_id, reference_transaction_id, failure_code,
	result_balance_minor, result_wallet_version, attempts, next_attempt_at, reference_deadline,
	created_at, updated_at, processed_at`

// transactionRow mirrors one wager_transactions row; nullable columns are pointers.
type transactionRow struct {
	createdAt, updatedAt                          time.Time
	processedAt, nextAttemptAt, referenceDeadline *time.Time
	providerID, externalID, idempotencyKey        *string
	payloadHash, roundID, gameID, referenceExtID  *string
	failureCode                                   *string
	resultBalance, resultVersion                  *int64
	origin, kind, status, currency                string
	amount                                        int64
	attempts                                      int
	referenceID                                   uuid.NullUUID
	id, walletID, playerID                        uuid.UUID
}

func (r *transactionRow) targets() []any {
	return []any{&r.id, &r.origin, &r.kind, &r.status, &r.walletID, &r.playerID, &r.currency, &r.amount,
		&r.providerID, &r.externalID, &r.idempotencyKey, &r.payloadHash, &r.roundID, &r.gameID,
		&r.referenceExtID, &r.referenceID, &r.failureCode,
		&r.resultBalance, &r.resultVersion, &r.attempts, &r.nextAttemptAt, &r.referenceDeadline,
		&r.createdAt, &r.updatedAt, &r.processedAt}
}

func scanTransaction(row pgx.Row) (*wagering.Transaction, error) {
	var r transactionRow
	if err := row.Scan(r.targets()...); err != nil {
		return nil, err
	}
	s, err := r.snapshot()
	if err != nil {
		return nil, fmt.Errorf("transaction %s: %w: %w", r.id, app.ErrIntegrity, err)
	}
	return wagering.Rehydrate(s)
}

func (r *transactionRow) snapshot() (wagering.Snapshot, error) {
	cur, err := money.ParseCurrency(r.currency)
	if err != nil {
		return wagering.Snapshot{}, err
	}
	amount, err := money.FromMinor(r.amount, cur)
	if err != nil {
		return wagering.Snapshot{}, err
	}
	s := wagering.Snapshot{
		ID: r.id, Origin: wagering.Origin(r.origin), Kind: wagering.Kind(r.kind), Status: wagering.Status(r.status),
		WalletID: r.walletID, PlayerID: r.playerID, Money: amount, Attempts: r.attempts,
		CreatedAt: r.createdAt.UTC(), UpdatedAt: r.updatedAt.UTC(),
		ProcessedAt: utc(r.processedAt), NextAttemptAt: utc(r.nextAttemptAt), ReferenceDeadline: utc(r.referenceDeadline),
	}
	if r.referenceID.Valid {
		s.ReferenceTransactionID = r.referenceID.UUID
	}
	if r.failureCode != nil {
		s.FailureCode = wagering.FailureCode(*r.failureCode)
	}
	if r.resultBalance != nil && r.resultVersion != nil {
		balance, balErr := money.FromMinor(*r.resultBalance, cur)
		if balErr != nil {
			return wagering.Snapshot{}, balErr
		}
		s.Result = &wagering.Result{Balance: balance, WalletVersion: *r.resultVersion}
	}
	if s.Origin == wagering.OriginExternal {
		s.External = &wagering.External{
			ProviderID: deref(r.providerID), ExternalTransactionID: deref(r.externalID), IdempotencyKey: deref(r.idempotencyKey),
			PayloadHash: deref(r.payloadHash), RoundID: deref(r.roundID), GameID: deref(r.gameID),
			ReferenceExternalTransactionID: deref(r.referenceExtID),
		}
	}
	return s, nil
}

func utc(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// nullable helpers for writes.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func nullUUID(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil} }
