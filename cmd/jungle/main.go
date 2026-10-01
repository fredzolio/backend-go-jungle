// Command jungle is the single binary of the wallet service.
//
//	jungle serve                         run the service (default)
//	jungle migrate up|down|reset|status  apply or revert database migrations
//	jungle healthcheck                   probe the local liveness endpoint (container healthcheck)
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/bootstrap"
	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
	"github.com/fredzolio/backend-go-jungle/internal/platform/logging"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		return serve()
	case "migrate":
		return migrate(args[1:])
	case "healthcheck":
		return healthcheck()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (expected: serve | migrate | healthcheck)\n", cmd)
		return 2
	}
}

func serve() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	log := logging.New(cfg.LogLevel, cfg.InstanceID)
	app := bootstrap.New(cfg, log)

	startCtx, cancelStart := context.WithTimeout(context.Background(), app.StartTimeout())
	defer cancelStart()
	if err := app.Start(startCtx); err != nil {
		log.Error("start failed", "error", err)
		return 1
	}
	signal := <-app.Wait()
	log.Info("stopping", "signal", signal.Signal.String())

	stopCtx, cancelStop := context.WithTimeout(context.Background(), app.StopTimeout())
	defer cancelStop()
	if err := app.Stop(stopCtx); err != nil {
		log.Error("stop failed", "error", err)
		return 1
	}
	return signal.ExitCode
}

func migrate(args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: jungle migrate %s\n", postgres.MigrateCommands)
		return 2
	}
	cfg, err := config.LoadMigrator()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	if err := postgres.Migrate(ctx, cfg.Postgres.DSN(cfg.InstanceID), args[0], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}
	return 0
}

func healthcheck() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/health/live", http.NoBody)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
