package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fredzolio/backend-go-jungle/internal/app"
)

// OutboxRelay implements app.OutboxRelayStore. Each call is one autocommit
// statement: the claim commits its lease before any network I/O, so no SQL
// transaction is ever held open while publishing.
type OutboxRelay struct{ pool *pgxpool.Pool }

// NewOutboxRelay builds the relay store.
func NewOutboxRelay(pool *pgxpool.Pool) *OutboxRelay { return &OutboxRelay{pool: pool} }

var _ app.OutboxRelayStore = (*OutboxRelay)(nil)

// Claim takes the oldest pending events that have no earlier unpublished event in
// their partition (the partition "head"). A head that is leased, backing off or
// parked (dead) keeps blocking its partition: order wins over availability, and a
// parked head is alerted on. SKIP LOCKED skips heads another relay is claiming;
// the events behind them are not eligible because the head is still unpublished.
// Both lookups use partial indexes over unpublished rows (migration 00006).
func (r *OutboxRelay) Claim(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]app.ClaimedEvent, error) {
	rows, err := r.pool.Query(ctx, `
		WITH candidates AS (
			SELECT o.id FROM outbox_events o
			 WHERE o.published_at IS NULL AND o.dead_at IS NULL AND o.next_attempt_at <= $1
			   AND (o.locked_until IS NULL OR o.locked_until < $1)
			   AND NOT EXISTS (SELECT 1 FROM outbox_events e
			                    WHERE e.partition_key = o.partition_key AND e.published_at IS NULL AND e.seq < o.seq)
			 ORDER BY o.seq
			 LIMIT $2
			 FOR UPDATE OF o SKIP LOCKED
		)
		UPDATE outbox_events o
		   SET locked_by = $3, locked_until = $4, claim_id = gen_random_uuid(), attempts = o.attempts + 1
		  FROM candidates c
		 WHERE o.id = c.id
		RETURNING o.id, o.claim_id, o.partition_key, o.event_type, o.event_version, o.payload, o.attempts`,
		now.UTC(), limit, owner, now.Add(lease).UTC())
	if err != nil {
		return nil, classify("claim outbox", err)
	}
	defer rows.Close()
	var out []app.ClaimedEvent
	for rows.Next() {
		var e app.ClaimedEvent
		if err := rows.Scan(&e.ID, &e.ClaimID, &e.PartitionKey, &e.EventType, &e.EventVersion, &e.Payload, &e.Attempts); err != nil {
			return nil, classify("scan claimed event", err)
		}
		out = append(out, e)
	}
	return out, classify("claim outbox", rows.Err())
}

// MarkPublished confirms publications in one statement, each fenced by its claim
// id; it returns how many were confirmed (the rest lost their lease).
func (r *OutboxRelay) MarkPublished(ctx context.Context, events []app.ClaimedEvent, at time.Time) (int, error) {
	ids, claims := make([]uuid.UUID, len(events)), make([]uuid.UUID, len(events))
	for i, e := range events {
		ids[i], claims[i] = e.ID, e.ClaimID
	}
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events o
		   SET published_at = $3, locked_by = NULL, locked_until = NULL, last_error = NULL
		  FROM unnest($1::uuid[], $2::uuid[]) AS c(id, claim_id)
		 WHERE o.id = c.id AND o.claim_id = c.claim_id AND o.published_at IS NULL`, ids, claims, at.UTC())
	if err != nil {
		return 0, classify("mark published", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *OutboxRelay) Reschedule(ctx context.Context, e app.ClaimedEvent, next time.Time, lastErr string, dead bool) error {
	_, err := r.pool.Exec(ctx, `UPDATE outbox_events
		   SET next_attempt_at = $3, last_error = $4, locked_by = NULL, locked_until = NULL,
		       dead_at = CASE WHEN $5 THEN now() ELSE NULL END
		 WHERE id = $1 AND claim_id = $2 AND published_at IS NULL`, e.ID, e.ClaimID, next.UTC(), lastErr, dead)
	return classify("reschedule outbox event", err)
}

func (r *OutboxRelay) Backlog(ctx context.Context, now time.Time) (time.Duration, int64, error) {
	var oldest *time.Time
	var pending int64
	err := r.pool.QueryRow(ctx, `SELECT min(occurred_at), count(*) FROM outbox_events
		WHERE published_at IS NULL AND dead_at IS NULL`).Scan(&oldest, &pending)
	if err != nil || oldest == nil {
		return 0, pending, classify("outbox backlog", err)
	}
	return now.Sub(*oldest), pending, nil
}
