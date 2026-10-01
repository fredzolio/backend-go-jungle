package app

import (
	"context"
	"errors"
)

// ErrMessageConflict: a message id was redelivered with different content. The
// message is poison: nothing is applied and it goes to the DLQ.
var ErrMessageConflict = errors.New("app: message id reused with different content")

// IngestCommand is one message from the ingress queue.
type IngestCommand struct {
	Submit       SubmitCommand
	ConsumerName string
	MessageID    string // envelope messageId: durable identity of the message
	MessageHash  string // canonical hash of the envelope
}

// IngestResult is the outcome of a committed ingestion.
type IngestResult struct {
	Submit SubmitResult
	// Duplicate: this message id had already been handled (redelivery).
	Duplicate bool
}

// Ingest applies a queued operation with the same guarantees as HTTP (same use
// case, same financial idempotency) and records the inbox row in the same SQL
// transaction as the domain change, the ledger and the events. A redelivery of
// a handled message finds the operation already applied (idempotent replay) and
// the inbox row already present: nothing moves twice.
func (uc *Wagering) Ingest(ctx context.Context, cmd IngestCommand) (IngestResult, error) {
	var duplicate bool
	submit := cmd.Submit
	submit.Within = func(ctx context.Context, tx Tx, result SubmitResult) error {
		inserted, stored, err := tx.Inbox().Complete(ctx, InboxRecord{
			ConsumerName: cmd.ConsumerName, MessageID: cmd.MessageID, PayloadHash: cmd.MessageHash,
			TransactionID: result.Transaction.ID, ReceivedAt: uc.d.Clock.Now(),
		})
		if err != nil {
			return err
		}
		if !inserted && stored != cmd.MessageHash {
			return ErrMessageConflict
		}
		duplicate = !inserted
		return nil
	}
	res, err := uc.Submit(ctx, submit)
	if err != nil {
		return IngestResult{}, err
	}
	return IngestResult{Submit: res, Duplicate: duplicate}, nil
}
