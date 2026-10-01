// Package events defines the integration events published through the
// transactional outbox. Constructors fix the event type and version; payloads are
// immutable snapshots taken when the originating transaction commits.
package events

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

// ErrInvalidEvent signals a constructor called with an inconsistent source.
var ErrInvalidEvent = errors.New("events: invalid event source")

// Event types (stable contract).
const (
	TypeWagerTransactionProcessed        = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         = "WagerTransactionRejected"
	TypeWagerTransactionPendingReference = "WagerTransactionPendingReference"
	TypeWalletBalanceChanged             = "WalletBalanceChanged"
	version                              = 1
)

// Timestamp marshals as RFC 3339 in UTC with millisecond precision.
type Timestamp time.Time

// MarshalJSON renders e.g. "2026-09-08T12:00:00.000Z".
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(t).UTC().Format("2006-01-02T15:04:05.000Z07:00") + `"`), nil
}

// Meta carries the identity and causality supplied by the caller.
type Meta struct {
	OccurredAt    time.Time
	CorrelationID string
	CausationID   string // optional
	EventID       uuid.UUID
}

// Envelope is the published shape of every event.
type Envelope[T any] struct {
	Data          T         `json:"data"`
	CausationID   *string   `json:"causationId,omitempty"`
	OccurredAt    Timestamp `json:"occurredAt"`
	EventType     string    `json:"eventType"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	Version       int       `json:"version"`
	EventID       uuid.UUID `json:"eventId"`
}

func envelope[T any](m Meta, eventType, aggregateID string, data T) (Envelope[T], error) {
	if m.EventID == uuid.Nil || m.OccurredAt.IsZero() || m.CorrelationID == "" {
		return Envelope[T]{}, fmt.Errorf("%w: meta requires eventId, occurredAt and correlationId", ErrInvalidEvent)
	}
	var causation *string
	if m.CausationID != "" {
		causation = &m.CausationID
	}
	return Envelope[T]{
		EventID: m.EventID, EventType: eventType, AggregateID: aggregateID, CorrelationID: m.CorrelationID,
		CausationID: causation, OccurredAt: Timestamp(m.OccurredAt), Version: version, Data: data,
	}, nil
}

// TransactionData is common to all wager transaction events. Provider fields are
// omitted for the internal OPENING.
type TransactionData struct {
	Money                          money.Money     `json:"money"`
	Kind                           wagering.Kind   `json:"kind"`
	Origin                         wagering.Origin `json:"origin"`
	ProviderID                     string          `json:"providerId,omitempty"`
	ExternalTransactionID          string          `json:"externalTransactionId,omitempty"`
	RoundID                        string          `json:"roundId,omitempty"`
	GameID                         string          `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string          `json:"referenceExternalTransactionId,omitempty"`
	Status                         wagering.Status `json:"status"`
	TransactionID                  uuid.UUID       `json:"transactionId"`
	WalletID                       uuid.UUID       `json:"walletId"`
	PlayerID                       uuid.UUID       `json:"playerId"`
}

func transactionData(tx *wagering.Transaction) TransactionData {
	s := tx.Snapshot()
	d := TransactionData{
		TransactionID: s.ID, Origin: s.Origin, Kind: s.Kind, Status: s.Status,
		WalletID: s.WalletID, PlayerID: s.PlayerID, Money: s.Money,
	}
	if e := s.External; e != nil {
		d.ProviderID, d.ExternalTransactionID, d.RoundID, d.GameID = e.ProviderID, e.ExternalTransactionID, e.RoundID, e.GameID
		d.ReferenceExternalTransactionID = e.ReferenceExternalTransactionID
	}
	return d
}

func requireStatus(tx *wagering.Transaction, want wagering.Status) error {
	if tx == nil || tx.Status() != want {
		return fmt.Errorf("%w: transaction must be %s", ErrInvalidEvent, want)
	}
	return nil
}

// WagerTransactionProcessed: successful conclusion, including LOSS and OPENING.
type WagerTransactionProcessed struct {
	Balance money.Money `json:"balance"`
	TransactionData
	WalletVersion int64 `json:"walletVersion"`
}

// NewTransactionProcessed builds the event from a PROCESSED transaction.
func NewTransactionProcessed(m Meta, tx *wagering.Transaction) (Envelope[WagerTransactionProcessed], error) {
	if err := requireStatus(tx, wagering.StatusProcessed); err != nil {
		return Envelope[WagerTransactionProcessed]{}, err
	}
	r := tx.Result()
	return envelope(m, TypeWagerTransactionProcessed, tx.ID().String(),
		WagerTransactionProcessed{TransactionData: transactionData(tx), Balance: r.Balance, WalletVersion: r.WalletVersion})
}

// WagerTransactionRejected: definitive business rejection.
type WagerTransactionRejected struct {
	FailureCode wagering.FailureCode `json:"failureCode"`
	TransactionData
}

// NewTransactionRejected builds the event from a REJECTED transaction.
func NewTransactionRejected(m Meta, tx *wagering.Transaction) (Envelope[WagerTransactionRejected], error) {
	if err := requireStatus(tx, wagering.StatusRejected); err != nil {
		return Envelope[WagerTransactionRejected]{}, err
	}
	return envelope(m, TypeWagerTransactionRejected, tx.ID().String(),
		WagerTransactionRejected{TransactionData: transactionData(tx), FailureCode: tx.Failure()})
}

// WagerTransactionPendingReference: the operation waits for its reference.
type WagerTransactionPendingReference struct {
	NextAttemptAt     Timestamp `json:"nextAttemptAt"`
	ReferenceDeadline Timestamp `json:"referenceDeadline"`
	TransactionData
}

// NewTransactionPendingReference builds the event from a PENDING_REFERENCE transaction.
func NewTransactionPendingReference(m Meta, tx *wagering.Transaction) (Envelope[WagerTransactionPendingReference], error) {
	if err := requireStatus(tx, wagering.StatusPendingReference); err != nil {
		return Envelope[WagerTransactionPendingReference]{}, err
	}
	s := tx.Snapshot()
	return envelope(m, TypeWagerTransactionPendingReference, tx.ID().String(), WagerTransactionPendingReference{
		TransactionData: transactionData(tx), NextAttemptAt: Timestamp(s.NextAttemptAt), ReferenceDeadline: Timestamp(s.ReferenceDeadline),
	})
}

// WalletBalanceChanged: an effective balance change (one ledger entry).
type WalletBalanceChanged struct {
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	Direction     wallet.Direction `json:"direction"`
	WalletVersion int64            `json:"walletVersion"`
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
}

// NewWalletBalanceChanged builds the event from the committed ledger entry.
func NewWalletBalanceChanged(m Meta, entry wallet.LedgerEntry) (Envelope[WalletBalanceChanged], error) {
	d := entry.Data()
	if d.ID == uuid.Nil {
		return Envelope[WalletBalanceChanged]{}, fmt.Errorf("%w: empty ledger entry", ErrInvalidEvent)
	}
	return envelope(m, TypeWalletBalanceChanged, d.WalletID.String(), WalletBalanceChanged{
		WalletID: d.WalletID, TransactionID: d.TransactionID, Direction: d.Direction, Money: d.Amount,
		BalanceBefore: d.BalanceBefore, BalanceAfter: d.BalanceAfter, WalletVersion: d.WalletVersion,
	})
}
