package wagering

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
)

// External holds the provider-facing identity of an EXTERNAL transaction.
type External struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string
}

// Result is the financial outcome returned to the provider and on every replay:
// the wallet balance and version observed when the transaction was concluded.
type Result struct {
	Balance       money.Money
	WalletVersion int64
}

// Snapshot is the full persisted state; it is also the rehydration input.
type Snapshot struct {
	CreatedAt              time.Time
	UpdatedAt              time.Time
	ProcessedAt            time.Time // zero until terminal
	NextAttemptAt          time.Time // PENDING_REFERENCE only
	ReferenceDeadline      time.Time // PENDING_REFERENCE only
	Money                  money.Money
	External               *External // nil for INTERNAL
	Result                 *Result   // set when PROCESSED or REJECTED
	Kind                   Kind
	Status                 Status
	Origin                 Origin
	FailureCode            FailureCode
	Attempts               int
	ID                     uuid.UUID
	WalletID               uuid.UUID
	PlayerID               uuid.UUID
	ReferenceTransactionID uuid.UUID // resolved reference; Nil when none
}

// Transaction is a wager transaction. State changes only through its methods.
type Transaction struct{ s Snapshot }

// NewExternal accepts a validated provider request in PENDING.
func NewExternal(id uuid.UUID, req ExternalRequest, at time.Time) (*Transaction, error) {
	if id == uuid.Nil || at.IsZero() {
		return nil, fmt.Errorf("%w: missing id or timestamp", ErrInvalidTransaction)
	}
	f := req.f
	if f.Kind == "" {
		return nil, fmt.Errorf("%w: request was not built by NewExternalRequest", ErrInvalidTransaction)
	}
	return &Transaction{s: Snapshot{
		ID: id, Origin: OriginExternal, Kind: f.Kind, Status: StatusPending,
		WalletID: f.WalletID, PlayerID: f.PlayerID, Money: f.Money, CreatedAt: at, UpdatedAt: at,
		External: &External{
			ProviderID: f.ProviderID, ExternalTransactionID: f.ExternalTransactionID, IdempotencyKey: f.IdempotencyKey,
			PayloadHash: req.PayloadHash(), RoundID: f.RoundID, GameID: f.GameID,
			ReferenceExternalTransactionID: f.ReferenceExternalTransactionID,
		},
	}}, nil
}

// NewOpening creates the internal OPENING credit of a wallet, in PENDING.
func NewOpening(id, walletID, playerID uuid.UUID, amount money.Money, at time.Time) (*Transaction, error) {
	if id == uuid.Nil || walletID == uuid.Nil || playerID == uuid.Nil || at.IsZero() || !amount.IsPositive() {
		return nil, fmt.Errorf("%w: opening needs ids, timestamp and a positive amount", ErrInvalidTransaction)
	}
	return &Transaction{s: Snapshot{
		ID: id, Origin: OriginInternal, Kind: KindOpening, Status: StatusPending,
		WalletID: walletID, PlayerID: playerID, Money: amount, CreatedAt: at, UpdatedAt: at,
	}}, nil
}

// Rehydrate rebuilds a persisted transaction, checking consistency only; it never
// applies transitions, movements or events.
func Rehydrate(s Snapshot) (*Transaction, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &Transaction{s: s}, nil
}

func (s Snapshot) validate() error {
	bad := func(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidTransaction, reason) }
	switch {
	case s.ID == uuid.Nil || s.WalletID == uuid.Nil || s.PlayerID == uuid.Nil:
		return bad("missing identifier")
	case !s.Money.IsValid() || s.Money.IsNegative():
		return bad("invalid money")
	case s.CreatedAt.IsZero():
		return bad("missing createdAt")
	case (s.Origin == OriginInternal) != (s.Kind == KindOpening):
		return bad("OPENING is exactly the internal origin")
	case (s.Origin == OriginExternal) != (s.External != nil):
		return bad("external metadata must match origin")
	case s.Status.IsTerminal() && s.ProcessedAt.IsZero():
		return bad("terminal transaction without processedAt")
	case (s.Status == StatusRejected || s.Status == StatusFailed) != (s.FailureCode != ""):
		return bad("failure code is required exactly for REJECTED/FAILED")
	case s.Status == StatusProcessed && s.Result == nil:
		return bad("processed transaction without result")
	case s.Status == StatusPendingReference && (s.NextAttemptAt.IsZero() || s.ReferenceDeadline.IsZero()):
		return bad("pending reference without schedule")
	}
	if _, err := ParseStatus(string(s.Status)); err != nil {
		return err
	}
	if _, err := ParseKind(string(s.Kind)); err != nil {
		return err
	}
	return nil
}

// Snapshot exposes the state for persistence and events.
func (t *Transaction) Snapshot() Snapshot { return t.s }

func (t *Transaction) ID() uuid.UUID        { return t.s.ID }
func (t *Transaction) Kind() Kind           { return t.s.Kind }
func (t *Transaction) Status() Status       { return t.s.Status }
func (t *Transaction) Money() money.Money   { return t.s.Money }
func (t *Transaction) WalletID() uuid.UUID  { return t.s.WalletID }
func (t *Transaction) PlayerID() uuid.UUID  { return t.s.PlayerID }
func (t *Transaction) External() *External  { return t.s.External }
func (t *Transaction) Result() *Result      { return t.s.Result }
func (t *Transaction) Failure() FailureCode { return t.s.FailureCode }

func (t *Transaction) requireOpen(target Status) error {
	if t.s.Status != StatusPending && t.s.Status != StatusPendingReference {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.s.Status, target)
	}
	return nil
}

// Process concludes the transaction successfully. reference is the resolved
// referenced transaction (uuid.Nil when none).
func (t *Transaction) Process(result Result, reference uuid.UUID, at time.Time) error {
	if err := t.requireOpen(StatusProcessed); err != nil {
		return err
	}
	if !result.Balance.IsValid() || result.WalletVersion < 1 {
		return fmt.Errorf("%w: invalid result", ErrInvalidTransaction)
	}
	t.s.Status, t.s.Result, t.s.ReferenceTransactionID = StatusProcessed, &result, reference
	t.s.ProcessedAt, t.s.UpdatedAt = at, at
	t.s.NextAttemptAt, t.s.ReferenceDeadline = time.Time{}, time.Time{}
	return nil
}

// Reject concludes the transaction with a business rejection; observed is the
// wallet state seen at that moment (returned on replays).
func (t *Transaction) Reject(code FailureCode, observed *Result, at time.Time) error {
	if err := t.requireOpen(StatusRejected); err != nil {
		return err
	}
	if code == "" || code == FailureInfrastructure {
		return fmt.Errorf("%w: rejection needs a business failure code", ErrInvalidTransaction)
	}
	t.s.Status, t.s.FailureCode, t.s.Result = StatusRejected, code, observed
	t.s.ProcessedAt, t.s.UpdatedAt = at, at
	t.s.NextAttemptAt, t.s.ReferenceDeadline = time.Time{}, time.Time{}
	return nil
}

// Fail records a permanent infrastructure failure (audit only, no movement).
func (t *Transaction) Fail(at time.Time) error {
	if err := t.requireOpen(StatusFailed); err != nil {
		return err
	}
	t.s.Status, t.s.FailureCode = StatusFailed, FailureInfrastructure
	t.s.ProcessedAt, t.s.UpdatedAt = at, at
	t.s.NextAttemptAt, t.s.ReferenceDeadline = time.Time{}, time.Time{}
	return nil
}

// AwaitReference parks the transaction until its reference is available
// (PENDING -> PENDING_REFERENCE) or schedules the next attempt (attempt counter
// grows). The deadline is fixed by the first call.
func (t *Transaction) AwaitReference(next, deadline time.Time, at time.Time) error {
	if err := t.requireOpen(StatusPendingReference); err != nil {
		return err
	}
	if next.IsZero() || deadline.IsZero() {
		return fmt.Errorf("%w: schedule required", ErrInvalidTransaction)
	}
	if t.s.Status == StatusPending {
		t.s.ReferenceDeadline = deadline
	}
	t.s.Status, t.s.NextAttemptAt, t.s.UpdatedAt = StatusPendingReference, next, at
	t.s.Attempts++
	return nil
}

// ReferenceExpired reports whether the waiting window has elapsed.
func (t *Transaction) ReferenceExpired(now time.Time) bool {
	return t.s.Status == StatusPendingReference && !now.Before(t.s.ReferenceDeadline)
}
