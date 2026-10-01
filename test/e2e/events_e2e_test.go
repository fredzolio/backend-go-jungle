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

func eventsReader(t *testing.T) *sqs.Client {
	t.Helper()
	read := func(name string) string {
		raw, err := os.ReadFile(provisioned(t, "aws/events-reader/"+name))
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

// The outbox relay publishes committed events to wager-events.fifo; the audit
// subscriber queue receives them with the typed envelope contract.
func TestCommitted_operation_events_reach_the_subscriber(t *testing.T) {
	w := openWallet(t, "100.00")
	ext := "evt-" + uuid.NewString()[:8]
	r := call(t, "POST", "/wagering/transactions", token(t, "provider-a"), betBody(w, "provider-a", ext, "40.00"), "Idempotency-Key", "provider-a:"+ext)
	if r.status != http.StatusOK {
		t.Fatalf("bet: %d %v", r.status, r.body)
	}
	reader := eventsReader(t)
	ctx := context.Background()
	q, err := reader.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("wager-events-audit.fifo")})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[string]any{}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && seen["WalletBalanceChanged:v2"] == nil {
		out, err := reader.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			var env map[string]any
			_ = json.Unmarshal([]byte(aws.ToString(m.Body)), &env)
			data, _ := env["data"].(map[string]any)
			if data["walletId"] == w["id"] {
				key := env["eventType"].(string)
				if v, ok := data["walletVersion"].(float64); ok {
					key += map[float64]string{1: ":v1", 2: ":v2"}[v]
				}
				seen[key] = env
			}
			_, _ = reader.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: q.QueueUrl, ReceiptHandle: m.ReceiptHandle})
		}
	}
	changed := seen["WalletBalanceChanged:v2"]
	if changed == nil {
		t.Fatalf("balance change event not received; saw %v", keys(seen))
	}
	data := changed["data"].(map[string]any)
	if data["direction"] != "DEBIT" || data["balanceAfter"].(map[string]any)["amount"] != "60.00" ||
		changed["eventId"] == nil || changed["version"] != float64(1) || changed["correlationId"] == nil {
		t.Fatalf("event contract: %v", changed)
	}
}

func keys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
