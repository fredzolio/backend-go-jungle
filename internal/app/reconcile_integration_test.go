//go:build integration

package app_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

type recordingMetrics struct {
	app.NopMetrics
	mu        sync.Mutex
	divergent int
}

func (m *recordingMetrics) ReconciliationChecked(consistent bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !consistent {
		m.divergent++
	}
}

// tamper bypasses every trigger as superuser (session_replication_role=replica):
// the only way to create a divergence, used to prove reconciliation detects it.
func (f fixture) tamper(t *testing.T, sql string, args ...any) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), env.DSN("postgres", f.db.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	err = pgx.BeginFunc(context.Background(), conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), sql, args...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f fixture) reconciler(m app.Metrics) *app.Reconciler {
	deps := app.Deps{UoW: postgres.NewUnitOfWork(f.db.App), Clock: app.SystemClock{}, IDs: app.UUIDv7{}, Metrics: m}
	return app.NewReconciler(deps, silentLog)
}

func TestReconcile_reports_consistency_after_real_operations(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "1000.00")
	f.mustSubmit(t, w, op{extID: "bet-1"})
	f.mustSubmit(t, w, op{kind: "WIN", extID: "win-1", amount: "100.00"})
	f.mustSubmit(t, w, op{kind: "LOSS", extID: "loss-1", amount: "0.00"})
	res, err := f.reconciler(nil).Reconcile(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Consistent || !res.ChainIntact || res.CheckedEntries != 3 || res.Stored.Amount() != "1075.00" ||
		res.Calculated.Amount() != "1075.00" || res.Difference.Amount() != "0.00" {
		t.Fatalf("reconciliation = %+v", res)
	}
}

func TestReconcile_detects_balance_divergence_without_changing_data(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "100.00")
	f.tamper(t, `UPDATE wallets SET balance_minor = balance_minor + 1 WHERE id = $1`, w.ID)
	m := &recordingMetrics{}
	res, err := f.reconciler(m).Reconcile(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Consistent || res.Difference.Amount() != "0.01" || res.Stored.Amount() != "100.01" || m.divergent != 1 {
		t.Fatalf("reconciliation = %+v divergent=%d", res, m.divergent)
	}
	if f.balance(t, w.ID) != 10001 {
		t.Fatal("reconciliation must not change the stored balance")
	}
}

func TestReconcile_detects_a_broken_ledger_chain(t *testing.T) {
	f := newFixture(t)
	w := f.open(t, "100.00")
	r := f.mustSubmit(t, w, op{extID: "bet-1", amount: "10.00"})
	if r.Transaction.Status != wagering.StatusProcessed {
		t.Fatal(r.Transaction.Status)
	}
	// Rewrite history: the debit now claims to start from 50.00 (sum still matches).
	f.tamper(t, `UPDATE wallet_ledger_entries SET balance_before_minor = 5000, balance_after_minor = 4000 WHERE wallet_id = $1 AND wallet_version = 2`, w.ID)
	res, err := f.reconciler(nil).Reconcile(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Consistent || res.ChainIntact {
		t.Fatalf("chain break not detected: %+v", res)
	}
}

func TestSweep_checks_every_wallet_and_counts_divergences(t *testing.T) {
	f := newFixture(t)
	var ids []uuid.UUID
	for range 5 {
		ids = append(ids, f.open(t, "10.00").ID)
	}
	f.tamper(t, `UPDATE wallets SET balance_minor = 0, version = version WHERE id = $1`, ids[2])
	m := &recordingMetrics{}
	rec := f.reconciler(m)
	checked := 0
	for {
		n, err := rec.Sweep(context.Background(), 2)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		checked += n
	}
	if checked != 5 || m.divergent != 1 {
		t.Fatalf("checked=%d divergent=%d", checked, m.divergent)
	}
}
