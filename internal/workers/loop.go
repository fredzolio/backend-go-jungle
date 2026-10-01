package workers

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"go.uber.org/fx"
)

// Loop runs Copies goroutines of a long-running Run function (e.g. SQS pollers)
// bound to the lifecycle. Run must return promptly once its context is cancelled.
type Loop struct {
	Run    func(ctx context.Context)
	Log    *slog.Logger
	Name   string
	Copies int
}

// Register binds the loop to the lifecycle.
func (l Loop) Register(lc fx.Lifecycle) {
	var (
		cancel context.CancelFunc
		wg     sync.WaitGroup
	)
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, c := context.WithCancel(context.Background())
			cancel = c
			for range max(l.Copies, 1) {
				wg.Go(func() { l.Run(ctx) })
			}
			l.Log.Info("worker started", slog.String("worker", l.Name), slog.Int("copies", max(l.Copies, 1)))
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
				l.Log.Info("worker stopped", slog.String("worker", l.Name))
				return nil
			case <-ctx.Done():
				return fmt.Errorf("worker %s did not stop in time: %w", l.Name, ctx.Err())
			}
		},
	})
}
