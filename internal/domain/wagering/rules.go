package wagering

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

// Decision is the outcome of evaluating an open external transaction. It is a
// sealed sum type: Move | NoMove | Reject | AwaitReference.
type Decision interface{ isDecision() }

// Move applies Amount to the wallet in Direction. Reference is uuid.Nil when none.
type Move struct {
	Amount    money.Money
	Direction wallet.Direction
	Reference uuid.UUID
}

// NoMove concludes successfully without a balance change (LOSS).
type NoMove struct{}

// Reject concludes with a definitive business failure.
type Reject struct{ Code FailureCode }

// AwaitReference parks the transaction until its reference is processed.
type AwaitReference struct{}

func (Move) isDecision()           {}
func (NoMove) isDecision()         {}
func (Reject) isDecision()         {}
func (AwaitReference) isDecision() {}

// Reference is what the caller found for (providerId, referenceExternalTransactionId).
type Reference struct {
	Tx *Transaction // nil when it has not arrived yet
	// AlreadyReversed: the referenced transaction already has a PROCESSED REFUND or
	// ROLLBACK. A transaction is reversed successfully at most once, whatever the
	// reversal kind, so a BET can never be returned twice (REFUND + ROLLBACK).
	AlreadyReversed bool
}

// Evaluate applies the business rules. The wallet must be the transaction's
// wallet, locked by the caller; ref is consulted only when the request names a
// reference. Errors signal programming mistakes, never business outcomes.
func Evaluate(t *Transaction, w *wallet.Wallet, ref Reference) (Decision, error) {
	ext := t.External()
	if ext == nil || w == nil || w.ID() != t.WalletID() {
		return nil, fmt.Errorf("%w: evaluate needs an external transaction and its own wallet", ErrInvalidTransaction)
	}
	if w.Currency() != t.Money().Currency() {
		return Reject{FailureCurrencyMismatch}, nil
	}
	if w.PlayerID() != t.PlayerID() {
		return Reject{FailureWalletPlayerMismatch}, nil
	}

	var referenceID uuid.UUID
	if ext.ReferenceExternalTransactionID != "" {
		verdict, refTx := checkReference(t, ref)
		if verdict != nil {
			return verdict, nil
		}
		referenceID = refTx.ID()
	}

	switch t.Kind() {
	case KindBet:
		return debit(w, t.Money(), uuid.Nil, FailureInsufficientFunds), nil
	case KindWin, KindRefund:
		return Move{Amount: t.Money(), Direction: wallet.Credit, Reference: referenceID}, nil
	case KindLoss:
		return NoMove{}, nil
	case KindRollback:
		direction := wallet.Debit
		if ref.Tx.Kind() == KindBet {
			direction = wallet.Credit
		}
		if direction == wallet.Debit {
			return debit(w, t.Money(), referenceID, FailureReversalInsufficientFunds), nil
		}
		return Move{Amount: t.Money(), Direction: direction, Reference: referenceID}, nil
	case KindOpening:
		return nil, fmt.Errorf("%w: OPENING is not evaluated", ErrInvalidTransaction)
	default:
		return nil, fmt.Errorf("%w: kind %q", ErrInvalidTransaction, t.Kind())
	}
}

func debit(w *wallet.Wallet, amount money.Money, reference uuid.UUID, shortage FailureCode) Decision {
	if !w.CanDebit(amount) {
		return Reject{shortage}
	}
	return Move{Amount: amount, Direction: wallet.Debit, Reference: reference}
}

// reversible lists which kinds each referencing kind may point to.
var reversible = map[Kind][]Kind{
	KindWin:      {KindBet},
	KindRefund:   {KindBet},
	KindRollback: {KindBet, KindWin, KindRefund},
}

// checkReference returns a non-nil verdict when evaluation must stop (wait or
// reject), otherwise the resolved referenced transaction.
func checkReference(t *Transaction, ref Reference) (Decision, *Transaction) {
	r := ref.Tx
	if r == nil || r.Status() == StatusPending || r.Status() == StatusPendingReference {
		return AwaitReference{}, nil
	}
	if r.Status() != StatusProcessed {
		return Reject{FailureReferenceNotProcessed}, nil
	}
	allowed := false
	for _, k := range reversible[t.Kind()] {
		allowed = allowed || r.Kind() == k
	}
	if !allowed {
		return Reject{FailureReferenceKindInvalid}, nil
	}
	re, te := r.External(), t.External()
	if re == nil || re.ProviderID != te.ProviderID || re.RoundID != te.RoundID ||
		r.PlayerID() != t.PlayerID() || r.WalletID() != t.WalletID() ||
		r.Money().Currency() != t.Money().Currency() {
		return Reject{FailureReferenceMismatch}, nil
	}
	if t.Kind().IsReversal() {
		if !r.Money().Equal(t.Money()) {
			return Reject{FailureReversalAmountMismatch}, nil
		}
		if ref.AlreadyReversed {
			return Reject{FailureAlreadyReversed}, nil
		}
	}
	return nil, r
}
