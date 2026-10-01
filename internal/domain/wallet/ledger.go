package wallet

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
)

// Direction of a ledger movement.
type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

// ParseDirection accepts DEBIT or CREDIT.
func ParseDirection(s string) (Direction, error) {
	switch d := Direction(s); d {
	case Debit, Credit:
		return d, nil
	default:
		return "", fmt.Errorf("%w: direction %q", ErrInvalidLedgerEntry, s)
	}
}

// Opposite returns the reversing direction.
func (d Direction) Opposite() Direction {
	if d == Debit {
		return Credit
	}
	return Debit
}

// LedgerEntry is an immutable, append-only record of one balance change.
type LedgerEntry struct {
	createdAt     time.Time
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	direction     Direction
	walletVersion int64
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
}

// LedgerEntryData is the full state of an entry (construction and rehydration).
type LedgerEntryData struct {
	CreatedAt     time.Time
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	Direction     Direction
	WalletVersion int64
	ID            uuid.UUID
	WalletID      uuid.UUID
	TransactionID uuid.UUID
}

// NewLedgerEntry validates balanceAfter = balanceBefore ± amount and the other
// invariants; it is also used to rehydrate persisted entries.
func NewLedgerEntry(d LedgerEntryData) (LedgerEntry, error) {
	if d.ID == uuid.Nil || d.WalletID == uuid.Nil || d.TransactionID == uuid.Nil {
		return LedgerEntry{}, fmt.Errorf("%w: missing identifier", ErrInvalidLedgerEntry)
	}
	if d.WalletVersion < 1 || d.CreatedAt.IsZero() {
		return LedgerEntry{}, fmt.Errorf("%w: invalid version or timestamp", ErrInvalidLedgerEntry)
	}
	if !d.Amount.IsPositive() {
		return LedgerEntry{}, fmt.Errorf("%w: amount must be positive", ErrInvalidLedgerEntry)
	}
	var expected money.Money
	var err error
	switch d.Direction {
	case Credit:
		expected, err = d.BalanceBefore.Add(d.Amount)
	case Debit:
		expected, err = d.BalanceBefore.Sub(d.Amount)
	default:
		return LedgerEntry{}, fmt.Errorf("%w: direction %q", ErrInvalidLedgerEntry, d.Direction)
	}
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("%w: %w", ErrInvalidLedgerEntry, err)
	}
	if !expected.Equal(d.BalanceAfter) || d.BalanceBefore.IsNegative() || d.BalanceAfter.IsNegative() {
		return LedgerEntry{}, fmt.Errorf("%w: balanceAfter %s inconsistent with %s %s %s",
			ErrInvalidLedgerEntry, d.BalanceAfter, d.BalanceBefore, d.Direction, d.Amount)
	}
	return LedgerEntry{
		id: d.ID, walletID: d.WalletID, transactionID: d.TransactionID, direction: d.Direction,
		amount: d.Amount, balanceBefore: d.BalanceBefore, balanceAfter: d.BalanceAfter,
		walletVersion: d.WalletVersion, createdAt: d.CreatedAt,
	}, nil
}

// Data exposes the entry state for persistence and events.
func (e LedgerEntry) Data() LedgerEntryData {
	return LedgerEntryData{
		ID: e.id, WalletID: e.walletID, TransactionID: e.transactionID, Direction: e.direction,
		Amount: e.amount, BalanceBefore: e.balanceBefore, BalanceAfter: e.balanceAfter,
		WalletVersion: e.walletVersion, CreatedAt: e.createdAt,
	}
}
