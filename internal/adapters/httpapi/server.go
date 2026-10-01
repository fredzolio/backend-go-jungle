// Package httpapi is the HTTP adapter: routing, handlers and server lifecycle.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
	"github.com/fredzolio/backend-go-jungle/internal/platform/health"
)

// ServerParams groups the dependencies of the HTTP server.
type ServerParams struct {
	fx.In

	Lifecycle  fx.Lifecycle
	Shutdowner fx.Shutdowner
	Log        *slog.Logger
	Health     *health.Registry
	Handler    http.Handler
	Config     config.Config
}

// RegisterServer starts the listener on Fx start and, on stop, drains: readiness
// goes DOWN, the edge stops routing, then in-flight requests finish.
func RegisterServer(p ServerParams) {
	cfg := p.Config.HTTP
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           p.Handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(p.Log.Handler(), slog.LevelWarn),
	}
	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.Addr)
			if err != nil {
				return fmt.Errorf("listen %s: %w", cfg.Addr, err)
			}
			go serve(srv, ln, p)
			p.Log.InfoContext(ctx, "http server listening", slog.String("addr", cfg.Addr))
			return nil
		},
		OnStop: func(ctx context.Context) error {
			p.Health.StartDraining()
			select {
			case <-time.After(cfg.DrainDelay):
			case <-ctx.Done():
			}
			if err := srv.Shutdown(ctx); err != nil {
				return fmt.Errorf("http shutdown: %w", err)
			}
			p.Log.InfoContext(ctx, "http server stopped")
			return nil
		},
	})
}

func serve(srv *http.Server, ln net.Listener, p ServerParams) {
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		p.Log.Error("http server failed", slog.Any("error", err))
		if shutdownErr := p.Shutdowner.Shutdown(fx.ExitCode(1)); shutdownErr != nil {
			p.Log.Error("request shutdown", slog.Any("error", shutdownErr))
		}
	}
}
