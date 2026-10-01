// Package bootstrap composes the application graph with Uber Fx. It is the only
// package that knows every module; the domain never imports Fx.
package bootstrap

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/awsx"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/httpapi"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/oidc"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/snspublisher"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/sqsconsumer"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
	"github.com/fredzolio/backend-go-jungle/internal/platform/health"
	"github.com/fredzolio/backend-go-jungle/internal/platform/metrics"
	"github.com/fredzolio/backend-go-jungle/internal/workers"
)

const healthGroup = `group:"health_checks"`

// New returns the full Fx application for an already-parsed configuration.
func New(cfg config.Config, log *slog.Logger) *fx.App { return fx.New(Options(cfg, log)) }

// Options is the complete graph (exposed so tests can validate it).
func Options(cfg config.Config, log *slog.Logger) fx.Option {
	return fx.Options(
		fx.Supply(cfg, log),
		fx.WithLogger(func() fxevent.Logger { return &fxevent.SlogLogger{Logger: log} }),
		fx.StartTimeout(cfg.Lifecycle.StartTimeout),
		fx.StopTimeout(cfg.Lifecycle.StopTimeout),
		platformModule,
		postgresModule,
		messagingModule,
		metricsModule,
		appModule,
		httpModule,
		fx.Options(roleModules(cfg)...),
	)
}

// roleModules enables the background roles configured for this process.
func roleModules(cfg config.Config) []fx.Option {
	var opts []fx.Option
	if cfg.HasRole("resolver") {
		opts = append(opts, resolverModule)
	}
	if cfg.HasRole("consumer") {
		opts = append(opts, consumerModule)
	}
	if cfg.HasRole("outbox") {
		opts = append(opts, outboxModule)
	}
	if cfg.HasRole("reconciler") {
		opts = append(opts, reconcilerModule)
	}
	return opts
}

var appModule = fx.Module("app",
	fx.Provide(
		func(pool *pgxpool.Pool) app.UnitOfWork { return postgres.NewUnitOfWork(pool) },
		func(uow app.UnitOfWork, m app.Metrics) app.Deps {
			return app.Deps{UoW: uow, Clock: app.SystemClock{}, IDs: app.UUIDv7{}, Metrics: m}
		},
		app.NewWallets,
		app.NewReconciler,
		app.NewQueries,
		func(d app.Deps, cfg config.Config) *app.Wagering {
			r := cfg.Reference
			return app.NewWagering(d, app.ReferencePolicy{InitialBackoff: r.InitialBackoff, MaxBackoff: r.MaxBackoff, TTL: r.TTL})
		},
	),
)

var consumerModule = fx.Module("consumer",
	fx.Invoke(func(lc fx.Lifecycle, client awsx.ConsumerSQS, uc *app.Wagering, cfg config.Config, log *slog.Logger, p *metrics.Prom) error {
		c := cfg.Consumer
		senders, err := sqsconsumer.LoadSenders(c.SendersFile, c.TrustedAccounts)
		if err != nil {
			return err
		}
		workers.Loop{Name: "sqs-consumer", Copies: c.Pollers, Log: log, Run: func(ctx context.Context) {
			queue, err := awsx.QueueURL(ctx, client.Client, cfg.AWS.IngressQueue)
			if err != nil {
				return
			}
			dlq, err := awsx.QueueURL(ctx, client.Client, cfg.AWS.IngressDLQ)
			if err != nil {
				return
			}
			sqsconsumer.New(sqsconsumer.Deps{API: client, Ingest: uc, Senders: senders, Log: log, Observer: p}, sqsconsumer.Config{
				QueueURL: queue, DLQURL: dlq, ConsumerName: c.Name, ProcessTimeout: c.ProcessTimeout,
				MaxBackoff: c.MaxBackoff, MaxMessages: c.MaxMessages, WaitSeconds: c.WaitSeconds,
			}).Run(ctx)
		}}.Register(lc)
		return nil
	}),
)

var outboxModule = fx.Module("outbox",
	fx.Provide(awsx.NewPublisherSNS),
	fx.Invoke(func(lc fx.Lifecycle, pool *pgxpool.Pool, client awsx.PublisherSNS, cfg config.Config, log *slog.Logger, m app.Metrics) {
		o := cfg.Outbox
		relay := app.NewRelay(app.RelayConfig{
			Store: postgres.NewOutboxRelay(pool), Publisher: snspublisher.New(client, cfg.AWS.EventsTopic),
			Clock: app.SystemClock{}, Metrics: m, Log: log, Owner: cfg.InstanceID,
			Policy: app.RelayPolicy{Lease: o.Lease, InitialBackoff: o.InitialBackoff, MaxBackoff: o.MaxBackoff, MaxAttempts: o.MaxAttempts},
		})
		workers.Poller{Name: "outbox-relay", Run: relay.RunOnce, Log: log, Interval: o.PollInterval, Batch: o.Batch}.Register(lc)
	}),
)

// useCases groups the application services injected into the HTTP adapter.
type useCases struct {
	fx.In

	Wallets    *app.Wallets
	Wagering   *app.Wagering
	Queries    *app.Queries
	Reconciler *app.Reconciler
}

var reconcilerModule = fx.Module("reconciler",
	fx.Invoke(func(lc fx.Lifecycle, r *app.Reconciler, cfg config.Config, log *slog.Logger) {
		workers.Poller{Name: "reconciliation-sweep", Run: r.Sweep, Log: log, Interval: cfg.ReconcileInterval, Batch: 200}.Register(lc)
	}),
)

var resolverModule = fx.Module("resolver",
	fx.Invoke(func(lc fx.Lifecycle, uc *app.Wagering, cfg config.Config, log *slog.Logger) {
		workers.Poller{
			Name: "reference-resolver", Run: uc.ResolveDue, Log: log,
			Interval: cfg.Reference.PollInterval, Batch: cfg.Reference.Batch,
		}.Register(lc)
	}),
)

var platformModule = fx.Module("platform",
	fx.Provide(fx.Annotate(health.NewRegistry, fx.ParamTags(``, healthGroup))),
)

var postgresModule = fx.Module("postgres",
	fx.Provide(
		postgres.NewPool,
		fx.Annotate(postgres.HealthCheck, fx.ResultTags(healthGroup)),
	),
)

var messagingModule = fx.Module("messaging",
	fx.Provide(
		awsx.NewConsumerSQS,
		fx.Annotate(awsx.IngressHealthCheck, fx.ResultTags(healthGroup)),
	),
)

var httpModule = fx.Module("http",
	fx.Provide(
		func(cfg config.Config) (httpapi.TokenVerifier, error) {
			return oidc.NewVerifier(oidc.Config{Issuer: cfg.OIDC.Issuer, JWKSURL: cfg.OIDC.JWKSURL, Audience: cfg.OIDC.Audience})
		},
		func(h *health.Registry, v httpapi.TokenVerifier, log *slog.Logger, p *metrics.Prom, uc useCases) http.Handler {
			return httpapi.NewHandler(httpapi.Routes{Health: h, Verifier: v, Observer: p, Log: log,
				Handlers: httpapi.Handlers{Wallets: uc.Wallets, Wagering: uc.Wagering, Queries: uc.Queries, Reconciler: uc.Reconciler, Log: log}})
		},
	),
	fx.Invoke(httpapi.RegisterServer),
)
