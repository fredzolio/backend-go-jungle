// Package bootstrap composes the application graph with Uber Fx. It is the only
// package that knows every module; the domain never imports Fx.
package bootstrap

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/awsx"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/httpapi"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
	"github.com/fredzolio/backend-go-jungle/internal/platform/health"
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
		httpModule,
	)
}

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
	fx.Provide(httpapi.NewMux),
	fx.Invoke(httpapi.RegisterServer),
)
