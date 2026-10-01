package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/events"
)

// Use-case errors, classifiable with errors.Is. None of them persists anything.
var (
	// ErrIdempotencyConflict: the idempotency key was reused with a different payload.
	ErrIdempotencyConflict = errors.New("app: idempotency key reused with a different payload")
	// ErrDuplicateExternalTransaction: (providerId, externalTransactionId) was already
	// submitted under another idempotency key; it is never applied twice.
	ErrDuplicateExternalTransaction = errors.New("app: external transaction already submitted with another idempotency key")
	// ErrWalletNotFound: the wallet does not exist (correctable input).
	ErrWalletNotFound = errors.New("app: wallet not found")
	// ErrProviderMismatch: the authenticated provider differs from the payload providerId.
	ErrProviderMismatch = errors.New("app: provider does not match the authenticated identity")
)

// Clock returns the current time in UTC with microsecond precision (what
// PostgreSQL stores), so in-memory and persisted timestamps agree.
type Clock interface{ Now() time.Time }

// SystemClock is the production clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// IDs generates identifiers (UUIDv7: time-ordered, index friendly).
type IDs interface{ New() uuid.UUID }

// UUIDv7 is the production generator.
type UUIDv7 struct{}

// New implements IDs.
func (UUIDv7) New() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// ReferencePolicy bounds the wait for a reference that has not arrived yet.
type ReferencePolicy struct {
	InitialBackoff time.Duration // first retry delay
	MaxBackoff     time.Duration // cap of the exponential backoff
	TTL            time.Duration // after this, REJECTED with REFERENCE_NOT_FOUND
}

// DefaultReferencePolicy: 1s, 2s, 4s ... capped at 1 min, for up to 10 min.
var DefaultReferencePolicy = ReferencePolicy{InitialBackoff: time.Second, MaxBackoff: time.Minute, TTL: 10 * time.Minute}

// NextAttempt returns now + exponential backoff with "equal jitter" (half fixed,
// half random) for the given attempt number (0-based), using integer math only.
func (p ReferencePolicy) NextAttempt(now time.Time, attempt int) time.Time {
	delay := p.MaxBackoff
	if attempt < 30 && p.InitialBackoff<<attempt < p.MaxBackoff {
		delay = p.InitialBackoff << attempt
	}
	half := delay / 2
	return now.Add(half + time.Duration(rand.Int64N(int64(half)+1))) //nolint:gosec // jitter, not security
}

// Meta carries correlation data of the request being processed.
type Meta struct {
	CorrelationID string // HTTP X-Correlation-Id or SQS messageId
	CausationID   string // optional: what directly caused this processing
}

// outboxRecord snapshots an event envelope for the outbox. Every event of a wallet
// shares the wallet as partition key: per-wallet ordering and SNS MessageGroupId.
func outboxRecord[T any](env events.Envelope[T], aggregateType string, walletID uuid.UUID) (OutboxRecord, error) {
	payload, err := json.Marshal(env)
	if err != nil {
		return OutboxRecord{}, fmt.Errorf("marshal %s: %w", env.EventType, err)
	}
	return OutboxRecord{
		EventID: env.EventID, AggregateType: aggregateType, AggregateID: env.AggregateID,
		PartitionKey: walletID.String(), EventType: env.EventType, EventVersion: env.Version,
		Payload: payload, OccurredAt: time.Time(env.OccurredAt),
	}, nil
}

func (m Meta) event(ids IDs, at time.Time, fallbackCorrelation string) events.Meta {
	correlation := m.CorrelationID
	if correlation == "" {
		correlation = fallbackCorrelation
	}
	return events.Meta{EventID: ids.New(), OccurredAt: at, CorrelationID: correlation, CausationID: m.CausationID}
}
