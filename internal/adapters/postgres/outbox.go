package postgres

import (
	"context"

	"github.com/fredzolio/backend-go-jungle/internal/app"
)

type outboxStore stores

// Insert records events in the caller's transaction; they become visible to the
// relay only if that transaction commits.
func (s outboxStore) Insert(ctx context.Context, records ...app.OutboxRecord) error {
	for _, r := range records {
		_, err := s.q.Exec(ctx, `INSERT INTO outbox_events
			(id, aggregate_type, aggregate_id, partition_key, event_type, event_version, payload, occurred_at, next_attempt_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`,
			r.EventID, r.AggregateType, r.AggregateID, r.PartitionKey, r.EventType, r.EventVersion, r.Payload, r.OccurredAt.UTC())
		if err != nil {
			return classify("insert outbox event", err)
		}
	}
	return nil
}
