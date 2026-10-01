package wagering_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

var (
	t0       = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	playerID = uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	walletID = uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
)

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// raw returns a valid BET request; tests mutate copies.
func raw(t *testing.T) wagering.RawRequest {
	t.Helper()
	return wagering.RawRequest{
		ProviderID: "provider-a", ExternalTransactionID: "transaction-123", IdempotencyKey: "provider-a:transaction-123",
		PlayerID: playerID.String(), WalletID: walletID.String(), RoundID: "round-987", GameID: "fortune-chimp",
		Kind: "BET", Money: brl(t, "25.00"),
	}
}

func request(t *testing.T, mutate func(*wagering.RawRequest)) wagering.ExternalRequest {
	t.Helper()
	r := raw(t)
	if mutate != nil {
		mutate(&r)
	}
	req, err := wagering.NewExternalRequest(r)
	if err != nil {
		t.Fatalf("NewExternalRequest: %v", err)
	}
	return req
}

func pendingTx(t *testing.T, mutate func(*wagering.RawRequest)) *wagering.Transaction {
	t.Helper()
	tx, err := wagering.NewExternal(uuid.New(), request(t, mutate), t0)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// processedTx builds a PROCESSED referenced transaction of the given kind/amount.
func processedTx(t *testing.T, kind, amount, extID string) *wagering.Transaction {
	t.Helper()
	tx := pendingTx(t, func(r *wagering.RawRequest) {
		r.Kind, r.Money, r.ExternalTransactionID, r.IdempotencyKey = kind, brl(t, amount), extID, "provider-a:"+extID
		if kind == "REFUND" || kind == "ROLLBACK" {
			r.ReferenceExternalTransactionID = "some-bet"
		}
	})
	if err := tx.Process(wagering.Result{Balance: brl(t, "0.00"), WalletVersion: 2}, uuid.Nil, t0); err != nil {
		t.Fatal(err)
	}
	return tx
}

func walletWith(t *testing.T, balance string) *wallet.Wallet {
	t.Helper()
	w, err := wallet.Rehydrate(wallet.Snapshot{ID: walletID, PlayerID: playerID, Balance: brl(t, balance), Version: 1, CreatedAt: t0, UpdatedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	return w
}
