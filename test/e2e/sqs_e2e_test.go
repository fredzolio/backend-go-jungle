//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
)

// producer returns an SQS client authenticated as the provider's IAM producer
// (MiniStack runs with AUTH=true: the broker policy is enforced).
func producer(t *testing.T, provider string) *sqs.Client {
	t.Helper()
	read := func(name string) string {
		raw, err := os.ReadFile(provisioned(t, "aws/producer-"+provider+"/"+name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(raw))
	}
	endpoint := os.Getenv("JUNGLE_SQS_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:14566"
	}
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(read("access_key_id"), read("secret_access_key"), "")}
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(endpoint) })
}

func publish(t *testing.T, c *sqs.Client, w map[string]any, ext string) {
	t.Helper()
	ctx := context.Background()
	q, err := c.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("wager-transactions.fifo")})
	if err != nil {
		t.Fatal(err)
	}
	messageID := "msg-" + uuid.NewString()
	body, _ := json.Marshal(map[string]any{
		"messageId": messageID, "type": "WagerTransactionRequested", "occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"providerId": "provider-a", "externalTransactionId": ext, "idempotencyKey": "provider-a:" + ext,
			"playerId": w["playerId"], "walletId": w["id"], "roundId": "round-e2e", "gameId": "fortune-chimp",
			"kind": "BET", "money": map[string]string{"amount": "30.00", "currency": "BRL"},
		},
	})
	_, err = c.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: q.QueueUrl, MessageBody: aws.String(string(body)),
		MessageGroupId: aws.String(w["id"].(string)), MessageDeduplicationId: aws.String(messageID)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSQS_operation_reaches_the_wallet_and_matches_HTTP_idempotency(t *testing.T) {
	w := openWallet(t, "100.00")
	ext := "sqs-" + uuid.NewString()[:8]
	publish(t, producer(t, "provider-a"), w, ext)
	tokA := token(t, "provider-a")
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := call(t, "GET", "/providers/provider-a/wagering/transactions/"+ext, tokA, nil)
		if r.status == http.StatusOK && r.body["status"] == "PROCESSED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SQS operation not processed: %d %v", r.status, r.body)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The same operation through HTTP is a replay of the SQS one.
	replay := call(t, "POST", "/wagering/transactions", tokA, map[string]any{
		"providerId": "provider-a", "externalTransactionId": ext, "playerId": w["playerId"], "walletId": w["id"],
		"roundId": "round-e2e", "gameId": "fortune-chimp", "kind": "BET", "money": map[string]string{"amount": "30.00", "currency": "BRL"},
	}, "Idempotency-Key", "provider-a:"+ext)
	if replay.status != http.StatusOK || replay.body["idempotentReplay"] != true {
		t.Fatalf("HTTP copy of the SQS operation: %d %v", replay.status, replay.body)
	}
	wallet := call(t, "GET", "/wallets/"+w["id"].(string), token(t, "jungle-internal"), nil)
	if wallet.body["balance"].(map[string]any)["amount"] != "70.00" {
		t.Fatalf("wallet = %v", wallet.body)
	}
}

func TestSQS_broker_policy_denies_receive_to_producers(t *testing.T) {
	c := producer(t, "provider-a")
	q, err := c.GetQueueUrl(context.Background(), &sqs.GetQueueUrlInput{QueueName: aws.String("wager-transactions.fifo")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl}); err == nil {
		t.Fatal("producer credentials must not consume the ingress queue")
	}
}
