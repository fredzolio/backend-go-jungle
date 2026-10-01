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

// Claim: `heads` is the oldest unpublished event of every partition. A head that
// is leased, backing off or parked (dead) blocks its partition: order wins over
// availability, and a parked head is alerted on (outbox backlog metric). SKIP
// LOCKED plus the re-checked lease condition keep concurrent relays from claiming
// the same row.
func (r *OutboxRelay) Claim(ctx context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]app.ClaimedEvent, error) {
	rows, err := r.pool.Query(ctx, `
		WITH heads AS (
			SELECT DISTINCT ON (partition_key) id
			  FROM outbox_events
			 WHERE published_at IS NULL
			 ORDER BY partition_key, seq
		), candidates AS (
			SELECT o.id FROM outbox_events o JOIN heads h ON h.id = o.id
			 WHERE o.dead_at IS NULL AND o.next_attempt_at <= $1 AND (o.locked_until IS NULL OR o.locked_until < $1)
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

func (r *OutboxRelay) MarkPublished(ctx context.Context, id, claimID uuid.UUID, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events
		   SET published_at = $3, locked_by = NULL, locked_until = NULL, last_error = NULL
		 WHERE id = $1 AND claim_id = $2 AND published_at IS NULL`, id, claimID, at.UTC())
	if err != nil {
		return false, classify("mark published", err)
	}
	return tag.RowsAffected() == 1, nil
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
