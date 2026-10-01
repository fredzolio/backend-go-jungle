package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/fredzolio/backend-go-jungle/internal/app"
)

// transientCodes are SQLSTATEs after which retrying the whole transaction is safe:
// nothing was committed.
var transientCodes = map[string]bool{
	"40001": true, // serialization_failure
	"40P01": true, // deadlock_detected
	"55P03": true, // lock_not_available (lock_timeout)
	"57014": true, // query_canceled (statement_timeout)
	"57P01": true, // admin_shutdown
	"57P02": true, // crash_shutdown
	"57P03": true, // cannot_connect_now
}

// classify maps driver errors onto app sentinels, keeping the original for logs.
// Errors that are not database errors (domain/app errors from inside a unit of
// work) pass through untouched.
func classify(op string, err error) error {
	if err == nil || errors.Is(err, app.ErrTransient) || errors.Is(err, app.ErrIntegrity) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case transientCodes[pgErr.Code] || strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "53"):
			return fmt.Errorf("%s: %w: %w", op, app.ErrTransient, err)
		case strings.HasPrefix(pgErr.Code, "23"):
			return fmt.Errorf("%s: %w: %w", op, app.ErrIntegrity, err)
		default:
			return fmt.Errorf("%s: %w", op, err)
		}
	}
	var netErr net.Error
	var connectErr *pgconn.ConnectError
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) || pgconn.SafeToRetry(err) ||
		errors.As(err, &netErr) || errors.As(err, &connectErr) {
		return fmt.Errorf("%s: %w: %w", op, app.ErrTransient, err)
	}
	return err
}

// violates reports a constraint violation of the given SQLSTATE and constraint.
func violates(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code && pgErr.ConstraintName == constraint
}

const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
)
