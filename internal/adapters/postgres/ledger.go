package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

type ledgerStore stores

const ledgerColumns = `id, wallet_id, transaction_id, direction, currency, amount_minor,
	balance_before_minor, balance_after_minor, wallet_version, created_at`

func (s ledgerStore) Insert(ctx context.Context, e wallet.LedgerEntry) error {
	d := e.Data()
	_, err := s.q.Exec(ctx, `INSERT INTO wallet_ledger_entries (`+ledgerColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		d.ID, d.WalletID, d.TransactionID, d.Direction, d.Amount.Currency().Code(), d.Amount.Minor(),
		d.BalanceBefore.Minor(), d.BalanceAfter.Minor(), d.WalletVersion, d.CreatedAt.UTC())
	return classify("insert ledger entry", err)
}

func (s ledgerStore) List(ctx context.Context, walletID uuid.UUID, afterVersion int64, limit int) ([]wallet.LedgerEntry, error) {
	rows, err := s.q.Query(ctx, `SELECT `+ledgerColumns+` FROM wallet_ledger_entries
		WHERE wallet_id = $1 AND wallet_version > $2 ORDER BY wallet_version LIMIT $3`, walletID, afterVersion, limit)
	if err != nil {
		return nil, classify("list ledger", err)
	}
	defer rows.Close()
	var out []wallet.LedgerEntry
	for rows.Next() {
		var (
			d                     wallet.LedgerEntryData
			direction, currency   string
			amount, before, after int64
		)
		if err := rows.Scan(&d.ID, &d.WalletID, &d.TransactionID, &direction, &currency, &amount,
			&before, &after, &d.WalletVersion, &d.CreatedAt); err != nil {
			return nil, classify("scan ledger", err)
		}
		entry, err := rehydrateEntry(d, direction, currency, [3]int64{amount, before, after})
		if err != nil {
			return nil, fmt.Errorf("ledger entry %s: %w: %w", d.ID, app.ErrIntegrity, err)
		}
		out = append(out, entry)
	}
	return out, classify("list ledger", rows.Err())
}

func rehydrateEntry(d wallet.LedgerEntryData, direction, currency string, minors [3]int64) (wallet.LedgerEntry, error) {
	cur, err := money.ParseCurrency(currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	if d.Direction, err = wallet.ParseDirection(direction); err != nil {
		return wallet.LedgerEntry{}, err
	}
	values := make([]money.Money, 3)
	for i, m := range minors {
		if values[i], err = money.FromMinor(m, cur); err != nil {
			return wallet.LedgerEntry{}, err
		}
	}
	d.Amount, d.BalanceBefore, d.BalanceAfter = values[0], values[1], values[2]
	d.CreatedAt = d.CreatedAt.UTC()
	return wallet.NewLedgerEntry(d)
}

// Summary runs in the caller's (snapshot) transaction: one statement computes the
// rebuilt balance, the entry count and the first chain break (lag window).
func (s ledgerStore) Summary(ctx context.Context, walletID uuid.UUID) (app.LedgerSummary, error) {
	var sum app.LedgerSummary
	err := s.q.QueryRow(ctx, `
		WITH entries AS (
			SELECT direction, amount_minor, balance_before_minor, wallet_version,
			       lag(balance_after_minor) OVER (ORDER BY wallet_version) AS previous_after
			  FROM wallet_ledger_entries WHERE wallet_id = $1
		)
		SELECT coalesce(sum(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0)::bigint,
		       count(*),
		       coalesce(min(wallet_version) FILTER (WHERE previous_after IS NOT NULL AND balance_before_minor <> previous_after), 0)::bigint
		  FROM entries`, walletID).Scan(&sum.BalanceMinor, &sum.Entries, &sum.FirstBreak)
	return sum, classify("ledger summary", err)
}
