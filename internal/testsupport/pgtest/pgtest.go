//go:build integration

// Package pgtest runs a real PostgreSQL (testcontainers) for integration tests.
// It recreates the production role model (owner / app / readonly, as provisioned
// by Terraform), migrates a template database once and clones it per test, so
// each test starts from a clean, fully migrated schema in milliseconds.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
)

const (
	image    = "postgres:17.11-alpine"
	template = "jungle_template"
	// RolePassword is the password of every test role (test-only container).
	RolePassword = "test"
)

// Env is one running PostgreSQL with the migrated template.
type Env struct {
	container *tcpostgres.PostgresContainer
	host      string
}

// Start boots the container, creates the roles and migrates the template.
func Start(ctx context.Context) (*Env, error) {
	c, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword(RolePassword), tcpostgres.WithDatabase("postgres"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		return nil, fmt.Errorf("start postgres container: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, err
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, err
	}
	env := &Env{container: c, host: fmt.Sprintf("%s:%s", host, port.Port())}
	if err := env.bootstrap(ctx); err != nil {
		return nil, err
	}
	return env, nil
}

// HostPort is the mapped address of the server.
func (e *Env) HostPort() string { return e.host }

// Stop terminates the container.
func (e *Env) Stop(ctx context.Context) error { return e.container.Terminate(ctx) }

// DSN builds a connection string for a role and database with the production
// session timeouts (lock_timeout shortened so contention tests are fast).
func (e *Env) DSN(role, database string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(role, RolePassword), Host: e.host, Path: "/" + database}
	q := url.Values{}
	q.Set("sslmode", "disable")
	q.Set("lock_timeout", "1000")
	q.Set("statement_timeout", "10000")
	u.RawQuery = q.Encode()
	return u.String()
}

func (e *Env) exec(ctx context.Context, database string, statements ...string) error {
	pool, err := pgxpool.New(ctx, e.DSN("postgres", database))
	if err != nil {
		return err
	}
	defer pool.Close()
	for _, s := range statements {
		if _, err := pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}

func (e *Env) bootstrap(ctx context.Context) error {
	err := e.exec(ctx, "postgres",
		`CREATE ROLE jungle_owner LOGIN PASSWORD 'test'`,
		`CREATE ROLE jungle_app LOGIN PASSWORD 'test'`,
		`CREATE ROLE jungle_readonly LOGIN PASSWORD 'test'`,
		`CREATE DATABASE `+template+` OWNER jungle_owner`,
	)
	if err != nil {
		return err
	}
	// Mirrors infra/terraform/modules/postgres_access.
	err = e.exec(ctx, template,
		`GRANT CONNECT ON DATABASE `+template+` TO jungle_app, jungle_readonly`,
		`GRANT USAGE ON SCHEMA public TO jungle_app, jungle_readonly`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE jungle_owner IN SCHEMA public GRANT SELECT, INSERT, UPDATE ON TABLES TO jungle_app`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE jungle_owner IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO jungle_app`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE jungle_owner IN SCHEMA public GRANT SELECT ON TABLES TO jungle_readonly`,
	)
	if err != nil {
		return err
	}
	return postgres.Migrate(ctx, e.DSN("jungle_owner", template), "up", io.Discard)
}

// DB is one isolated database cloned from the migrated template.
type DB struct {
	App   *pgxpool.Pool // jungle_app: what the service uses
	Owner *pgxpool.Pool // jungle_owner: schema owner (still bound by triggers)
	Name  string
}

// NewDatabase clones the template into a fresh database dropped at test end.
func (e *Env) NewDatabase(t *testing.T) DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := "t_" + randomSuffix(t)
	if err := e.exec(ctx, "postgres", `CREATE DATABASE `+name+` TEMPLATE `+template+` OWNER jungle_owner`); err != nil {
		t.Fatal(err)
	}
	db := DB{Name: name, App: e.pool(t, "jungle_app", name), Owner: e.pool(t, "jungle_owner", name)}
	t.Cleanup(func() {
		db.App.Close()
		db.Owner.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if err := e.exec(dropCtx, "postgres", `DROP DATABASE `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
	return db
}

// EmptyDatabase creates an unmigrated database owned by jungle_owner.
func (e *Env) EmptyDatabase(t *testing.T) string {
	t.Helper()
	name := "e_" + randomSuffix(t)
	if err := e.exec(context.Background(), "postgres", `CREATE DATABASE `+name+` OWNER jungle_owner`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.exec(context.Background(), "postgres", `DROP DATABASE `+name+` WITH (FORCE)`) })
	return name
}

func (e *Env) pool(t *testing.T, role, database string) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), e.DSN(role, database))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
