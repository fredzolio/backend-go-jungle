//go:build integration

package snspublisher_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/snspublisher"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
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
	if pg, err = pgtest.Start(ctx); err == nil {
		ministack, err = awstest.Start(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = ministack.Stop(ctx)
	_ = pg.Stop(ctx)
	os.Exit(code)
}

type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

var policy = app.RelayPolicy{Lease: 30 * time.Second, InitialBackoff: time.Second, MaxBackoff: 10 * time.Second, MaxAttempts: 3}

type rig struct {
	db    pgtest.DB
	topic awstest.Topic
	clock *fakeClock
	store *postgres.OutboxRelay
}

// newRig seeds wallets×bets through the real use cases, producing real events.
func newRig(t *testing.T, wallets, bets int) rig {
	t.Helper()
	db := pg.NewDatabase(t)
	deps := app.Deps{UoW: postgres.NewUnitOfWork(db.App), Clock: app.SystemClock{}, IDs: app.UUIDv7{}}
	open, submit := app.NewWallets(deps), app.NewWagering(deps, app.DefaultReferencePolicy)
	ctx := context.Background()
	balance, _ := money.Parse("100.00", "BRL")
	for range wallets {
		w, err := open.Open(ctx, app.OpenWalletCommand{PlayerID: uuid.New(), InitialBalance: balance})
		if err != nil {
			t.Fatal(err)
		}
		for i := range bets {
			amount, _ := money.Parse("1.00", "BRL")
			ext := fmt.Sprintf("bet-%d", i)
			req, err := wagering.NewExternalRequest(wagering.RawRequest{ProviderID: "provider-a", ExternalTransactionID: w.ID.String() + "-" + ext,
				IdempotencyKey: w.ID.String() + "-" + ext, PlayerID: w.PlayerID.String(), WalletID: w.ID.String(),
				RoundID: "r", GameID: "g", Kind: "BET", Money: amount})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := submit.Submit(ctx, app.SubmitCommand{Request: req, AuthenticatedProvider: "provider-a"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return rig{db: db, topic: ministack.NewTopic(t), clock: &fakeClock{now: time.Now().UTC().Add(time.Minute)}, store: postgres.NewOutboxRelay(db.App)}
}

func (r rig) relay(owner string, store app.OutboxRelayStore, pub app.EventPublisher) *app.Relay {
	if store == nil {
		store = r.store
	}
	if pub == nil {
		pub = snspublisher.New(ministack.SNS(), r.topic.ARN)
	}
	return app.NewRelay(store, pub, r.clock, silent, owner, policy)
}

func drain(t *testing.T, relay *app.Relay) {
	t.Helper()
	for range 100 {
		n, err := relay.RunOnce(context.Background(), 7)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	t.Fatal("relay never drained")
}

func (r rig) count(t *testing.T, sql string) int64 {
	t.Helper()
	var n int64
	if err := r.db.Owner.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// expectedOrder returns, per wallet, the event ids in commit (seq) order.
func (r rig) expectedOrder(t *testing.T) map[string][]string {
	t.Helper()
	rows, err := r.db.Owner.Query(context.Background(), `SELECT partition_key, id FROM outbox_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var key string
		var id uuid.UUID
		_ = rows.Scan(&key, &id)
		out[key] = append(out[key], id.String())
	}
	return out
}

// received reads the audit queue until want messages arrived, per group in order.
func (r rig) received(t *testing.T, want int) (map[string][]string, int) {
	t.Helper()
	client, out, total := ministack.SQS(), map[string][]string{}, 0
	deadline := time.Now().Add(30 * time.Second)
	for total < want && time.Now().Before(deadline) {
		res, err := client.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(r.topic.QueueURL),
			MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameMessageGroupId}})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range res.Messages {
			var env struct {
				EventID string `json:"eventId"`
			}
			_ = json.Unmarshal([]byte(aws.ToString(m.Body)), &env)
			group := m.Attributes["MessageGroupId"]
			out[group] = append(out[group], env.EventID)
			total++
			_, _ = client.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: aws.String(r.topic.QueueURL), ReceiptHandle: m.ReceiptHandle})
		}
	}
	return out, total
}

// Mandatory scenario 6 (part): events committed while no relay ran (crash between
// commit and publish) are all published, once each, in wallet order.
func TestRelay_publishes_committed_events_once_in_wallet_order(t *testing.T) {
	r := newRig(t, 4, 3)
	want := int(r.count(t, `SELECT count(*) FROM outbox_events`))
	drain(t, r.relay("relay-1", nil, nil))
	if r.count(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) != 0 {
		t.Fatal("unpublished events remain")
	}
	got, total := r.received(t, want)
	if total != want {
		t.Fatalf("received %d of %d", total, want)
	}
	for wallet, ids := range r.expectedOrder(t) {
		if !slices.Equal(got[wallet], ids) {
			t.Fatalf("wallet %s order:\n got %v\nwant %v", wallet, got[wallet], ids)
		}
	}
}

// Mandatory scenario 6: two publishers contending for the same outbox.
func TestTwo_relays_contending_publish_everything_in_order(t *testing.T) {
	r := newRig(t, 6, 3)
	want := int(r.count(t, `SELECT count(*) FROM outbox_events`))
	var wg sync.WaitGroup
	for i := range 2 {
		relay := r.relay(fmt.Sprintf("relay-%d", i), nil, nil)
		wg.Go(func() { drain(t, relay) })
	}
	wg.Wait()
	if r.count(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) != 0 {
		t.Fatal("unpublished events remain")
	}
	got, total := r.received(t, want)
	if total != want {
		t.Fatalf("received %d of %d (duplicates or losses)", total, want)
	}
	for wallet, ids := range r.expectedOrder(t) {
		if !slices.Equal(got[wallet], ids) {
			t.Fatalf("wallet %s order broken", wallet)
		}
	}
}

// crashBeforeConfirm publishes but never confirms (process died after publish).
type crashBeforeConfirm struct{ *postgres.OutboxRelay }

func (crashBeforeConfirm) MarkPublished(context.Context, uuid.UUID, uuid.UUID, time.Time) (bool, error) {
	return false, errors.New("simulated crash before confirming publication")
}

// Mandatory scenario 6: crash between publication and confirmation. Another relay
// takes over after the lease expires and republishes with the same eventId.
func TestCrash_between_publish_and_confirm_republishes_same_event_id(t *testing.T) {
	r := newRig(t, 1, 1) // 4 events in one partition
	_, _ = r.relay("doomed", crashBeforeConfirm{r.store}, nil).RunOnce(context.Background(), 10)
	if r.count(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) != 4 {
		t.Fatal("nothing should be confirmed by the crashed relay")
	}
	r.clock.Advance(policy.Lease + time.Second) // lease expires
	drain(t, r.relay("survivor", nil, nil))
	if r.count(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) != 0 {
		t.Fatal("survivor did not publish")
	}
	if r.count(t, `SELECT max(attempts) FROM outbox_events`) != 2 {
		t.Fatal("head event should have been published twice (attempts=2)")
	}
	got, total := r.received(t, 4)
	ids := r.expectedOrder(t)
	for wallet := range ids {
		if total != 4 || !slices.Equal(got[wallet], ids[wallet]) {
			t.Fatalf("subscriber saw %d messages %v, want the 4 events once (same eventIds) in order %v", total, got[wallet], ids[wallet])
		}
	}
}

type brokenBroker struct{}

func (brokenBroker) Publish(_ context.Context, events []app.ClaimedEvent) map[uuid.UUID]error {
	out := map[uuid.UUID]error{}
	for _, e := range events {
		out[e.ID] = errors.New("broker unavailable")
	}
	return out
}

// Failed publications back off, then park (dead) after MaxAttempts; a parked head
// keeps the rest of its wallet's events waiting (order over availability).
func TestFailing_publications_back_off_and_park_the_head(t *testing.T) {
	r := newRig(t, 1, 0) // opening: 2 events in one partition
	relay := r.relay("relay", nil, brokenBroker{})
	for range policy.MaxAttempts {
		if _, err := relay.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		r.clock.Advance(policy.MaxBackoff + time.Second)
	}
	if r.count(t, `SELECT count(*) FROM outbox_events WHERE dead_at IS NOT NULL`) != 1 {
		t.Fatal("head should be parked after max attempts")
	}
	if n, _ := relay.RunOnce(context.Background(), 10); n != 0 {
		t.Fatalf("events behind a parked head were claimed: %d", n)
	}
	if r.count(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL`) != 0 {
		t.Fatal("nothing may be marked published")
	}
}
