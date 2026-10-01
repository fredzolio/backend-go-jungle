// Package postgres holds the pgx connection pool and, later, the repositories.
package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
	"github.com/fredzolio/backend-go-jungle/internal/platform/health"
)

// NewPool builds the pool and binds it to the Fx lifecycle: start waits until the
// database answers (bounded by the Fx start timeout), stop closes it last because
// every consumer of the pool depends on it and is therefore stopped first.
func NewPool(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.Postgres.DSN(cfg.InstanceID))
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}
	pc.MaxConns = cfg.Postgres.MaxConns
	pc.MaxConnIdleTime = 5 * time.Minute
	pc.HealthCheckPeriod = 30 * time.Second

	// Fx constructors carry no context; NewWithConfig does not dial (MinConns=0).
	pool, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error { return waitReady(ctx, pool, log) },
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}

func waitReady(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	backoff := 250 * time.Millisecond
	for {
		err := pool.Ping(ctx)
		if err == nil {
			return nil
		}
		log.InfoContext(ctx, "waiting for postgres", slog.Any("error", err), slog.Duration("retry_in", backoff))
		select {
		case <-ctx.Done():
			return fmt.Errorf("postgres not ready: %w", err)
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 2*time.Second)
	}
}

// HealthCheck exposes the pool as a readiness probe.
func HealthCheck(pool *pgxpool.Pool) health.Check {
	return health.Check{Name: "postgres", Probe: pool.Ping}
}
