package postgres

import (
	"context"

	"github.com/fredzolio/backend-go-jungle/internal/app"
)

type inboxStore stores

// Complete inserts the inbox row in the caller's transaction (the same one that
// applies the domain change), so "handled" and its effects commit together.
func (s inboxStore) Complete(ctx context.Context, r app.InboxRecord) (bool, string, error) {
	tag, err := s.q.Exec(ctx, `INSERT INTO inbox_messages
		(consumer_name, message_id, payload_hash, transaction_id, received_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $5) ON CONFLICT DO NOTHING`,
		r.ConsumerName, r.MessageID, r.PayloadHash, nullUUID(r.TransactionID), r.ReceivedAt.UTC())
	if err != nil {
		return false, "", classify("insert inbox", err)
	}
	if tag.RowsAffected() == 1 {
		return true, "", nil
	}
	var stored string
	err = s.q.QueryRow(ctx, `SELECT payload_hash FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`,
		r.ConsumerName, r.MessageID).Scan(&stored)
	return false, stored, classify("read inbox", err)
}
