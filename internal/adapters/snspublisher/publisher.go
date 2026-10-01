// Package snspublisher publishes outbox events to the SNS FIFO topic
// wager-events.fifo.
//
// Routing contract: MessageGroupId = walletId (per-wallet order for every
// subscriber), MessageDeduplicationId = eventId (a republication of the same event
// is dropped by SNS within its 5-minute window; consumers must still deduplicate
// by eventId), message attributes eventType and eventVersion (filter policies),
// body = the immutable event envelope.
package snspublisher

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/app"
)

// API is the subset of the SNS client used.
type API interface {
	PublishBatch(ctx context.Context, in *sns.PublishBatchInput, opts ...func(*sns.Options)) (*sns.PublishBatchOutput, error)
}

// Publisher implements app.EventPublisher.
type Publisher struct {
	api      API
	topicARN string
}

// New builds a publisher for the topic.
func New(api API, topicARN string) *Publisher { return &Publisher{api: api, topicARN: topicARN} }

var _ app.EventPublisher = (*Publisher)(nil)

const (
	maxBatch             = 10 // SNS PublishBatch limit
	maxConcurrentBatches = 8
)

// Publish sends events in batches of 10 and reports a result per event. Chunks are
// sent concurrently: a claim holds at most one event per partition (wallet), so
// no two chunks carry events whose relative order matters.
func (p *Publisher) Publish(ctx context.Context, events []app.ClaimedEvent) map[uuid.UUID]error {
	results := make(map[uuid.UUID]error, len(events))
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, maxConcurrentBatches)
	)
	for start := 0; start < len(events); start += maxBatch {
		chunk := events[start:min(start+maxBatch, len(events))]
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			chunkResults := p.publishChunk(ctx, chunk)
			mu.Lock()
			maps.Copy(results, chunkResults)
			mu.Unlock()
		})
	}
	wg.Wait()
	return results
}

func (p *Publisher) publishChunk(ctx context.Context, chunk []app.ClaimedEvent) map[uuid.UUID]error {
	results := make(map[uuid.UUID]error, len(chunk))
	entries := make([]types.PublishBatchRequestEntry, 0, len(chunk))
	for _, e := range chunk {
		entries = append(entries, types.PublishBatchRequestEntry{
			Id: aws.String(e.ID.String()), Message: aws.String(string(e.Payload)),
			MessageGroupId: aws.String(e.PartitionKey), MessageDeduplicationId: aws.String(e.ID.String()),
			MessageAttributes: map[string]types.MessageAttributeValue{
				"eventType":    {DataType: aws.String("String"), StringValue: aws.String(e.EventType)},
				"eventVersion": {DataType: aws.String("Number"), StringValue: aws.String(strconv.Itoa(e.EventVersion))},
			},
		})
	}
	out, err := p.api.PublishBatch(ctx, &sns.PublishBatchInput{TopicArn: aws.String(p.topicARN), PublishBatchRequestEntries: entries})
	if err != nil {
		for _, e := range chunk {
			results[e.ID] = err
		}
		return results
	}
	for _, f := range out.Failed {
		if id, parseErr := uuid.Parse(aws.ToString(f.Id)); parseErr == nil {
			results[id] = fmt.Errorf("sns rejected event: %s %s", aws.ToString(f.Code), aws.ToString(f.Message))
		}
	}
	accepted := map[string]bool{}
	for _, s := range out.Successful {
		accepted[aws.ToString(s.Id)] = true
	}
	for _, e := range chunk {
		if results[e.ID] == nil && !accepted[e.ID.String()] {
			results[e.ID] = errors.New("sns did not report the event as published")
		}
	}
	return results
}
