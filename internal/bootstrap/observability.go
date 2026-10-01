package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/awsx"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
	"github.com/fredzolio/backend-go-jungle/internal/platform/metrics"
	"github.com/fredzolio/backend-go-jungle/internal/workers"
)

var metricsModule = fx.Module("metrics",
	fx.Provide(
		metrics.New,
		func(p *metrics.Prom) app.Metrics { return p },
	),
	fx.Invoke(registerMetricsServer, registerGauges),
)

// registerMetricsServer serves /metrics on the internal port.
func registerMetricsServer(lc fx.Lifecycle, p *metrics.Prom, cfg config.Config, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", p.Handler())
	srv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.MetricsAddr)
			if err != nil {
				return fmt.Errorf("listen metrics %s: %w", cfg.MetricsAddr, err)
			}
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("metrics server failed", slog.Any("error", err))
				}
			}()
			return nil
		},
		OnStop: srv.Shutdown,
	})
}

// registerGauges refreshes backlog gauges (outbox lag, DLQ depth, pending
// references) every few seconds.
func registerGauges(lc fx.Lifecycle, p *metrics.Prom, pool *pgxpool.Pool, q *app.Queries, sqsClient awsx.ConsumerSQS, cfg config.Config, log *slog.Logger) {
	relayStore := postgres.NewOutboxRelay(pool)
	var dlqURL string
	workers.Poller{Name: "gauges", Log: log, Interval: 5 * time.Second, Batch: 1, Run: func(ctx context.Context, _ int) (int, error) {
		if oldest, pending, err := relayStore.Backlog(ctx, time.Now()); err == nil {
			p.SetOutboxBacklog(oldest, pending)
		}
		if n, err := q.PendingReferences(ctx); err == nil {
			p.SetPendingReferences(n)
		}
		if dlqURL == "" {
			out, err := sqsClient.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(cfg.AWS.IngressDLQ)})
			if err != nil {
				return 0, nil //nolint:nilerr // SQS briefly unavailable: retry next round
			}
			dlqURL = aws.ToString(out.QueueUrl)
		}
		out, err := sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: aws.String(dlqURL),
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages}})
		if err == nil {
			if n, convErr := strconv.ParseInt(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)], 10, 64); convErr == nil {
				p.SetDLQDepth(n)
			}
		}
		return 0, nil
	}}.Register(lc)
}
