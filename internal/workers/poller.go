// Package workers runs background loops (reference resolver, outbox relay, ...)
// bound to the Fx lifecycle: started after their dependencies, stopped before
// them, with cancellation and an observable termination.
package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.uber.org/fx"
)

// Poller repeatedly calls Run. When a round handles a full batch it continues
// immediately (backlog); otherwise it sleeps Interval.
type Poller struct {
	Run      func(ctx context.Context, batch int) (int, error)
	Log      *slog.Logger
	Name     string
	Interval time.Duration
	Batch    int
}

// Register binds the poller to the lifecycle. On stop, the loop context is
// cancelled: an in-flight SQL transaction rolls back (nothing half-applied is
// ever committed) and the work is picked up again by any instance.
func (p Poller) Register(lc fx.Lifecycle) {
	var (
		cancel context.CancelFunc
		done   = make(chan struct{})
	)
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			// The loop outlives the start hook, so it gets its own root context.
			loopCtx, c := context.WithCancel(context.Background())
			cancel = c
			go p.loop(loopCtx, done)
			p.Log.Info("worker started", slog.String("worker", p.Name))
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			select {
			case <-done:
				p.Log.Info("worker stopped", slog.String("worker", p.Name))
				return nil
			case <-ctx.Done():
				return fmt.Errorf("worker %s did not stop in time: %w", p.Name, ctx.Err())
			}
		},
	})
}

func (p Poller) loop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	for {
		n, err := p.Run(ctx, p.Batch)
		switch {
		case errors.Is(err, context.Canceled) && ctx.Err() != nil:
			return
		case err != nil:
			p.Log.Warn("worker round failed", slog.String("worker", p.Name), slog.Any("error", err))
		case n > 0:
			p.Log.Debug("worker round", slog.String("worker", p.Name), slog.Int("handled", n))
		}
		if err == nil && n >= p.Batch {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.Interval):
		}
	}
}
