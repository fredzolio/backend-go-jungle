// Package wallet holds the Wallet aggregate root and its ledger entries. Balance
// changes only happen through Debit/Credit, which return the matching LedgerEntry
// that must be committed together with the new balance.
package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
)

// Errors are classifiable with errors.Is.
var (
	ErrInvalidWallet      = errors.New("wallet: invalid wallet")
	ErrInvalidLedgerEntry = errors.New("wallet: invalid ledger entry")
	ErrInsufficientFunds  = errors.New("wallet: insufficient funds")
	ErrCurrencyMismatch   = errors.New("wallet: currency mismatch")
	ErrInvalidMovement    = errors.New("wallet: invalid movement")
)

// Wallet is the financial aggregate root. (playerID, currency) is unique.
type Wallet struct {
	createdAt time.Time
	updatedAt time.Time
	balance   money.Money
	version   int64
	id        uuid.UUID
	playerID  uuid.UUID
}

// Snapshot is the full persisted state of a wallet.
type Snapshot struct {
	CreatedAt time.Time
	UpdatedAt time.Time
	Balance   money.Money
	Version   int64
	ID        uuid.UUID
	PlayerID  uuid.UUID
}

// Opening describes a new wallet. A positive InitialBalance produces the OPENING
// ledger entry; the wallet version is 1 either way.
type Opening struct {
	At                 time.Time
	InitialBalance     money.Money
	ID                 uuid.UUID
	PlayerID           uuid.UUID
	OpeningTransaction uuid.UUID
	OpeningEntry       uuid.UUID
}

// Open creates a wallet. The returned entry is nil when the initial balance is zero.
func Open(o Opening) (*Wallet, *LedgerEntry, error) {
	if o.ID == uuid.Nil || o.PlayerID == uuid.Nil || o.At.IsZero() {
		return nil, nil, fmt.Errorf("%w: missing identity or timestamp", ErrInvalidWallet)
	}
	if !o.InitialBalance.IsValid() || o.InitialBalance.IsNegative() {
		return nil, nil, fmt.Errorf("%w: initial balance must be zero or positive", ErrInvalidWallet)
	}
	zero, err := money.Zero(o.InitialBalance.Currency())
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidWallet, err)
	}
	w := &Wallet{id: o.ID, playerID: o.PlayerID, balance: o.InitialBalance, version: 1, createdAt: o.At, updatedAt: o.At}
	if o.InitialBalance.IsZero() {
		return w, nil, nil
	}
	entry, err := NewLedgerEntry(LedgerEntryData{
		ID: o.OpeningEntry, WalletID: o.ID, TransactionID: o.OpeningTransaction, Direction: Credit,
		Amount: o.InitialBalance, BalanceBefore: zero, BalanceAfter: o.InitialBalance,
		WalletVersion: 1, CreatedAt: o.At,
	})
	if err != nil {
		return nil, nil, err
	}
	return w, &entry, nil
}

// Rehydrate rebuilds a persisted wallet without applying any movement.
func Rehydrate(s Snapshot) (*Wallet, error) {
	switch {
	case s.ID == uuid.Nil || s.PlayerID == uuid.Nil:
		return nil, fmt.Errorf("%w: missing identity", ErrInvalidWallet)
	case !s.Balance.IsValid() || s.Balance.IsNegative():
		return nil, fmt.Errorf("%w: balance must be a non-negative valid amount", ErrInvalidWallet)
	case s.Version < 1:
		return nil, fmt.Errorf("%w: version must be >= 1", ErrInvalidWallet)
	case s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt):
		return nil, fmt.Errorf("%w: invalid timestamps", ErrInvalidWallet)
	}
	return &Wallet{id: s.ID, playerID: s.PlayerID, balance: s.Balance, version: s.Version, createdAt: s.CreatedAt, updatedAt: s.UpdatedAt}, nil
}

// Movement identifies one balance change requested on the wallet.
type Movement struct {
	At            time.Time
	Amount        money.Money
	EntryID       uuid.UUID
	TransactionID uuid.UUID
}

// Debit subtracts Amount, keeping the balance >= 0.
func (w *Wallet) Debit(m Movement) (LedgerEntry, error) { return w.apply(Debit, m) }

// Credit adds Amount.
func (w *Wallet) Credit(m Movement) (LedgerEntry, error) { return w.apply(Credit, m) }

// Apply moves the balance in the given direction.
func (w *Wallet) Apply(d Direction, m Movement) (LedgerEntry, error) { return w.apply(d, m) }

func (w *Wallet) apply(d Direction, m Movement) (LedgerEntry, error) {
	if !m.Amount.IsValid() || !m.Amount.IsPositive() || m.At.IsZero() {
		return LedgerEntry{}, fmt.Errorf("%w: amount must be positive with a timestamp", ErrInvalidMovement)
	}
	if m.Amount.Currency() != w.balance.Currency() {
		return LedgerEntry{}, fmt.Errorf("%w: wallet %s, movement %s", ErrCurrencyMismatch, w.balance.Currency(), m.Amount.Currency())
	}
	var after money.Money
	var err error
	switch d {
	case Debit:
		after, err = w.balance.Sub(m.Amount)
		if err == nil && after.IsNegative() {
			return LedgerEntry{}, ErrInsufficientFunds
		}
	case Credit:
		after, err = w.balance.Add(m.Amount)
	default:
		return LedgerEntry{}, fmt.Errorf("%w: direction %q", ErrInvalidMovement, d)
	}
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("%w: %w", ErrInvalidMovement, err)
	}
	entry, err := NewLedgerEntry(LedgerEntryData{
		ID: m.EntryID, WalletID: w.id, TransactionID: m.TransactionID, Direction: d, Amount: m.Amount,
		BalanceBefore: w.balance, BalanceAfter: after, WalletVersion: w.version + 1, CreatedAt: m.At,
	})
	if err != nil {
		return LedgerEntry{}, err
	}
	w.balance, w.version, w.updatedAt = after, w.version+1, m.At
	return entry, nil
}

// CanDebit reports whether a debit of amount keeps the balance non-negative.
func (w *Wallet) CanDebit(amount money.Money) bool {
	cmp, err := w.balance.Cmp(amount)
	return err == nil && cmp >= 0
}

// Accessors.
func (w *Wallet) ID() uuid.UUID            { return w.id }
func (w *Wallet) PlayerID() uuid.UUID      { return w.playerID }
func (w *Wallet) Balance() money.Money     { return w.balance }
func (w *Wallet) Currency() money.Currency { return w.balance.Currency() }
func (w *Wallet) Version() int64           { return w.version }

// Snapshot exposes the wallet state for persistence.
func (w *Wallet) Snapshot() Snapshot {
	return Snapshot{ID: w.id, PlayerID: w.playerID, Balance: w.balance, Version: w.version, CreatedAt: w.createdAt, UpdatedAt: w.updatedAt}
}
