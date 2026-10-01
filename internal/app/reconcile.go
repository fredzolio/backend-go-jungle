package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
)

// Reconciliation compares the stored balance with the balance rebuilt from the
// ledger (opening included), on one consistent snapshot. It never changes data.
type Reconciliation struct {
	Stored         money.Money
	Calculated     money.Money
	Difference     money.Money // stored - calculated
	CheckedEntries int64
	WalletID       uuid.UUID
	Consistent     bool
	// ChainIntact: every entry starts where the previous one ended.
	ChainIntact bool
}

// Reconciler implements reconciliation, on demand and as a periodic sweep.
type Reconciler struct {
	d      Deps
	log    *slog.Logger
	cursor uuid.UUID // sweep position; restarts from the beginning when exhausted
}

// NewReconciler builds the reconciler.
func NewReconciler(d Deps, log *slog.Logger) *Reconciler { return &Reconciler{d: d, log: log} }

// Reconcile checks one wallet. Divergence is reported in the result, the logs and
// the metrics.
func (r *Reconciler) Reconcile(ctx context.Context, walletID uuid.UUID) (Reconciliation, error) {
	var out Reconciliation
	err := r.d.UoW.Snapshot(ctx, func(ctx context.Context, tx Tx) error {
		w, err := tx.Wallets().Get(ctx, walletID)
		if err != nil {
			return walletNotFound(err)
		}
		sum, err := tx.Ledger().Summary(ctx, walletID)
		if err != nil {
			return err
		}
		calculated, err := money.FromMinor(sum.BalanceMinor, w.Currency())
		if err != nil {
			return err
		}
		diff, err := w.Balance().Sub(calculated)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrIntegrity, err)
		}
		out = Reconciliation{
			WalletID: walletID, Stored: w.Balance(), Calculated: calculated, Difference: diff,
			CheckedEntries: sum.Entries, ChainIntact: sum.FirstBreak == 0,
			Consistent: diff.IsZero() && sum.FirstBreak == 0,
		}
		return nil
	})
	if err != nil {
		return Reconciliation{}, err
	}
	r.d.metrics().ReconciliationChecked(out.Consistent)
	if !out.Consistent {
		r.log.ErrorContext(ctx, "wallet ledger divergence", slog.String("walletId", walletID.String()),
			slog.Bool("consistent", false), slog.Int64("checked", out.CheckedEntries))
	}
	return out, nil
}

// Sweep reconciles the next batch of wallets and returns how many it checked.
// A single process runs the sweep (role "reconciler"); it is read-only, so
// running more is harmless.
func (r *Reconciler) Sweep(ctx context.Context, batch int) (int, error) {
	var ids []uuid.UUID
	err := r.d.UoW.Snapshot(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		ids, err = tx.Wallets().IDsAfter(ctx, r.cursor, batch)
		return err
	})
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		r.cursor = uuid.Nil
		return 0, nil
	}
	divergent := 0
	for _, id := range ids {
		res, err := r.Reconcile(ctx, id)
		if err != nil {
			return 0, err
		}
		if !res.Consistent {
			divergent++
		}
	}
	r.cursor = ids[len(ids)-1]
	if divergent > 0 {
		r.log.ErrorContext(ctx, "reconciliation sweep found divergences", slog.Int("divergent", divergent), slog.Int("checked", len(ids)))
	}
	return len(ids), nil
}
