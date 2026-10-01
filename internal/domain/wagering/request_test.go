package wagering_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

func TestNewExternalRequest_rejects_OPENING_from_providers(t *testing.T) {
	r := raw(t)
	r.Kind = "OPENING"
	if _, err := wagering.NewExternalRequest(r); !errors.Is(err, wagering.ErrReservedKind) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewExternalRequest_enforces_zero_amount_policy_per_kind(t *testing.T) {
	cases := []struct {
		kind, amount, ref string
		ok                bool
	}{
		{"BET", "25.00", "", true}, {"BET", "0.00", "", false},
		{"WIN", "10.00", "", true}, {"WIN", "0.00", "", false},
		{"LOSS", "0.00", "", true}, {"LOSS", "0.01", "", false},
		{"REFUND", "25.00", "transaction-1", true}, {"REFUND", "0.00", "transaction-1", false},
		{"ROLLBACK", "25.00", "transaction-1", true}, {"ROLLBACK", "0.00", "transaction-1", false},
	}
	for _, tc := range cases {
		r := raw(t)
		r.Kind, r.Money, r.ReferenceExternalTransactionID = tc.kind, brl(t, tc.amount), tc.ref
		_, err := wagering.NewExternalRequest(r)
		if (err == nil) != tc.ok {
			t.Fatalf("%s %s: err = %v, want ok=%v", tc.kind, tc.amount, err, tc.ok)
		}
		if err != nil && !errors.Is(err, wagering.ErrInvalidRequest) {
			t.Fatalf("%s %s: error not classifiable as ErrInvalidRequest: %v", tc.kind, tc.amount, err)
		}
	}
}

func TestNewExternalRequest_enforces_reference_policy(t *testing.T) {
	cases := []struct {
		kind, ref string
		ok        bool
	}{
		{"REFUND", "", false}, {"ROLLBACK", "", false},
		{"BET", "transaction-1", false}, {"LOSS", "transaction-1", false},
		{"WIN", "", true}, {"WIN", "transaction-1", true},
		{"REFUND", "transaction-123", false}, // self reference
	}
	for _, tc := range cases {
		r := raw(t)
		r.Kind, r.ReferenceExternalTransactionID = tc.kind, tc.ref
		if tc.kind == "LOSS" {
			r.Money = brl(t, "0.00")
		}
		if _, err := wagering.NewExternalRequest(r); (err == nil) != tc.ok {
			t.Fatalf("%s ref=%q: err = %v, want ok=%v", tc.kind, tc.ref, err, tc.ok)
		}
	}
}

func TestNewExternalRequest_reports_every_invalid_field(t *testing.T) {
	r := raw(t)
	r.ProviderID, r.PlayerID, r.RoundID, r.Money = "", "not-a-uuid", "round 1", money.Money{}
	_, err := wagering.NewExternalRequest(r)
	var fields []string
	for _, e := range []string{"providerId", "playerId", "roundId", "money"} {
		if !strings.Contains(err.Error(), e) {
			fields = append(fields, e)
		}
	}
	if len(fields) > 0 {
		t.Fatalf("missing field errors %v in %v", fields, err)
	}
	var verr *wagering.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("not a ValidationError: %v", err)
	}
}

func TestNewExternalRequest_normalizes_uuid_case(t *testing.T) {
	upper := request(t, func(r *wagering.RawRequest) { r.PlayerID = strings.ToUpper(r.PlayerID) })
	lower := request(t, nil)
	if upper.PayloadHash() != lower.PayloadHash() {
		t.Fatal("uuid case changed the payload hash")
	}
}

type hashVector struct {
	Input struct {
		Money                          struct{ Amount, Currency string }
		ProviderID                     string `json:"providerId"`
		ExternalTransactionID          string `json:"externalTransactionId"`
		PlayerID                       string `json:"playerId"`
		WalletID                       string `json:"walletId"`
		RoundID                        string `json:"roundId"`
		GameID                         string `json:"gameId"`
		Kind                           string `json:"kind"`
		ReferenceExternalTransactionID string `json:"referenceExternalTransactionId"`
	} `json:"input"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Vectors were produced by an independent implementation (Python json.dumps with
// sort_keys and compact separators), not by this package.
func TestPayloadHash_matches_independent_golden_vectors(t *testing.T) {
	data, err := os.ReadFile("testdata/payload_hash_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []hashVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		m, err := money.Parse(v.Input.Money.Amount, v.Input.Money.Currency)
		if err != nil {
			t.Fatal(err)
		}
		req, err := wagering.NewExternalRequest(wagering.RawRequest{
			ProviderID: v.Input.ProviderID, ExternalTransactionID: v.Input.ExternalTransactionID,
			IdempotencyKey: "any-key", PlayerID: v.Input.PlayerID, WalletID: v.Input.WalletID,
			RoundID: v.Input.RoundID, GameID: v.Input.GameID, Kind: v.Input.Kind, Money: m,
			ReferenceExternalTransactionID: v.Input.ReferenceExternalTransactionID,
		})
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		if got := req.PayloadHash(); got != v.SHA256 {
			t.Fatalf("%s: hash %s, want %s", v.Name, got, v.SHA256)
		}
	}
}

func TestPayloadHash_ignores_idempotency_key_but_not_business_fields(t *testing.T) {
	base := request(t, nil).PayloadHash()
	if request(t, func(r *wagering.RawRequest) { r.IdempotencyKey = "other-key" }).PayloadHash() != base {
		t.Fatal("idempotency key must not affect the hash")
	}
	if request(t, func(r *wagering.RawRequest) { r.Money = brl(t, "25.01") }).PayloadHash() == base {
		t.Fatal("amount change must change the hash (same key, different content => conflict)")
	}
	if request(t, func(r *wagering.RawRequest) { r.RoundID = "round-988" }).PayloadHash() == base {
		t.Fatal("round change must change the hash")
	}
}
