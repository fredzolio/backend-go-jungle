// Package awsx builds AWS SDK clients. Each component receives a client bound to
// its own IAM principal so broker policies enforce least privilege.
package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
	"github.com/fredzolio/backend-go-jungle/internal/platform/health"
)

// ConsumerSQS is the SQS client authenticated as the ingress consumer.
type ConsumerSQS struct{ *sqs.Client }

// NewConsumerSQS builds the consumer's SQS client.
func NewConsumerSQS(cfg config.Config) ConsumerSQS {
	return ConsumerSQS{newSQS(cfg.AWS, cfg.AWS.Consumer)}
}

func newSQS(cfg config.AWS, creds config.Credentials) *sqs.Client {
	awsCfg := aws.Config{
		Region:           cfg.Region,
		Credentials:      credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, ""),
		RetryMaxAttempts: 3,
	}
	return sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
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
