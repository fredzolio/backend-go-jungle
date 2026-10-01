package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/fredzolio/backend-go-jungle/internal/platform/faults"
)

// RelayPolicy tunes the outbox relay.
type RelayPolicy struct {
	Lease          time.Duration // must exceed the publish timeout
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	MaxAttempts    int // then the event is parked (dead_at) and alerted on
}

// RelayConfig wires a relay.
type RelayConfig struct {
	Store     OutboxRelayStore
	Publisher EventPublisher
	Clock     Clock
	Metrics   Metrics
	Log       *slog.Logger
	Owner     string // instance id, recorded as locked_by
	Policy    RelayPolicy
}

// Relay publishes committed outbox events. Any number of relays (in any process)
// may run: claims are leased and fenced, publication happens outside SQL
// transactions, and a crash at any point only causes a later republication of the
// same eventId (consumers deduplicate by eventId; SNS FIFO also deduplicates it).
type Relay struct {
	store   OutboxRelayStore
	pub     EventPublisher
	clock   Clock
	metrics Metrics
	log     *slog.Logger
	owner   string
	policy  RelayPolicy
}

// NewRelay builds a relay.
func NewRelay(c RelayConfig) *Relay {
	m := c.Metrics
	if m == nil {
		m = NopMetrics{}
	}
	return &Relay{store: c.Store, pub: c.Publisher, clock: c.Clock, metrics: m, log: c.Log, owner: c.Owner, policy: c.Policy}
}

// RunOnce claims, publishes and confirms one batch; it returns the batch size.
func (r *Relay) RunOnce(ctx context.Context, batch int) (int, error) {
	events, err := r.store.Claim(ctx, r.owner, r.clock.Now(), r.policy.Lease, batch)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	results := r.pub.Publish(ctx, events)
	faults.Point("relay.after_publish")
	var published []ClaimedEvent
	for _, e := range events {
		perr := results[e.ID]
		if perr == nil {
			published = append(published, e)
			continue
		}
		dead := e.Attempts >= r.policy.MaxAttempts
		if err := r.store.Reschedule(ctx, e, r.clock.Now().Add(r.backoff(e.Attempts)), perr.Error(), dead); err != nil {
			return len(events), err
		}
		r.metrics.OutboxFailed(dead)
		r.log.WarnContext(ctx, "outbox publish failed", slog.String("eventId", e.ID.String()),
			slog.String("eventType", e.EventType), slog.Int("attempts", e.Attempts), slog.Bool("dead", dead), slog.Any("error", perr))
	}
	if len(published) == 0 {
		return len(events), nil
	}
	confirmed, err := r.store.MarkPublished(ctx, published, r.clock.Now())
	if err != nil {
		return len(events), err
	}
	if confirmed < len(published) {
		r.log.WarnContext(ctx, "outbox leases lost after publish; those events may be republished",
			slog.Int("handled", len(published)-confirmed))
	}
	r.metrics.OutboxPublished(confirmed)
	return len(events), nil
}

func (r *Relay) backoff(attempts int) time.Duration {
	if attempts > 20 {
		return r.policy.MaxBackoff
	}
	return min(r.policy.InitialBackoff<<max(attempts-1, 0), r.policy.MaxBackoff)
}
