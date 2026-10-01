// Package logging builds the process-wide structured JSON logger.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
)

// allowed lists the only attribute keys that may reach the logs: identifiers that
// trace an operation and operational facts. Anything else (amounts, balances,
// payloads, tokens, secrets) is dropped even if someone logs it by mistake.
var allowed = map[string]bool{
	"service": true, "instance": true, "error": true, "panic": true,
	"correlationId": true, "messageId": true, "sqsMessageId": true, "transactionId": true,
	"walletId": true, "providerId": true, "clientId": true, "eventId": true, "eventType": true,
	"status": true, "outcome": true, "duplicate": true, "replay": true, "attempts": true, "dead": true,
	"worker": true, "copies": true, "handled": true, "check": true, "addr": true, "signal": true,
	"method": true, "route": true, "durationMs": true, "retry_in": true, "consistent": true,
	"divergent": true, "checked": true,
	// Fx lifecycle events
	"module": true, "function": true, "callee": true, "caller": true, "runtime": true, "type": true,
	"name": true, "stacktrace": true,
}

// New returns a JSON logger tagged with the service and instance.
func New(level slog.Level, instanceID string) *slog.Logger {
	return NewWithWriter(os.Stdout, level).With(slog.String("service", "jungle"), slog.String("instance", instanceID))
}

// NewWithWriter builds the allowlisted JSON logger on w.
func NewWithWriter(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(allowlist{next: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})})
}

// allowlist filters record and logger attributes by key.
type allowlist struct{ next slog.Handler }

func (h allowlist) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h allowlist) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if allowed[a.Key] {
			out.AddAttrs(a)
		}
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h allowlist) WithAttrs(attrs []slog.Attr) slog.Handler {
	kept := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if allowed[a.Key] {
			kept = append(kept, a)
		}
	}
	return allowlist{next: h.next.WithAttrs(kept)}
}

func (h allowlist) WithGroup(name string) slog.Handler {
	return allowlist{next: h.next.WithGroup(name)}
}
