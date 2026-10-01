// Package wagering models wager transactions: external operations from game
// providers (BET, WIN, LOSS, REFUND, ROLLBACK) and the internal OPENING credit,
// their state machine and the business rules that decide each outcome.
package wagering

import (
	"errors"
	"fmt"
)

// Errors are classifiable with errors.Is / errors.As.
var (
	ErrInvalidRequest     = errors.New("wagering: invalid request")
	ErrReservedKind       = errors.New("wagering: kind reserved for internal use")
	ErrInvalidTransition  = errors.New("wagering: invalid state transition")
	ErrInvalidTransaction = errors.New("wagering: invalid transaction")
)

// Kind of a wager transaction.
type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

// ParseKind accepts any kind (used when rehydrating).
func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return k, nil
	default:
		return "", fmt.Errorf("%w: kind %q", ErrInvalidTransaction, s)
	}
}

// ParseExternalKind accepts only kinds a provider may send; OPENING is rejected.
func ParseExternalKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return k, nil
	case KindOpening:
		return "", ErrReservedKind
	default:
		return "", fieldError("kind", fmt.Sprintf("unknown kind %q", s))
	}
}

// IsReversal reports REFUND and ROLLBACK.
func (k Kind) IsReversal() bool { return k == KindRefund || k == KindRollback }

// Status of a wager transaction.
type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

// ParseStatus accepts any status.
func ParseStatus(s string) (Status, error) {
	switch st := Status(s); st {
	case StatusPending, StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed:
		return st, nil
	default:
		return "", fmt.Errorf("%w: status %q", ErrInvalidTransaction, s)
	}
}

// IsTerminal reports PROCESSED, REJECTED and FAILED.
func (s Status) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

// Origin distinguishes provider operations from internal ones.
type Origin string

const (
	OriginExternal Origin = "EXTERNAL"
	OriginInternal Origin = "INTERNAL"
)

// FailureCode is the stable, documented reason of a definitive outcome
// (REJECTED or FAILED). Correctable input errors are not persisted and use
// ValidationError instead.
type FailureCode string

const (
	FailureInsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	FailureReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	FailureCurrencyMismatch          FailureCode = "CURRENCY_MISMATCH"
	FailureWalletPlayerMismatch      FailureCode = "WALLET_PLAYER_MISMATCH"
	FailureReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	FailureReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	FailureReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	FailureReferenceKindInvalid      FailureCode = "REFERENCE_KIND_NOT_REVERSIBLE"
	FailureReversalAmountMismatch    FailureCode = "REVERSAL_AMOUNT_MISMATCH"
	FailureAlreadyReversed           FailureCode = "ALREADY_REVERSED"
	FailureInfrastructure            FailureCode = "INFRASTRUCTURE_FAILURE"
)

// ParseFailureCode accepts any known code (used when rehydrating).
func ParseFailureCode(s string) (FailureCode, error) {
	switch c := FailureCode(s); c {
	case FailureInsufficientFunds, FailureReversalInsufficientFunds, FailureCurrencyMismatch,
		FailureWalletPlayerMismatch, FailureReferenceNotFound, FailureReferenceNotProcessed,
		FailureReferenceMismatch, FailureReferenceKindInvalid, FailureReversalAmountMismatch,
		FailureAlreadyReversed, FailureInfrastructure:
		return c, nil
	default:
		return "", fmt.Errorf("%w: failure code %q", ErrInvalidTransaction, s)
	}
}

// ValidationError is a correctable input problem on one field.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

// Unwrap lets callers match ErrInvalidRequest.
func (e *ValidationError) Unwrap() error { return ErrInvalidRequest }

func fieldError(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }
