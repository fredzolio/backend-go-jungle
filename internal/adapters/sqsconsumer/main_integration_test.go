//go:build integration

package sqsconsumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/sqsconsumer"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
	"github.com/fredzolio/backend-go-jungle/internal/testsupport/awstest"
	"github.com/fredzolio/backend-go-jungle/internal/testsupport/pgtest"
)

var (
	pg        *pgtest.Env
	ministack *awstest.Env
	silent    = slog.New(slog.NewTextHandler(io.Discard, nil))
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	if pg, err = pgtest.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if ministack, err = awstest.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = ministack.Stop(ctx)
	_ = pg.Stop(ctx)
	os.Exit(code)
}

// rig is one isolated database + queue pair + use cases.
type rig struct {
	db       pgtest.DB
	queues   awstest.Queues
	client   *sqs.Client
	wagering *app.Wagering
	wallets  *app.Wallets
	senders  *sqsconsumer.Senders
}

func newRig(t *testing.T, trusted ...string) rig {
	t.Helper()
	db := pg.NewDatabase(t)
	deps := app.Deps{UoW: postgres.NewUnitOfWork(db.App), Clock: app.SystemClock{}, IDs: app.UUIDv7{}}
	path := filepath.Join(t.TempDir(), "senders.json")
	if err := os.WriteFile(path, []byte(`{"provider-a":{"access_key_id":"AKIA-A","user_id":"AIDA-A"},"provider-b":{"access_key_id":"AKIA-B","user_id":"AIDA-B"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if trusted == nil {
		trusted = []string{"000000000000"} // MiniStack reports the account as SenderId
	}
	senders, err := sqsconsumer.LoadSenders(path, trusted)
	if err != nil {
		t.Fatal(err)
	}
	return rig{db: db, queues: ministack.NewQueues(t, 2, 4), client: ministack.SQS(), senders: senders,
		wagering: app.NewWagering(deps, app.DefaultReferencePolicy), wallets: app.NewWallets(deps)}
}

// start runs a consumer until the test ends (or stop is called).
func (r rig) start(t *testing.T, api sqsconsumer.API) (stop func()) {
	t.Helper()
	if api == nil {
		api = r.client
	}
	c := sqsconsumer.New(sqsconsumer.Deps{API: api, Ingest: r.wagering, Senders: r.senders, Log: silent, Observer: nopObserver{}}, sqsconsumer.Config{
		QueueURL: r.queues.URL, DLQURL: r.queues.DLQURL, ConsumerName: "wager-ingress",
		ProcessTimeout: 10 * time.Second, MaxBackoff: 2 * time.Second, MaxMessages: 10, WaitSeconds: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return stop
}

func (r rig) open(t *testing.T, balance string) wallet.Snapshot {
	t.Helper()
	m, _ := money.Parse(balance, "BRL")
	w, err := r.wallets.Open(context.Background(), app.OpenWalletCommand{PlayerID: uuid.New(), InitialBalance: m})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func envelope(w wallet.Snapshot, messageID, extID, kind, amount string) string {
	body, _ := json.Marshal(map[string]any{
		"messageId": messageID, "type": "WagerTransactionRequested", "occurredAt": "2026-09-08T12:00:00.000Z",
		"data": map[string]any{
			"providerId": "provider-a", "externalTransactionId": extID, "idempotencyKey": "provider-a:" + extID,
			"playerId": w.PlayerID.String(), "walletId": w.ID.String(), "roundId": "round-987", "gameId": "fortune-chimp",
			"kind": kind, "money": map[string]string{"amount": amount, "currency": "BRL"},
		},
	})
	return string(body)
}

// requestFor builds the same operation the envelope helper carries (HTTP path).
func requestFor(t *testing.T, w wallet.Snapshot, extID string) wagering.ExternalRequest {
	t.Helper()
	m, _ := money.Parse("25.00", "BRL")
	req, err := wagering.NewExternalRequest(wagering.RawRequest{
		ProviderID: "provider-a", ExternalTransactionID: extID, IdempotencyKey: "provider-a:" + extID,
		PlayerID: w.PlayerID.String(), WalletID: w.ID.String(), RoundID: "round-987", GameID: "fortune-chimp",
		Kind: "BET", Money: m,
	})
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// send publishes like a provider: group = wallet, dedup id = unique per send so
// tests can deliver the same envelope twice.
func (r rig) send(t *testing.T, group, body string) {
	t.Helper()
	_, err := r.client.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl: aws.String(r.queues.URL), MessageBody: aws.String(body),
		MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(uuid.NewString()),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (r rig) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := r.db.Owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (r rig) queueDepth(t *testing.T, url string) int {
	t.Helper()
	out, err := r.client.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(url),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
	if err != nil {
		t.Fatal(err)
	}
	var visible, hidden int
	fmt.Sscan(out.Attributes["ApproximateNumberOfMessages"], &visible)
	fmt.Sscan(out.Attributes["ApproximateNumberOfMessagesNotVisible"], &hidden)
	return visible + hidden
}

// dlqCodes drains the DLQ returning the errorCode attribute of each message.
func (r rig) dlqCodes(t *testing.T) []string {
	t.Helper()
	out, err := r.client.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(r.queues.DLQURL),
		MaxNumberOfMessages: 10, WaitTimeSeconds: 1, MessageAttributeNames: []string{"All"}})
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, m := range out.Messages {
		codes = append(codes, aws.ToString(m.MessageAttributes["errorCode"].StringValue))
	}
	return codes
}

type nopObserver struct{}

func (nopObserver) MessageHandled(string, bool) {}

// failingDeletes simulates a process that dies right after committing: the
// message is never removed from the queue.
type failingDeletes struct{ sqsconsumer.API }

func (failingDeletes) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return nil, errors.New("simulated crash before DeleteMessage")
}
