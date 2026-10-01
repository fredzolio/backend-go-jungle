package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

type walletStore stores

const walletColumns = `id, player_id, currency, balance_minor, version, created_at, updated_at`

func (s walletStore) Insert(ctx context.Context, w *wallet.Wallet) error {
	ws := w.Snapshot()
	_, err := s.q.Exec(ctx, `INSERT INTO wallets (`+walletColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		ws.ID, ws.PlayerID, ws.Balance.Currency().Code(), ws.Balance.Minor(), ws.Version, ws.CreatedAt.UTC(), ws.UpdatedAt.UTC())
	if violates(err, codeUniqueViolation, "wallets_player_currency_key") {
		return app.ErrWalletExists
	}
	return classify("insert wallet", err)
}

func (s walletStore) Get(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return s.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id)
}

// GetForUpdate takes FOR NO KEY UPDATE: it serializes writers of this wallet but
// does not block foreign-key checks of rows referencing it (ledger inserts).
func (s walletStore) GetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	return s.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR NO KEY UPDATE`, id)
}

// TryGetForUpdate is GetForUpdate with SKIP LOCKED: a busy wallet is skipped,
// never waited for.
func (s walletStore) TryGetForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, bool, error) {
	w, err := s.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR NO KEY UPDATE SKIP LOCKED`, id)
	if errors.Is(err, app.ErrNotFound) {
		return nil, false, nil
	}
	return w, err == nil, err
}

func (s walletStore) get(ctx context.Context, query string, id uuid.UUID) (*wallet.Wallet, error) {
	var (
		snap     wallet.Snapshot
		currency string
		minor    int64
	)
	err := s.q.QueryRow(ctx, query, id).Scan(&snap.ID, &snap.PlayerID, &currency, &minor, &snap.Version, &snap.CreatedAt, &snap.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("wallet %s: %w", id, app.ErrNotFound)
	}
	if err != nil {
		return nil, classify("get wallet", err)
	}
	cur, err := money.ParseCurrency(currency)
	if err != nil {
		return nil, fmt.Errorf("wallet %s: %w: %w", id, app.ErrIntegrity, err)
	}
	if snap.Balance, err = money.FromMinor(minor, cur); err != nil {
		return nil, fmt.Errorf("wallet %s: %w: %w", id, app.ErrIntegrity, err)
	}
	snap.CreatedAt, snap.UpdatedAt = snap.CreatedAt.UTC(), snap.UpdatedAt.UTC()
	return wallet.Rehydrate(snap)
}

// Update is guarded by expectedVersion: a writer that lost a race (which the row
// lock already prevents) can never overwrite a committed balance.
func (s walletStore) Update(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error {
	ws := w.Snapshot()
	tag, err := s.q.Exec(ctx,
		`UPDATE wallets SET balance_minor = $2, version = $3, updated_at = $4 WHERE id = $1 AND version = $5`,
		ws.ID, ws.Balance.Minor(), ws.Version, ws.UpdatedAt.UTC(), expectedVersion)
	if err != nil {
		return classify("update wallet", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("wallet %s at version %d: %w", ws.ID, expectedVersion, app.ErrConcurrentUpdate)
	}
	return nil
}

func (s walletStore) IDsAfter(ctx context.Context, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	rows, err := s.q.Query(ctx, `SELECT id FROM wallets WHERE id > $1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, classify("list wallet ids", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, classify("scan wallet id", err)
		}
		ids = append(ids, id)
	}
	return ids, classify("list wallet ids", rows.Err())
}
