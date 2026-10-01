package bootstrap_test

import (
	"io"
	"log/slog"
	"testing"

	"go.uber.org/fx"

	"github.com/fredzolio/backend-go-jungle/internal/bootstrap"
	"github.com/fredzolio/backend-go-jungle/internal/platform/config"
)

// The whole dependency graph, with every role enabled, resolves (no missing or
// ambiguous providers). Constructors are not executed.
func TestGraph_with_all_roles_is_complete(t *testing.T) {
	cfg := config.Config{Roles: []string{"resolver", "consumer", "outbox", "reconciler"}}
	if err := fx.ValidateApp(bootstrap.Options(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))); err != nil {
		t.Fatal(err)
	}
}
