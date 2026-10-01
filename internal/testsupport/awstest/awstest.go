//go:build integration

// Package awstest runs a real MiniStack (SQS/SNS) for integration tests and
// creates isolated FIFO queues per test.
package awstest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Env is one running MiniStack.
type Env struct {
	container testcontainers.Container
	Endpoint  string
}

// Start boots MiniStack.
func Start(ctx context.Context) (*Env, error) {
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "ministackorg/ministack:1.5.19",
			ExposedPorts: []string{"4566/tcp"},
			WaitingFor:   wait.ForHTTP("/_ministack/health").WithPort("4566/tcp"),
		},
		Started: true,
	})
	if err != nil {
		return nil, fmt.Errorf("start ministack: %w", err)
	}
	endpoint, err := c.PortEndpoint(ctx, "4566/tcp", "http")
	if err != nil {
		return nil, err
	}
	return &Env{container: c, Endpoint: endpoint}, nil
}

// Stop terminates the container.
func (e *Env) Stop(ctx context.Context) error { return e.container.Terminate(ctx) }

// SQS returns a client with the emulator root credentials.
func (e *Env) SQS() *sqs.Client {
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(e.Endpoint) })
}

// Queues is one isolated ingress queue with its DLQ.
type Queues struct {
	URL, DLQURL string
}

// NewQueues creates a FIFO queue + DLQ pair (redrive after maxReceive deliveries).
func (e *Env) NewQueues(t *testing.T, visibilitySeconds, maxReceive int) Queues {
	t.Helper()
	ctx := context.Background()
	client := e.SQS()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "ingress-" + hex.EncodeToString(suffix)
	dlq, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name + "-dlq.fifo"),
		Attributes: map[string]string{"FifoQueue": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	dlqAttrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: dlq.QueueUrl,
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatal(err)
	}
	redrive, _ := json.Marshal(map[string]any{"deadLetterTargetArn": dlqAttrs.Attributes["QueueArn"], "maxReceiveCount": maxReceive})
	q, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name + ".fifo"), Attributes: map[string]string{
		"FifoQueue": "true", "VisibilityTimeout": fmt.Sprint(visibilitySeconds), "RedrivePolicy": string(redrive),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return Queues{URL: aws.ToString(q.QueueUrl), DLQURL: aws.ToString(dlq.QueueUrl)}
}
