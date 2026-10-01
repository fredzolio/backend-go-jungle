// Package bootstrap composes the application graph with Uber Fx. It is the only
// package that knows every module; the domain never imports Fx.
package bootstrap

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/awsx"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/httpapi"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/oidc"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
	"github.com/fredzolio/backend-go-jungle/internal/platform/health"
	"github.com/fredzolio/backend-go-jungle/internal/workers"
)

const healthGroup = `group:"health_checks"`

// New returns the full Fx application for an already-parsed configuration.
func New(cfg config.Config, log *slog.Logger) *fx.App {
	return fx.New(
		fx.Supply(cfg, log),
		fx.WithLogger(func() fxevent.Logger { return &fxevent.SlogLogger{Logger: log} }),
		fx.StartTimeout(cfg.Lifecycle.StartTimeout),
		fx.StopTimeout(cfg.Lifecycle.StopTimeout),
		platformModule,
		postgresModule,
		messagingModule,
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
	return opts
}

var appModule = fx.Module("app",
	fx.Provide(
		func(pool *pgxpool.Pool) app.UnitOfWork { return postgres.NewUnitOfWork(pool) },
		func(uow app.UnitOfWork) app.Deps {
			return app.Deps{UoW: uow, Clock: app.SystemClock{}, IDs: app.UUIDv7{}}
		},
		app.NewWallets,
		app.NewQueries,
		func(d app.Deps, cfg config.Config) *app.Wagering {
			r := cfg.Reference
			return app.NewWagering(d, app.ReferencePolicy{InitialBackoff: r.InitialBackoff, MaxBackoff: r.MaxBackoff, TTL: r.TTL})
		},
	),
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
		func(h *health.Registry, v httpapi.TokenVerifier, log *slog.Logger, w *app.Wallets, wg *app.Wagering, q *app.Queries) http.Handler {
			return httpapi.NewHandler(httpapi.Routes{Health: h, Verifier: v, Log: log,
				Handlers: httpapi.Handlers{Wallets: w, Wagering: wg, Queries: q, Log: log}})
		},
	),
	fx.Invoke(httpapi.RegisterServer),
)
