// Package awsx builds AWS SDK clients. Each component receives a client bound to
// its own IAM principal so broker policies enforce least privilege.
package awsx

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
	"github.com/fredzolio/backend-go-jungle/internal/platform/health"
)

// PublisherSNS is the SNS client authenticated as the outbox publisher.
type PublisherSNS struct{ *sns.Client }

// NewPublisherSNS builds the publisher's SNS client.
func NewPublisherSNS(cfg config.Config) PublisherSNS {
	return PublisherSNS{sns.NewFromConfig(awsConfig(cfg.AWS, cfg.AWS.Publisher), func(o *sns.Options) {
		if cfg.AWS.EndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.AWS.EndpointURL)
		}
	})}
}

// ConsumerSQS is the SQS client authenticated as the ingress consumer.
type ConsumerSQS struct{ *sqs.Client }

// NewConsumerSQS builds the consumer's SQS client.
func NewConsumerSQS(cfg config.Config) ConsumerSQS {
	return ConsumerSQS{newSQS(cfg.AWS, cfg.AWS.Consumer)}
}

func awsConfig(cfg config.AWS, creds config.Credentials) aws.Config {
	return aws.Config{
		Region:           cfg.Region,
		Credentials:      credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, ""),
		RetryMaxAttempts: 3,
	}
}

func newSQS(cfg config.AWS, creds config.Credentials) *sqs.Client {
	return sqs.NewFromConfig(awsConfig(cfg, creds), func(o *sqs.Options) {
		if cfg.EndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.EndpointURL)
		}
	})
}

// IngressHealthCheck probes that the ingress queue is reachable with the
// consumer's credentials.
func IngressHealthCheck(client ConsumerSQS, cfg config.Config) health.Check {
	queue := cfg.AWS.IngressQueue
	return health.Check{Name: "sqs", Probe: func(ctx context.Context) error {
		if _, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(queue)}); err != nil {
			return fmt.Errorf("get queue url %s: %w", queue, err)
		}
		return nil
	}}
}

// QueueURL resolves a queue URL, retrying until ctx ends (SQS may be briefly
// unavailable when a process starts).
func QueueURL(ctx context.Context, client *sqs.Client, name string) (string, error) {
	for {
		out, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
		if err == nil {
			return aws.ToString(out.QueueUrl), nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("resolve queue %s: %w", name, err)
		case <-time.After(time.Second):
		}
	}
}
