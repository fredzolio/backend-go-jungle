package wagering_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

func TestNewExternal_starts_pending_with_external_metadata(t *testing.T) {
	tx := pendingTx(t, nil)
	ext := tx.External()
	if tx.Status() != wagering.StatusPending || ext == nil || ext.PayloadHash == "" || ext.IdempotencyKey != "provider-a:transaction-123" {
		t.Fatalf("unexpected: status=%s ext=%+v", tx.Status(), ext)
	}
}

func TestNewOpening_is_internal_without_provider_metadata(t *testing.T) {
	tx, err := wagering.NewOpening(uuid.New(), walletID, playerID, brl(t, "1000.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	s := tx.Snapshot()
	if s.Origin != wagering.OriginInternal || s.Kind != wagering.KindOpening || s.External != nil {
		t.Fatalf("unexpected opening: %+v", s)
	}
	if _, err := wagering.NewOpening(uuid.New(), walletID, playerID, brl(t, "0.00"), t0); !errors.Is(err, wagering.ErrInvalidTransaction) {
		t.Fatalf("zero opening: %v", err)
	}
}

func TestTransitions_from_open_states(t *testing.T) {
	result := wagering.Result{Balance: brl(t, "975.00"), WalletVersion: 2}
	at := t0.Add(time.Second)
	cases := map[string]func(*wagering.Transaction) error{
		"process": func(tx *wagering.Transaction) error { return tx.Process(result, uuid.Nil, at) },
		"reject":  func(tx *wagering.Transaction) error { return tx.Reject(wagering.FailureInsufficientFunds, &result, at) },
		"fail":    func(tx *wagering.Transaction) error { return tx.Fail(at) },
		"await":   func(tx *wagering.Transaction) error { return tx.AwaitReference(at, at.Add(time.Minute), at) },
	}
	for name, transition := range cases {
		if err := transition(pendingTx(t, nil)); err != nil {
			t.Fatalf("PENDING %s: %v", name, err)
		}
		parked := pendingTx(t, nil)
		if err := parked.AwaitReference(at, at.Add(time.Minute), at); err != nil {
			t.Fatal(err)
		}
		if err := transition(parked); err != nil {
			t.Fatalf("PENDING_REFERENCE %s: %v", name, err)
		}
	}
}

func TestTerminal_states_accept_no_transition(t *testing.T) {
	result := wagering.Result{Balance: brl(t, "975.00"), WalletVersion: 2}
	terminals := map[string]func(*wagering.Transaction) error{
		"processed": func(tx *wagering.Transaction) error { return tx.Process(result, uuid.Nil, t0) },
		"rejected":  func(tx *wagering.Transaction) error { return tx.Reject(wagering.FailureInsufficientFunds, &result, t0) },
		"failed":    func(tx *wagering.Transaction) error { return tx.Fail(t0) },
	}
	for name, toTerminal := range terminals {
		tx := pendingTx(t, nil)
		if err := toTerminal(tx); err != nil {
			t.Fatal(err)
		}
		before := tx.Snapshot()
		for _, err := range []error{
			tx.Process(result, uuid.Nil, t0),
			tx.Reject(wagering.FailureInsufficientFunds, &result, t0),
			tx.Fail(t0),
			tx.AwaitReference(t0, t0, t0),
		} {
			if !errors.Is(err, wagering.ErrInvalidTransition) {
				t.Fatalf("%s accepted a transition: %v", name, err)
			}
		}
		if tx.Snapshot().Status != before.Status || tx.Snapshot().FailureCode != before.FailureCode {
			t.Fatalf("%s state changed", name)
		}
	}
}

func TestReject_requires_a_business_code(t *testing.T) {
	if err := pendingTx(t, nil).Reject(wagering.FailureInfrastructure, nil, t0); !errors.Is(err, wagering.ErrInvalidTransaction) {
		t.Fatalf("err = %v", err)
	}
}

func TestAwaitReference_keeps_first_deadline_and_counts_attempts(t *testing.T) {
	tx := pendingTx(t, nil)
	deadline := t0.Add(10 * time.Minute)
	if err := tx.AwaitReference(t0.Add(time.Second), deadline, t0); err != nil {
		t.Fatal(err)
	}
	if err := tx.AwaitReference(t0.Add(3*time.Second), t0.Add(time.Hour), t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	s := tx.Snapshot()
	if s.Attempts != 2 || !s.ReferenceDeadline.Equal(deadline) || !s.NextAttemptAt.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("snapshot = %+v", s)
	}
	if tx.ReferenceExpired(deadline.Add(-time.Nanosecond)) || !tx.ReferenceExpired(deadline) {
		t.Fatal("expiry boundary wrong")
	}
}

func TestRehydrate_round_trips_without_transitions_and_rejects_inconsistency(t *testing.T) {
	tx := pendingTx(t, nil)
	if err := tx.Process(wagering.Result{Balance: brl(t, "975.00"), WalletVersion: 2}, uuid.Nil, t0); err != nil {
		t.Fatal(err)
	}
	s := tx.Snapshot()
	back, err := wagering.Rehydrate(s)
	if err != nil || back.Status() != wagering.StatusProcessed || back.Result().Balance.Amount() != "975.00" {
		t.Fatalf("rehydrate: %v %+v", err, back)
	}
	for name, mutate := range map[string]func(*wagering.Snapshot){
		"processed without result":   func(s *wagering.Snapshot) { s.Result = nil },
		"rejected without code":      func(s *wagering.Snapshot) { s.Status = wagering.StatusRejected },
		"opening with external meta": func(s *wagering.Snapshot) { s.Kind = wagering.KindOpening },
		"external without metadata":  func(s *wagering.Snapshot) { s.External = nil },
		"terminal without timestamp": func(s *wagering.Snapshot) { s.ProcessedAt = time.Time{} },
		"unknown status":             func(s *wagering.Snapshot) { s.Status = "DONE" },
	} {
		c := s
		mutate(&c)
		if _, err := wagering.Rehydrate(c); !errors.Is(err, wagering.ErrInvalidTransaction) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}
