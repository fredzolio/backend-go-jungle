// Package config parses process configuration from the environment exactly once,
// at startup. Secrets are read from files (`*_FILE` variables) written by the
// Terraform provisioner, never from plain environment values.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the complete, validated configuration of one process.
type Config struct {
	InstanceID string     `env:"INSTANCE_ID" envDefault:"local"`
	LogLevel   slog.Level `env:"LOG_LEVEL"   envDefault:"info"`
	// Roles enabled in this process (same binary everywhere): resolver, ...
	Roles     []string  `env:"ROLES" envDefault:"resolver" envSeparator:","`
	HTTP      HTTP      `envPrefix:"HTTP_"`
	Lifecycle Lifecycle `envPrefix:"LIFECYCLE_"`
	Reference Reference `envPrefix:"REFERENCE_"`
	Postgres  Postgres  `envPrefix:"DB_"`
	AWS       AWS       `envPrefix:"AWS_"`
}

// HTTP configures the public API listener.
type HTTP struct {
	Addr              string        `env:"ADDR"                envDefault:":8080"`
	ReadHeaderTimeout time.Duration `env:"READ_HEADER_TIMEOUT" envDefault:"5s"`
	ReadTimeout       time.Duration `env:"READ_TIMEOUT"        envDefault:"15s"`
	WriteTimeout      time.Duration `env:"WRITE_TIMEOUT"       envDefault:"15s"`
	IdleTimeout       time.Duration `env:"IDLE_TIMEOUT"        envDefault:"60s"`
	// DrainDelay is how long readiness reports DOWN before the listener stops,
	// giving the edge load balancer time to stop routing new requests here.
	DrainDelay time.Duration `env:"DRAIN_DELAY" envDefault:"3s"`
}

// Reference configures the wait for references that have not arrived yet and the
// resolver worker that retries them.
type Reference struct {
	InitialBackoff time.Duration `env:"INITIAL_BACKOFF"  envDefault:"1s"`
	MaxBackoff     time.Duration `env:"MAX_BACKOFF"      envDefault:"1m"`
	TTL            time.Duration `env:"TTL"              envDefault:"10m"`
	PollInterval   time.Duration `env:"POLL_INTERVAL"    envDefault:"500ms"`
	Batch          int           `env:"BATCH"            envDefault:"100"`
}

// HasRole reports whether a role is enabled.
func (c Config) HasRole(role string) bool {
	for _, r := range c.Roles {
		if strings.TrimSpace(r) == role {
			return true
		}
	}
	return false
}

// Lifecycle bounds Fx start and stop.
type Lifecycle struct {
	StartTimeout time.Duration `env:"START_TIMEOUT" envDefault:"60s"`
	StopTimeout  time.Duration `env:"STOP_TIMEOUT"  envDefault:"25s"`
}

// Postgres configures the application connection pool (role jungle_app).
type Postgres struct {
	Host     string `env:"HOST,required"`
	Name     string `env:"NAME,required"`
	User     string `env:"USER,required"`
	Password string `env:"PASSWORD_FILE,file,required"`
	Port     uint16 `env:"PORT"      envDefault:"5432"`
	MaxConns int32  `env:"MAX_CONNS" envDefault:"10"`
	// Session defaults: a transaction blocked on a row lock or a slow statement
	// fails fast (classified as transient) instead of hanging.
	LockTimeout      time.Duration `env:"LOCK_TIMEOUT"      envDefault:"5s"`
	StatementTimeout time.Duration `env:"STATEMENT_TIMEOUT" envDefault:"15s"`
}

// Migrator is the configuration of `jungle migrate` (owner role, no AWS).
type Migrator struct {
	InstanceID string        `env:"INSTANCE_ID"   envDefault:"migrate"`
	LogLevel   slog.Level    `env:"LOG_LEVEL"     envDefault:"info"`
	Postgres   Postgres      `envPrefix:"DB_"`
	Timeout    time.Duration `env:"MIGRATE_TIMEOUT" envDefault:"2m"`
}

// LoadMigrator parses the migrator configuration.
func LoadMigrator() (Migrator, error) {
	cfg, err := env.ParseAs[Migrator]()
	if err != nil {
		return Migrator{}, fmt.Errorf("parse environment: %w", err)
	}
	cfg.Postgres.Password = strings.TrimSpace(cfg.Postgres.Password)
	return cfg, nil
}

// AWS configures SQS/SNS access. EndpointURL points to MiniStack locally and is
// empty against real AWS.
type AWS struct {
	EndpointURL  string      `env:"ENDPOINT_URL"`
	Region       string      `env:"REGION"            envDefault:"us-east-1"`
	IngressQueue string      `env:"SQS_INGRESS_QUEUE" envDefault:"wager-transactions.fifo"`
	Consumer     Credentials `envPrefix:"CONSUMER_"`
}

// Credentials is one IAM principal; each component gets its own.
type Credentials struct {
	AccessKeyID     string `env:"ACCESS_KEY_ID_FILE,file,required"`
	SecretAccessKey string `env:"SECRET_ACCESS_KEY_FILE,file,required"`
}

// Load parses the environment into a Config.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}
	cfg.Postgres.Password = strings.TrimSpace(cfg.Postgres.Password)
	cfg.AWS.Consumer.AccessKeyID = strings.TrimSpace(cfg.AWS.Consumer.AccessKeyID)
	cfg.AWS.Consumer.SecretAccessKey = strings.TrimSpace(cfg.AWS.Consumer.SecretAccessKey)
	return cfg, nil
}

// DSN renders the pgx connection string, tagging connections with the instance.
func (p Postgres) DSN(instanceID string) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(p.User, p.Password),
		Host:   net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))),
		Path:   "/" + p.Name,
	}
	q := url.Values{}
	q.Set("sslmode", "disable")
	q.Set("application_name", "jungle-"+instanceID)
	q.Set("lock_timeout", strconv.FormatInt(p.LockTimeout.Milliseconds(), 10))
	q.Set("statement_timeout", strconv.FormatInt(p.StatementTimeout.Milliseconds(), 10))
	q.Set("idle_in_transaction_session_timeout", "30000")
	u.RawQuery = q.Encode()
	return u.String()
}
