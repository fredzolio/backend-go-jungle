package events_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/events"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

var at = time.Date(2026, 9, 8, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))

func meta() events.Meta {
	return events.Meta{EventID: uuid.New(), OccurredAt: at, CorrelationID: "corr-1"}
}

func opening(t *testing.T) (*wagering.Transaction, wallet.LedgerEntry) {
	t.Helper()
	amount, _ := money.Parse("1000.00", "BRL")
	w, entry, err := wallet.Open(wallet.Opening{ID: uuid.New(), PlayerID: uuid.New(), InitialBalance: amount, OpeningTransaction: uuid.New(), OpeningEntry: uuid.New(), At: at})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := wagering.NewOpening(entry.Data().TransactionID, w.ID(), w.PlayerID(), amount, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Process(wagering.Result{Balance: w.Balance(), WalletVersion: w.Version()}, uuid.Nil, at); err != nil {
		t.Fatal(err)
	}
	return tx, *entry
}

func keys(t *testing.T, v any) (map[string]any, []string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return m, ks
}

func TestWalletBalanceChanged_has_the_contract_shape(t *testing.T) {
	_, entry := opening(t)
	ev, err := events.NewWalletBalanceChanged(meta(), entry)
	if err != nil {
		t.Fatal(err)
	}
	env, envKeys := keys(t, ev)
	if want := []string{"aggregateId", "correlationId", "data", "eventId", "eventType", "occurredAt", "version"}; !slices.Equal(envKeys, want) {
		t.Fatalf("envelope keys = %v", envKeys)
	}
	if env["eventType"] != "WalletBalanceChanged" || env["version"] != float64(1) || env["occurredAt"] != "2026-09-08T15:00:00.000Z" {
		t.Fatalf("envelope = %v", env)
	}
	_, dataKeys := keys(t, ev.Data)
	if want := []string{"balanceAfter", "balanceBefore", "direction", "money", "transactionId", "walletId", "walletVersion"}; !slices.Equal(dataKeys, want) {
		t.Fatalf("data keys = %v", dataKeys)
	}
	data := env["data"].(map[string]any)
	if m := data["money"].(map[string]any); m["amount"] != "1000.00" || m["currency"] != "BRL" {
		t.Fatalf("money must be decimal strings: %v", m)
	}
}

func TestOpening_processed_event_omits_provider_metadata(t *testing.T) {
	tx, _ := opening(t)
	ev, err := events.NewTransactionProcessed(meta(), tx)
	if err != nil {
		t.Fatal(err)
	}
	_, dataKeys := keys(t, ev.Data)
	for _, k := range dataKeys {
		if k == "providerId" || k == "externalTransactionId" || k == "roundId" {
			t.Fatalf("internal event leaked provider field %q", k)
		}
	}
	if ev.EventType != events.TypeWagerTransactionProcessed || ev.Data.Balance.Amount() != "1000.00" || ev.Data.WalletVersion != 1 {
		t.Fatalf("event = %+v", ev)
	}
}

func TestConstructors_require_matching_status_and_meta(t *testing.T) {
	tx, _ := opening(t)
	if _, err := events.NewTransactionRejected(meta(), tx); !errors.Is(err, events.ErrInvalidEvent) {
		t.Fatalf("rejected from processed: %v", err)
	}
	if _, err := events.NewTransactionPendingReference(meta(), tx); !errors.Is(err, events.ErrInvalidEvent) {
		t.Fatalf("pending from processed: %v", err)
	}
	if _, err := events.NewTransactionProcessed(events.Meta{OccurredAt: at}, tx); !errors.Is(err, events.ErrInvalidEvent) {
		t.Fatalf("missing meta: %v", err)
	}
}
