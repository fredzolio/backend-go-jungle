// Package logging builds the process-wide structured JSON logger.
package logging

import (
	"log/slog"
	"os"
)

// New returns a JSON slog logger tagged with the service and instance.
func New(level slog.Level, instanceID string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With(
		slog.String("service", "jungle"),
		slog.String("instance", instanceID),
	)
}
