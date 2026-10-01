//go:build integration

package bootstrap_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/goleak"

	"github.com/fredzolio/backend-go-jungle/internal/bootstrap"
	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
	"github.com/fredzolio/backend-go-jungle/internal/testsupport/awstest"
	"github.com/fredzolio/backend-go-jungle/internal/testsupport/pgtest"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// The real application (every role) starts against real dependencies, serves,
// and stops cleanly: workers end, servers close, the pool is released and no
// goroutine is leaked.
func TestApplication_starts_serves_and_stops_without_leaks(t *testing.T) {
	ctx := context.Background()
	pg, err := pgtest.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Stop(ctx) })
	ms, err := awstest.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Stop(ctx) })
	db := pg.NewDatabase(t)
	queues := ms.NewQueues(t, 30, 5)
	topic := ms.NewTopic(t)

	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	host, port, _ := net.SplitHostPort(pg.HostPort())
	cfg := config.Config{
		InstanceID: "lifecycle", Roles: []string{"resolver", "consumer", "outbox", "reconciler"},
		HTTP:        config.HTTP{Addr: freeAddr(t), ReadHeaderTimeout: time.Second, DrainDelay: 10 * time.Millisecond},
		MetricsAddr: freeAddr(t), ReconcileInterval: time.Second,
		Lifecycle: config.Lifecycle{StartTimeout: 30 * time.Second, StopTimeout: 15 * time.Second},
		Reference: config.Reference{InitialBackoff: time.Second, MaxBackoff: time.Second, TTL: time.Minute, PollInterval: 100 * time.Millisecond, Batch: 10},
		Postgres: config.Postgres{Host: host, Port: mustPort(t, port), Name: db.Name, User: "jungle_app", Password: pgtest.RolePassword,
			MaxConns: 4, LockTimeout: time.Second, StatementTimeout: 5 * time.Second},
		AWS: config.AWS{EndpointURL: ms.Endpoint, Region: "us-east-1", IngressQueue: queueName(queues.URL), IngressDLQ: queueName(queues.DLQURL),
			EventsTopic: topic.ARN, Consumer: config.Credentials{AccessKeyID: "test", SecretAccessKey: "test"},
			Publisher: config.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}},
		OIDC: config.OIDC{Issuer: "http://idp.invalid/realms/jungle", JWKSURL: "http://idp.invalid/certs", Audience: "jungle-api"},
		Consumer: config.Consumer{Name: "wager-ingress", SendersFile: write("senders.json", `{}`), ProcessTimeout: 5 * time.Second,
			MaxBackoff: time.Second, Pollers: 2, MaxMessages: 10, WaitSeconds: 1},
		Outbox: config.Outbox{Lease: 10 * time.Second, InitialBackoff: time.Second, MaxBackoff: time.Second, PollInterval: 100 * time.Millisecond, MaxAttempts: 3, Batch: 10},
	}
	before := goleak.IgnoreCurrent()

	app := fx.New(bootstrap.Options(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))))
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, url := range []string{"http://" + cfg.HTTP.Addr + "/health/ready", "http://" + cfg.MetricsAddr + "/metrics"} {
		res, err := http.Get(url) //nolint:noctx // test
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %v %v", url, res, err)
		}
		res.Body.Close()
	}
	time.Sleep(300 * time.Millisecond) // let every worker run a few rounds
	stopCtx, cancelStop := context.WithTimeout(ctx, 15*time.Second)
	defer cancelStop()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	http.DefaultClient.CloseIdleConnections()
	goleak.VerifyNone(t, before, goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreAnyFunction("internal/poll.runtime_pollWait"))
}

func mustPort(t *testing.T, s string) uint16 {
	t.Helper()
	p, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(p)
}

func queueName(url string) string { return url[strings.LastIndex(url, "/")+1:] }
