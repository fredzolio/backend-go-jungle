package sqsconsumer

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/platform/faults"
)

// API is the subset of the SQS client the consumer uses.
type API interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

// Ingester applies one message (app.Wagering).
type Ingester interface {
	Ingest(ctx context.Context, cmd app.IngestCommand) (app.IngestResult, error)
}

// Config tunes the consumer. Visibility timeout and maxReceiveCount live on the
// queue (Terraform); ProcessTimeout must stay well below the visibility timeout.
type Config struct {
	QueueURL       string
	DLQURL         string
	ConsumerName   string
	ProcessTimeout time.Duration
	MaxBackoff     time.Duration
	MaxMessages    int32
	WaitSeconds    int32
}

// Observer receives one outcome per handled message (metrics).
type Observer interface {
	MessageHandled(outcome string, duplicate bool)
}

// Deps are the collaborators of a consumer.
type Deps struct {
	API      API
	Ingest   Ingester
	Senders  *Senders
	Log      *slog.Logger
	Observer Observer
}

// Consumer polls the ingress queue.
type Consumer struct {
	d   Deps
	cfg Config
}

// New builds a consumer.
func New(d Deps, cfg Config) *Consumer { return &Consumer{d: d, cfg: cfg} }

// Run polls until ctx is cancelled. Cancellation stops fetching immediately; a
// message already being handled finishes (bounded by ProcessTimeout) and the
// rest of its batch is released for immediate redelivery elsewhere.
func (c *Consumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		out, err := c.d.API.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(c.cfg.QueueURL), MaxNumberOfMessages: c.cfg.MaxMessages, WaitTimeSeconds: c.cfg.WaitSeconds,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameSenderId, types.MessageSystemAttributeNameApproximateReceiveCount,
				types.MessageSystemAttributeNameMessageGroupId,
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.d.Log.Warn("receive failed", slog.Any("error", err))
			sleep(ctx, time.Second)
			continue
		}
		c.handleBatch(ctx, out.Messages)
	}
}

// handleBatch keeps FIFO order per group: once a message of a group stays in the
// queue, the following messages of that group in the batch are released instead
// of being processed out of order.
func (c *Consumer) handleBatch(ctx context.Context, msgs []types.Message) {
	blocked := map[string]bool{}
	for _, m := range msgs {
		group := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
		if ctx.Err() != nil || blocked[group] {
			c.changeVisibility(m, 0)
			continue
		}
		if !c.handle(ctx, m) {
			blocked[group] = true
		}
	}
}

type outcome int

const (
	committed outcome = iota // durable handling committed: delete
	poison                   // permanent: DLQ + delete
	transient                // keep: retry later with backoff (redrive after maxReceiveCount)
)

// handle processes one message and reports whether it left the queue.
func (c *Consumer) handle(ctx context.Context, m types.Message) bool {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.ProcessTimeout)
	defer cancel()
	result, reason, duplicate, err := c.process(pctx, m)
	c.d.Observer.MessageHandled(reason, duplicate)
	attrs := []any{slog.String("sqsMessageId", aws.ToString(m.MessageId)), slog.String("outcome", reason)}
	switch result {
	case committed:
		faults.Point("consumer.after_commit")
		if err := c.delete(m); err != nil {
			// Committed but still visible: the redelivery is an idempotent replay.
			c.d.Log.Warn("delete after commit failed", append(attrs, slog.Any("error", err))...)
		}
		return true
	case poison:
		c.d.Log.Warn("message rejected to DLQ", append(attrs, slog.Any("error", err))...)
		if dlqErr := c.toDLQ(m, reason); dlqErr != nil {
			c.d.Log.Error("dlq send failed; message kept", append(attrs, slog.Any("error", dlqErr))...)
			return false
		}
		return true
	default:
		c.d.Log.Warn("transient failure; message kept", append(attrs, slog.Any("error", err))...)
		c.changeVisibility(m, c.backoff(m))
		return false
	}
}

func (c *Consumer) process(ctx context.Context, m types.Message) (outcome, string, bool, error) {
	msg, err := parse(aws.ToString(m.Body))
	if err != nil {
		return poison, "MALFORMED", false, err
	}
	f := msg.request.Fields()
	if err := c.d.Senders.Authorize(m.Attributes[string(types.MessageSystemAttributeNameSenderId)], f.ProviderID); err != nil {
		return poison, "UNAUTHORIZED_SENDER", false, err
	}
	res, err := c.d.Ingest.Ingest(ctx, app.IngestCommand{
		ConsumerName: c.cfg.ConsumerName, MessageID: msg.id, MessageHash: msg.hash,
		Submit: app.SubmitCommand{
			Request: msg.request, AuthenticatedProvider: f.ProviderID,
			Meta: app.Meta{CorrelationID: msg.id, CausationID: aws.ToString(m.MessageId), Channel: "sqs"},
		},
	})
	if err != nil {
		result, code, err := classify(err)
		return result, code, false, err
	}
	t := res.Submit.Transaction
	c.d.Log.Info("message handled", slog.String("messageId", msg.id), slog.String("transactionId", t.ID.String()),
		slog.String("walletId", t.WalletID.String()), slog.String("providerId", f.ProviderID),
		slog.String("status", string(t.Status)), slog.Bool("duplicate", res.Duplicate), slog.Bool("replay", res.Submit.Replay))
	return committed, string(t.Status), res.Duplicate, nil
}

// classify: definitive problems go to the DLQ at once; anything else is retried
// (and reaches the DLQ through the queue redrive after maxReceiveCount).
func classify(err error) (outcome, string, error) {
	permanent := map[error]string{
		app.ErrIdempotencyConflict: "IDEMPOTENCY_KEY_REUSED", app.ErrDuplicateExternalTransaction: "DUPLICATE_EXTERNAL_TRANSACTION",
		app.ErrProviderMismatch: "PROVIDER_MISMATCH", app.ErrWalletNotFound: "WALLET_NOT_FOUND",
		app.ErrMessageConflict: "MESSAGE_ID_CONFLICT", app.ErrIntegrity: "INTEGRITY_VIOLATION",
		wagering.ErrInvalidRequest: "INVALID_REQUEST", wagering.ErrReservedKind: "RESERVED_KIND",
	}
	for sentinel, code := range permanent {
		if errors.Is(err, sentinel) {
			return poison, code, err
		}
	}
	return transient, "TRANSIENT", err
}

func (c *Consumer) backoff(m types.Message) int32 {
	n, err := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	if err != nil || n < 1 {
		n = 1
	}
	delay := time.Second << min(n-1, 16)
	return int32(min(delay, c.cfg.MaxBackoff) / time.Second) //nolint:gosec // bounded by MaxBackoff
}

// Queue operations use their own short deadline: they must complete even while
// the consumer is shutting down.
func opContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (c *Consumer) delete(m types.Message) error {
	ctx, cancel := opContext()
	defer cancel()
	_, err := c.d.API.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(c.cfg.QueueURL), ReceiptHandle: m.ReceiptHandle})
	return err
}

func (c *Consumer) changeVisibility(m types.Message, seconds int32) {
	ctx, cancel := opContext()
	defer cancel()
	if _, err := c.d.API.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(c.cfg.QueueURL), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: seconds,
	}); err != nil {
		c.d.Log.Warn("change visibility failed", slog.String("sqsMessageId", aws.ToString(m.MessageId)), slog.Any("error", err))
	}
}

func (c *Consumer) toDLQ(m types.Message, reason string) error {
	ctx, cancel := opContext()
	defer cancel()
	group := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
	if group == "" {
		group = "poison"
	}
	_, err := c.d.API.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(c.cfg.DLQURL), MessageBody: m.Body, MessageGroupId: aws.String(group),
		MessageDeduplicationId: m.MessageId,
		MessageAttributes: map[string]types.MessageAttributeValue{
			"errorCode":       {DataType: aws.String("String"), StringValue: aws.String(reason)},
			"sourceMessageId": {DataType: aws.String("String"), StringValue: m.MessageId},
		},
	})
	if err != nil {
		return err
	}
	return c.delete(m)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
