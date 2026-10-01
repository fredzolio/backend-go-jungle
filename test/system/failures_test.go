//go:build system

package system

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

func (c *cluster) enqueue(t *testing.T, w wallet, messageID, ext string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"messageId": messageID, "type": "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano), "data": func() map[string]any {
			d := op(w, ext, "BET", "25.00", "")
			d["idempotencyKey"] = "provider-a:" + ext
			return d
		}()})
	_, err := ministack.SQS().SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: aws.String(c.queues.URL),
		MessageBody: aws.String(string(body)), MessageGroupId: aws.String(w.id), MessageDeduplicationId: aws.String(uuid.NewString())})
	if err != nil {
		t.Fatal(err)
	}
}

// The same operation via HTTP (one process) and SQS (consumed by others).
func TestHTTP_and_SQS_copies_of_an_operation_apply_once_across_processes(t *testing.T) {
	c := newCluster(t)
	api := c.start("api", "resolver", "")
	c.start("consumer-1", "consumer", "")
	c.start("consumer-2", "consumer", "")
	w := c.openWallet(t, "100.00")
	c.enqueue(t, w, "msg-cross", "tx-cross")
	if r := c.submit(t, api, op(w, "tx-cross", "BET", "25.00", "")); r.status != http.StatusOK {
		t.Fatalf("http: %d %v", r.status, r.body)
	}
	eventually(t, "sqs copy consumed", 20*time.Second, func() bool {
		return c.count(`SELECT count(*) FROM inbox_messages WHERE message_id = 'msg-cross'`) == 1
	})
	if ledgerOf(c, w) != 2 || balanceOf(c, w) != 7500 {
		t.Fatal("cross-channel copy moved money twice")
	}
	c.assertLedgerMatchesBalances()
}

// Scenario 5: a consumer process dies right after committing, before deleting the
// message; another process gets the redelivery and does not move money again.
func TestConsumer_process_killed_after_commit_before_delete(t *testing.T) {
	c := newCluster(t)
	c.start("api", "resolver", "")
	doomed := c.startDoomed("consumer-doomed", "consumer", "consumer.after_commit")
	w := c.openWallet(t, "100.00")
	c.enqueue(t, w, "msg-crash", "tx-crash")
	c.waitExit(doomed, 20*time.Second)
	if ledgerOf(c, w) != 2 {
		t.Fatal("the doomed consumer should have committed before dying")
	}
	c.start("consumer-survivor", "consumer", "")
	eventually(t, "redelivery handled", 30*time.Second, func() bool {
		out, err := ministack.SQS().GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(c.queues.URL),
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
		return err == nil && out.Attributes["ApproximateNumberOfMessages"] == "0" && out.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
	})
	if ledgerOf(c, w) != 2 || c.count(`SELECT count(*) FROM inbox_messages`) != 1 {
		t.Fatal("redelivery moved money twice")
	}
	c.assertLedgerMatchesBalances()
}

// Scenario 6: a relay process dies between publishing and confirming; another
// process republishes the same events (same eventIds) after the lease expires.
func TestRelay_process_killed_between_publish_and_confirm(t *testing.T) {
	c := newCluster(t)
	c.start("api", "resolver", "")
	w := c.openWallet(t, "100.00")
	c.submit(t, c.pick(), op(w, "bet-1", "BET", "10.00", ""))
	doomed := c.startDoomed("relay-doomed", "outbox", "relay.after_publish")
	c.waitExit(doomed, 20*time.Second)
	if c.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL`) != 0 {
		t.Fatal("nothing should be confirmed by the doomed relay")
	}
	c.start("relay-survivor", "outbox", "")
	eventually(t, "all events published", 30*time.Second, func() bool {
		return c.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})
	if c.count(`SELECT max(attempts) FROM outbox_events`) < 2 {
		t.Fatal("the head event should have been published again by the survivor")
	}
}

// Scenarios 7 + 8: a REFUND parked as PENDING_REFERENCE on a process that is then
// killed is resumed by another process once its BET arrives.
func TestPending_reference_survives_the_process_that_accepted_it(t *testing.T) {
	c := newCluster(t)
	first := c.start("p0", allRoles, "")
	c.start("p1", allRoles, "")
	w := c.openWallet(t, "100.00")
	if r := c.submit(t, first, op(w, "refund-1", "REFUND", "25.00", "bet-1")); r.status != http.StatusAccepted {
		t.Fatalf("refund: %d %v", r.status, r.body)
	}
	c.kill(first)
	if r := c.submit(t, c.pick(), op(w, "bet-1", "BET", "25.00", "")); r.status != http.StatusOK {
		t.Fatalf("bet: %d %v", r.status, r.body)
	}
	eventually(t, "refund resumed by the surviving process", 20*time.Second, func() bool {
		return c.count(`SELECT count(*) FROM wager_transactions WHERE external_transaction_id = 'refund-1' AND status = 'PROCESSED'`) == 1
	})
	if balanceOf(c, w) != 10000 {
		t.Fatalf("balance = %d", balanceOf(c, w))
	}
	c.assertLedgerMatchesBalances()
}

// Scenario 8: kill every process; new processes keep idempotency (original
// results on replay), resume pending work and keep the books consistent.
func TestFull_restart_preserves_idempotency_pending_and_consistency(t *testing.T) {
	c := newCluster(t)
	for i := range 3 {
		c.start(fmt.Sprintf("old-%d", i), allRoles, "")
	}
	w := c.openWallet(t, "100.00")
	first := c.submit(t, c.pick(), op(w, "bet-1", "BET", "30.00", ""))
	c.submit(t, c.pick(), op(w, "bet-2", "BET", "20.00", ""))
	c.submit(t, c.pick(), op(w, "rollback-1", "ROLLBACK", "40.00", "win-later")) // pending
	c.mu.Lock()
	old := append([]*process(nil), c.procs...)
	c.mu.Unlock()
	for _, p := range old {
		c.kill(p)
	}
	for i := range 3 {
		c.start(fmt.Sprintf("new-%d", i), allRoles, "")
	}
	replay := c.submit(t, c.pick(), op(w, "bet-1", "BET", "30.00", ""))
	if replay.body["idempotentReplay"] != true || replay.body["transactionId"] != first.body["transactionId"] ||
		replay.body["balance"].(map[string]any)["amount"] != "70.00" {
		t.Fatalf("replay after restart: %v", replay.body)
	}
	c.submit(t, c.pick(), op(w, "win-later", "WIN", "40.00", ""))
	eventually(t, "pending rollback resumed after restart", 20*time.Second, func() bool {
		return c.count(`SELECT count(*) FROM wager_transactions WHERE external_transaction_id = 'rollback-1' AND status = 'PROCESSED'`) == 1
	})
	if balanceOf(c, w) != 5000 {
		t.Fatalf("balance = %d", balanceOf(c, w))
	}
	r := c.pick().call(t, "POST", "/wallets/"+w.id+"/reconciliation", idp.Internal(t), nil)
	if r.body["consistent"] != true {
		t.Fatalf("reconciliation: %v", r.body)
	}
	c.assertLedgerMatchesBalances()
}
