//go:build integration

package sqsconsumer_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/app"
)

const ledgerOf = `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`

func TestMessage_is_applied_recorded_in_inbox_and_deleted(t *testing.T) {
	r := newRig(t)
	w := r.open(t, "100.00")
	r.start(t, nil)
	r.send(t, w.ID.String(), envelope(w, "msg-1", "tx-1", "BET", "25.00"))
	eventually(t, "message processed and deleted", func() bool {
		return r.count(t, ledgerOf, w.ID) == 2 && r.queueDepth(t, r.queues.URL) == 0
	})
	if n := r.count(t, `SELECT count(*) FROM inbox_messages WHERE message_id = 'msg-1' AND completed_at IS NOT NULL AND transaction_id IS NOT NULL`); n != 1 {
		t.Fatalf("inbox rows = %d", n)
	}
}

func TestRedelivered_message_moves_money_once(t *testing.T) {
	r := newRig(t)
	w := r.open(t, "100.00")
	r.start(t, nil)
	body := envelope(w, "msg-dup", "tx-dup", "BET", "25.00")
	r.send(t, w.ID.String(), body)
	r.send(t, w.ID.String(), body) // at-least-once: same envelope delivered again
	eventually(t, "both deliveries handled", func() bool { return r.queueDepth(t, r.queues.URL) == 0 })
	if r.count(t, ledgerOf, w.ID) != 2 || r.count(t, `SELECT count(*) FROM inbox_messages`) != 1 {
		t.Fatal("redelivery applied twice")
	}
}

// Same operation through HTTP (the use case) and SQS: applied once.
func TestSame_operation_via_HTTP_and_SQS_is_applied_once(t *testing.T) {
	r := newRig(t)
	w := r.open(t, "100.00")
	req := requestFor(t, w, "tx-cross")
	if _, err := r.wagering.Submit(context.Background(), app.SubmitCommand{Request: req, AuthenticatedProvider: "provider-a"}); err != nil {
		t.Fatal(err)
	}
	r.start(t, nil)
	r.send(t, w.ID.String(), envelope(w, "msg-cross", "tx-cross", "BET", "25.00"))
	eventually(t, "sqs copy handled", func() bool { return r.queueDepth(t, r.queues.URL) == 0 })
	if r.count(t, ledgerOf, w.ID) != 2 {
		t.Fatal("cross-channel duplicate moved money")
	}
}

func TestPoison_messages_go_to_the_DLQ_without_effects(t *testing.T) {
	r := newRig(t)
	w := r.open(t, "100.00")
	r.start(t, nil)
	r.send(t, "g1", `{"not":"an envelope"}`)
	r.send(t, w.ID.String(), envelope(w, "msg-x", "tx-x", "BET", "1.00"))
	r.send(t, w.ID.String(), envelope(w, "msg-x", "tx-other", "BET", "2.00")) // same messageId, other content
	r.send(t, w.ID.String(), envelope(w, "msg-open", "tx-open", "OPENING", "1.00"))
	eventually(t, "queue drained", func() bool { return r.queueDepth(t, r.queues.URL) == 0 })
	var codes []string
	eventually(t, "dlq populated", func() bool {
		codes = append(codes, r.dlqCodes(t)...)
		return len(codes) >= 3
	})
	slices.Sort(codes)
	if !slices.Equal(codes, []string{"MALFORMED", "MALFORMED", "MESSAGE_ID_CONFLICT"}) {
		t.Fatalf("dlq codes = %v", codes)
	}
	if r.count(t, ledgerOf, w.ID) != 2 {
		t.Fatal("only msg-x may have moved money")
	}
}

func TestUnauthorized_sender_is_rejected_to_the_DLQ(t *testing.T) {
	r := newRig(t, "999999999999") // the emulator account is not trusted here
	w := r.open(t, "100.00")
	r.start(t, nil)
	r.send(t, w.ID.String(), envelope(w, "msg-spoof", "tx-spoof", "BET", "1.00"))
	eventually(t, "dlq", func() bool { return slices.Contains(r.dlqCodes(t), "UNAUTHORIZED_SENDER") })
	if r.count(t, ledgerOf, w.ID) != 1 {
		t.Fatal("unauthorized message moved money")
	}
}

// Mandatory scenario 5: the consumer commits and dies before deleting; the
// redelivered message is handled by another consumer without moving money again.
func TestCrash_after_commit_before_delete_is_redelivered_safely(t *testing.T) {
	r := newRig(t)
	w := r.open(t, "100.00")
	stopA := r.start(t, failingDeletes{API: r.client})
	r.send(t, w.ID.String(), envelope(w, "msg-crash", "tx-crash", "BET", "25.00"))
	eventually(t, "first consumer committed", func() bool { return r.count(t, ledgerOf, w.ID) == 2 })
	stopA()
	if r.queueDepth(t, r.queues.URL) != 1 {
		t.Fatal("message should still be in the queue")
	}
	r.start(t, nil) // another consumer
	eventually(t, "redelivery handled and deleted", func() bool { return r.queueDepth(t, r.queues.URL) == 0 })
	if r.count(t, ledgerOf, w.ID) != 2 || r.count(t, `SELECT count(*) FROM inbox_messages`) != 1 {
		t.Fatal("redelivery moved money twice")
	}
}

// A transient failure keeps per-wallet FIFO order: BET 80 then BET 30 on 100,
// while the wallet is locked, must end with the first processed and the second
// rejected — never the other way around.
func TestTransient_failure_preserves_group_order(t *testing.T) {
	r := newRig(t)
	w := r.open(t, "100.00")
	locked, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = postgres.NewUnitOfWork(r.db.App).Do(context.Background(), func(ctx context.Context, tx app.Tx) error {
			if _, err := tx.Wallets().GetForUpdate(ctx, w.ID); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	r.send(t, w.ID.String(), envelope(w, "msg-80", "bet-80", "BET", "80.00"))
	r.send(t, w.ID.String(), envelope(w, "msg-30", "bet-30", "BET", "30.00"))
	r.start(t, nil)
	time.Sleep(2500 * time.Millisecond) // first attempts hit the lock timeout (1 s) and back off
	close(release)
	eventually(t, "both handled", func() bool { return r.queueDepth(t, r.queues.URL) == 0 })
	statuses := map[string]string{}
	rows, _ := r.db.Owner.Query(context.Background(), `SELECT external_transaction_id, status FROM wager_transactions WHERE origin = 'EXTERNAL'`)
	for rows.Next() {
		var ext, st string
		_ = rows.Scan(&ext, &st)
		statuses[ext] = st
	}
	rows.Close()
	if statuses["bet-80"] != "PROCESSED" || statuses["bet-30"] != "REJECTED" {
		t.Fatalf("order not preserved: %v", statuses)
	}
}
