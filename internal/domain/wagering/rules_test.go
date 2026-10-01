package wagering_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

func reversalOf(t *testing.T, kind, amount string) *wagering.Transaction {
	t.Helper()
	return pendingTx(t, func(r *wagering.RawRequest) {
		r.Kind, r.Money, r.ExternalTransactionID, r.IdempotencyKey = kind, brl(t, amount), "rev-1", "provider-a:rev-1"
		r.ReferenceExternalTransactionID = "ref-1"
	})
}

func evaluate(t *testing.T, tx *wagering.Transaction, w *wallet.Wallet, ref wagering.Reference) wagering.Decision {
	t.Helper()
	d, err := wagering.Evaluate(tx, w, ref)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return d
}

func wantReject(t *testing.T, d wagering.Decision, code wagering.FailureCode) {
	t.Helper()
	if r, ok := d.(wagering.Reject); !ok || r.Code != code {
		t.Fatalf("decision = %#v, want Reject(%s)", d, code)
	}
}

func wantMove(t *testing.T, d wagering.Decision, dir wallet.Direction, amount string) {
	t.Helper()
	m, ok := d.(wagering.Move)
	if !ok || m.Direction != dir || m.Amount.Amount() != amount {
		t.Fatalf("decision = %#v, want Move(%s %s)", d, dir, amount)
	}
}

func TestBet_debits_when_funds_suffice(t *testing.T) {
	wantMove(t, evaluate(t, pendingTx(t, nil), walletWith(t, "25.00"), wagering.Reference{}), wallet.Debit, "25.00")
}

func TestBet_rejects_insufficient_funds(t *testing.T) {
	wantReject(t, evaluate(t, pendingTx(t, nil), walletWith(t, "24.99"), wagering.Reference{}), wagering.FailureInsufficientFunds)
}

func TestWin_without_reference_credits(t *testing.T) {
	tx := pendingTx(t, func(r *wagering.RawRequest) { r.Kind = "WIN" })
	wantMove(t, evaluate(t, tx, walletWith(t, "0.00"), wagering.Reference{}), wallet.Credit, "25.00")
}

func TestWin_with_reference_requires_a_processed_bet_of_the_same_round(t *testing.T) {
	win := pendingTx(t, func(r *wagering.RawRequest) { r.Kind, r.ReferenceExternalTransactionID = "WIN", "bet-1" })
	w := walletWith(t, "0.00")
	wantMove(t, evaluate(t, win, w, wagering.Reference{Tx: processedTx(t, "BET", "10.00", "bet-1")}), wallet.Credit, "25.00")
	if _, ok := evaluate(t, win, w, wagering.Reference{}).(wagering.AwaitReference); !ok {
		t.Fatal("missing reference must wait")
	}
	wantReject(t, evaluate(t, win, w, wagering.Reference{Tx: processedTx(t, "WIN", "10.00", "win-0")}), wagering.FailureReferenceKindInvalid)
}

func TestLoss_concludes_without_movement(t *testing.T) {
	loss := pendingTx(t, func(r *wagering.RawRequest) { r.Kind, r.Money = "LOSS", brl(t, "0.00") })
	if _, ok := evaluate(t, loss, walletWith(t, "0.00"), wagering.Reference{}).(wagering.NoMove); !ok {
		t.Fatal("LOSS must not move the balance")
	}
}

func TestWallet_mismatches_are_rejected_before_anything_else(t *testing.T) {
	tx := pendingTx(t, nil)
	usd, _ := money.Parse("100.00", "USD")
	usdWallet, _ := wallet.Rehydrate(wallet.Snapshot{ID: walletID, PlayerID: playerID, Balance: usd, Version: 1, CreatedAt: t0, UpdatedAt: t0})
	wantReject(t, evaluate(t, tx, usdWallet, wagering.Reference{}), wagering.FailureCurrencyMismatch)
	other, _ := wallet.Rehydrate(wallet.Snapshot{ID: walletID, PlayerID: uuid.New(), Balance: brl(t, "100.00"), Version: 1, CreatedAt: t0, UpdatedAt: t0})
	wantReject(t, evaluate(t, tx, other, wagering.Reference{}), wagering.FailureWalletPlayerMismatch)
}

func TestRefund_credits_the_full_bet(t *testing.T) {
	wantMove(t, evaluate(t, reversalOf(t, "REFUND", "25.00"), walletWith(t, "0.00"),
		wagering.Reference{Tx: processedTx(t, "BET", "25.00", "ref-1")}), wallet.Credit, "25.00")
}

func TestRollback_direction_is_opposite_to_the_reference(t *testing.T) {
	w := walletWith(t, "100.00")
	cases := map[string]wallet.Direction{"BET": wallet.Credit, "WIN": wallet.Debit, "REFUND": wallet.Debit}
	for kind, dir := range cases {
		wantMove(t, evaluate(t, reversalOf(t, "ROLLBACK", "25.00"), w, wagering.Reference{Tx: processedTx(t, kind, "25.00", "ref-1")}), dir, "25.00")
	}
}

func TestRollback_debit_beyond_balance_uses_a_distinct_code(t *testing.T) {
	d := evaluate(t, reversalOf(t, "ROLLBACK", "25.00"), walletWith(t, "10.00"), wagering.Reference{Tx: processedTx(t, "WIN", "25.00", "ref-1")})
	wantReject(t, d, wagering.FailureReversalInsufficientFunds)
}

func TestReversal_reference_rules(t *testing.T) {
	w := walletWith(t, "100.00")
	rejected := pendingTx(t, func(r *wagering.RawRequest) { r.ExternalTransactionID, r.IdempotencyKey = "ref-1", "provider-a:ref-1" })
	if err := rejected.Reject(wagering.FailureInsufficientFunds, nil, t0); err != nil {
		t.Fatal(err)
	}
	parked := pendingTx(t, func(r *wagering.RawRequest) { r.ExternalTransactionID, r.IdempotencyKey = "ref-1", "provider-a:ref-1" })
	if err := parked.AwaitReference(t0, t0.Add(time.Minute), t0); err != nil {
		t.Fatal(err)
	}
	otherRound := processedTx(t, "BET", "25.00", "ref-1")
	snap := otherRound.Snapshot()
	ext := *snap.External
	ext.RoundID = "round-other"
	snap.External = &ext
	otherRound, _ = wagering.Rehydrate(snap)

	cases := []struct {
		ref  wagering.Reference
		want wagering.Decision
		name string
		kind string
	}{
		{name: "not arrived", kind: "REFUND", ref: wagering.Reference{}, want: wagering.AwaitReference{}},
		{name: "still pending", kind: "REFUND", ref: wagering.Reference{Tx: parked}, want: wagering.AwaitReference{}},
		{name: "rejected reference", kind: "REFUND", ref: wagering.Reference{Tx: rejected}, want: wagering.Reject{Code: wagering.FailureReferenceNotProcessed}},
		{name: "refund of a win", kind: "REFUND", ref: wagering.Reference{Tx: processedTx(t, "WIN", "25.00", "ref-1")}, want: wagering.Reject{Code: wagering.FailureReferenceKindInvalid}},
		{name: "rollback of rollback", kind: "ROLLBACK", ref: wagering.Reference{Tx: processedTx(t, "ROLLBACK", "25.00", "ref-1")}, want: wagering.Reject{Code: wagering.FailureReferenceKindInvalid}},
		{name: "partial refund", kind: "REFUND", ref: wagering.Reference{Tx: processedTx(t, "BET", "30.00", "ref-1")}, want: wagering.Reject{Code: wagering.FailureReversalAmountMismatch}},
		{name: "different round", kind: "REFUND", ref: wagering.Reference{Tx: otherRound}, want: wagering.Reject{Code: wagering.FailureReferenceMismatch}},
		{name: "refund after rollback (any prior reversal)", kind: "REFUND", ref: wagering.Reference{Tx: processedTx(t, "BET", "25.00", "ref-1"), AlreadyReversed: true}, want: wagering.Reject{Code: wagering.FailureAlreadyReversed}},
		{name: "rollback after refund", kind: "ROLLBACK", ref: wagering.Reference{Tx: processedTx(t, "BET", "25.00", "ref-1"), AlreadyReversed: true}, want: wagering.Reject{Code: wagering.FailureAlreadyReversed}},
	}
	for _, tc := range cases {
		if got := evaluate(t, reversalOf(t, tc.kind, "25.00"), w, tc.ref); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: decision = %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

func TestEvaluate_rejects_programming_errors(t *testing.T) {
	opening, _ := wagering.NewOpening(uuid.New(), walletID, playerID, brl(t, "1.00"), t0)
	if _, err := wagering.Evaluate(opening, walletWith(t, "1.00"), wagering.Reference{}); err == nil {
		t.Fatal("OPENING must not be evaluated")
	}
	if _, err := wagering.Evaluate(pendingTx(t, nil), nil, wagering.Reference{}); err == nil {
		t.Fatal("nil wallet must error")
	}
}
