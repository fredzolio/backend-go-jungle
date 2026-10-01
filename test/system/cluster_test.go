//go:build system

// Package system runs the real binary as several independent OS processes (own
// memory, own connection pools) against real PostgreSQL and MiniStack, and kills
// them at chosen points. The binary is built with -race and -tags faultinject;
// any DATA RACE printed by a process fails the test.
//
//	make test-system
package system

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fredzolio/backend-go-jungle/internal/testsupport/awstest"
	"github.com/fredzolio/backend-go-jungle/internal/testsupport/idptest"
	"github.com/fredzolio/backend-go-jungle/internal/testsupport/pgtest"
)

var (
	binary    string
	pg        *pgtest.Env
	ministack *awstest.Env
	idp       *idptest.IdP
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "jungle-system-")
	if err == nil {
		binary = filepath.Join(dir, "jungle")
		build := exec.Command("go", "build", "-race", "-tags", "faultinject", "-o", binary, "../../cmd/jungle")
		build.Env, build.Stdout, build.Stderr = append(os.Environ(), "CGO_ENABLED=1"), os.Stdout, os.Stderr
		err = build.Run()
	}
	if err == nil {
		pg, err = pgtest.Start(ctx)
	}
	if err == nil {
		ministack, err = awstest.Start(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "system setup:", err)
		os.Exit(1)
	}
	idp = idptest.Start()
	code := m.Run()
	idp.JWKS.Close()
	_ = ministack.Stop(ctx)
	_ = pg.Stop(ctx)
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// cluster is one isolated environment (database, queues, topic) shared by the
// processes of a test.
type cluster struct {
	t      *testing.T
	super  *pgxpool.Pool // superuser view (pg_stat_activity shows other roles' waits)
	db     pgtest.DB
	queues awstest.Queues
	topic  awstest.Topic
	dir    string
	mu     sync.Mutex
	procs  []*process
	next   int
}

type process struct {
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	done   chan struct{}
	name   string
	addr   string
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	c := &cluster{t: t, db: pg.NewDatabase(t), queues: ministack.NewQueues(t, 3, 5), topic: ministack.NewTopic(t), dir: t.TempDir()}
	for name, content := range map[string]string{
		"db_password": pgtest.RolePassword, "aws_key": "test", "aws_secret": "test", "topic_arn": c.topic.ARN,
		"senders.json": `{"provider-a":{"access_key_id":"x","user_id":"y"},"provider-b":{"access_key_id":"x","user_id":"y"}}`,
	} {
		if err := os.WriteFile(filepath.Join(c.dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	super, err := pgxpool.New(context.Background(), pg.DSN("postgres", c.db.Name))
	if err != nil {
		t.Fatal(err)
	}
	c.super = super
	t.Cleanup(super.Close)
	t.Cleanup(c.shutdown)
	return c
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// start launches one process and waits until it is ready.
func (c *cluster) start(name, roles, faultPoints string) *process {
	c.t.Helper()
	p := c.launch(name, roles, faultPoints)
	c.waitReady(p)
	return p
}

// startDoomed launches a process expected to crash at a fault point, possibly
// before it ever reports ready.
func (c *cluster) startDoomed(name, roles, faultPoint string) *process {
	c.t.Helper()
	return c.launch(name, roles, faultPoint)
}

func (c *cluster) launch(name, roles, faultPoints string) *process {
	c.t.Helper()
	host, port, _ := net.SplitHostPort(pg.HostPort())
	file := func(n string) string { return filepath.Join(c.dir, n) }
	queue := func(url string) string { return url[strings.LastIndex(url, "/")+1:] }
	p := &process{name: name, addr: freeAddr(c.t), stderr: &bytes.Buffer{}, done: make(chan struct{})}
	p.cmd = exec.Command(binary, "serve")
	p.cmd.Env = append(os.Environ(),
		"INSTANCE_ID="+name, "ROLES="+roles, "JUNGLE_FAULTS="+faultPoints, "LOG_LEVEL=warn",
		"HTTP_ADDR="+p.addr, "METRICS_ADDR="+freeAddr(c.t), "HTTP_DRAIN_DELAY=0s",
		"DB_HOST="+host, "DB_PORT="+port, "DB_NAME="+c.db.Name, "DB_USER=jungle_app", "DB_PASSWORD_FILE="+file("db_password"),
		"DB_LOCK_TIMEOUT=3s", "DB_MAX_CONNS=8",
		"AWS_ENDPOINT_URL="+ministack.Endpoint, "AWS_SQS_INGRESS_QUEUE="+queue(c.queues.URL), "AWS_SQS_INGRESS_DLQ="+queue(c.queues.DLQURL),
		"AWS_EVENTS_TOPIC_ARN_FILE="+file("topic_arn"),
		"AWS_CONSUMER_ACCESS_KEY_ID_FILE="+file("aws_key"), "AWS_CONSUMER_SECRET_ACCESS_KEY_FILE="+file("aws_secret"),
		"AWS_PUBLISHER_ACCESS_KEY_ID_FILE="+file("aws_key"), "AWS_PUBLISHER_SECRET_ACCESS_KEY_FILE="+file("aws_secret"),
		"OIDC_ISSUER="+idptest.Issuer, "OIDC_JWKS_URL="+idp.JWKS.URL,
		"CONSUMER_SENDERS_FILE="+file("senders.json"), "CONSUMER_TRUSTED_ACCOUNTS=000000000000", "CONSUMER_WAIT_SECONDS=1",
		"OUTBOX_LEASE=3s", "OUTBOX_POLL_INTERVAL=100ms", "REFERENCE_POLL_INTERVAL=200ms", "REFERENCE_INITIAL_BACKOFF=1s",
		"RECONCILE_INTERVAL=1h",
	)
	p.cmd.Stdout, p.cmd.Stderr = io.Discard, p.stderr
	if err := p.cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	go func() { _ = p.cmd.Wait(); close(p.done) }()
	c.mu.Lock()
	c.procs = append(c.procs, p)
	c.mu.Unlock()
	return p
}

func (c *cluster) waitReady(p *process) {
	c.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err := http.Get("http://" + p.addr + "/health/ready") //nolint:noctx // test
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case <-p.done:
			c.t.Fatalf("%s exited during startup: %s", p.name, p.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("%s not ready: %s", p.name, p.stderr.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// kill sends SIGKILL (no graceful shutdown at all).
func (c *cluster) kill(p *process) {
	c.t.Helper()
	_ = p.cmd.Process.Signal(syscall.SIGKILL)
	<-p.done
	c.remove(p)
}

// waitExit waits for a process to die by itself (fault point).
func (c *cluster) waitExit(p *process, timeout time.Duration) {
	c.t.Helper()
	select {
	case <-p.done:
		c.remove(p)
	case <-time.After(timeout):
		c.t.Fatalf("%s did not crash at its fault point", p.name)
	}
}

func (c *cluster) remove(p *process) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, q := range c.procs {
		if q == p {
			c.procs = append(c.procs[:i], c.procs[i+1:]...)
			return
		}
	}
}

// shutdown stops every process gracefully and fails on any reported data race.
func (c *cluster) shutdown() {
	c.mu.Lock()
	procs := append([]*process(nil), c.procs...)
	c.mu.Unlock()
	for _, p := range procs {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(30 * time.Second):
			c.t.Errorf("%s did not stop on SIGTERM", p.name)
			_ = p.cmd.Process.Kill()
		}
		if strings.Contains(p.stderr.String(), "DATA RACE") {
			c.t.Errorf("%s reported a data race:\n%s", p.name, p.stderr.String())
		}
		if p.cmd.ProcessState != nil && p.cmd.ProcessState.ExitCode() != 0 {
			c.t.Errorf("%s exited with %d on SIGTERM:\n%s", p.name, p.cmd.ProcessState.ExitCode(), p.stderr.String())
		}
	}
}

// pick returns live processes round-robin.
func (c *cluster) pick() *process {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.procs[c.next%len(c.procs)]
	c.next++
	return p
}

type reply struct {
	body   map[string]any
	status int
}

func (p *process) call(t *testing.T, method, path, token string, body any, headers ...string) reply {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, "http://"+p.addr+path, reader)
	req.Header.Set("Authorization", "Bearer "+token)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil { // Errorf (not Fatalf): call runs inside goroutines too
		t.Errorf("%s %s on %s: %v", method, path, p.name, err)
		return reply{}
	}
	defer res.Body.Close()
	out := reply{status: res.StatusCode, body: map[string]any{}}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (c *cluster) count(sql string, args ...any) int64 {
	c.t.Helper()
	var n int64
	if err := c.db.Owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		c.t.Fatal(err)
	}
	return n
}

// assertLedgerMatchesBalances: for every wallet, stored balance and version equal
// what the ledger says (sum of credits minus debits; last entry version).
func (c *cluster) assertLedgerMatchesBalances() {
	c.t.Helper()
	divergent := c.count(`SELECT count(*) FROM wallets w LEFT JOIN LATERAL (
		SELECT coalesce(sum(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0) AS total,
		       coalesce(max(wallet_version), 1) AS version
		  FROM wallet_ledger_entries e WHERE e.wallet_id = w.id) l ON true
		WHERE w.balance_minor <> l.total OR w.version <> l.version OR w.balance_minor < 0`)
	if divergent != 0 {
		c.t.Fatalf("%d wallets diverge from their ledger", divergent)
	}
}
