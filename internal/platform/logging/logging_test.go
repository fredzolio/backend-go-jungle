package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/fredzolio/backend-go-jungle/internal/platform/logging"
)

func TestLogger_keeps_identifiers_and_drops_sensitive_attributes(t *testing.T) {
	var buf bytes.Buffer
	log := logging.NewWithWriter(&buf, slog.LevelInfo).With(slog.String("walletId", "w-1"), slog.String("token", "secret"))
	log.Info("handled", slog.String("transactionId", "t-1"), slog.String("amount", "975.00"),
		slog.String("body", `{"money":1}`), slog.String("authorization", "Bearer x"))

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["walletId"] != "w-1" || line["transactionId"] != "t-1" || line["msg"] != "handled" {
		t.Fatalf("identifiers missing: %v", line)
	}
	for _, k := range []string{"token", "amount", "body", "authorization"} {
		if _, leaked := line[k]; leaked {
			t.Fatalf("sensitive attribute %q leaked: %v", k, line)
		}
	}
}
