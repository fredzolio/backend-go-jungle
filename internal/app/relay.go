package app

import (
	"context"
	"log/slog"
	"time"
)

// RelayPolicy tunes the outbox relay.
type RelayPolicy struct {
	Lease          time.Duration // must exceed the publish timeout
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	MaxAttempts    int // then the event is parked (dead_at) and alerted on
}

// Relay publishes committed outbox events. Any number of relays (in any process)
// may run: claims are leased and fenced, publication happens outside SQL
// transactions, and a crash at any point only causes a later republication of the
// same eventId (consumers deduplicate by eventId; SNS FIFO also deduplicates it).
type Relay struct {
	store  OutboxRelayStore
	pub    EventPublisher
	clock  Clock
	log    *slog.Logger
	owner  string
	policy RelayPolicy
}

// NewRelay builds a relay identified by owner (instance id).
func NewRelay(store OutboxRelayStore, pub EventPublisher, clock Clock, log *slog.Logger, owner string, policy RelayPolicy) *Relay {
	return &Relay{store: store, pub: pub, clock: clock, log: log, owner: owner, policy: policy}
}

// RunOnce claims, publishes and confirms one batch; it returns the batch size.
func (r *Relay) RunOnce(ctx context.Context, batch int) (int, error) {
	events, err := r.store.Claim(ctx, r.owner, r.clock.Now(), r.policy.Lease, batch)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	results := r.pub.Publish(ctx, events)
	for _, e := range events {
		if perr := results[e.ID]; perr != nil {
			dead := e.Attempts >= r.policy.MaxAttempts
			next := r.clock.Now().Add(r.backoff(e.Attempts))
			if err := r.store.Reschedule(ctx, e, next, perr.Error(), dead); err != nil {
				return len(events), err
			}
			r.log.WarnContext(ctx, "outbox publish failed", slog.String("eventId", e.ID.String()),
				slog.String("eventType", e.EventType), slog.Int("attempts", e.Attempts), slog.Bool("dead", dead), slog.Any("error", perr))
			continue
		}
		ok, err := r.store.MarkPublished(ctx, e.ID, e.ClaimID, r.clock.Now())
		if err != nil {
			return len(events), err
		}
		if !ok {
			r.log.WarnContext(ctx, "outbox lease lost after publish; it may be republished", slog.String("eventId", e.ID.String()))
		}
	}
	return len(events), nil
}

func (r *Relay) backoff(attempts int) time.Duration {
	if attempts > 20 {
		return r.policy.MaxBackoff
	}
	return min(r.policy.InitialBackoff<<max(attempts-1, 0), r.policy.MaxBackoff)
}
