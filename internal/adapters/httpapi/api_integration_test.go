//go:build integration

package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const player = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"

func TestAuthentication_rejects_missing_invalid_and_expired_tokens(t *testing.T) {
	a := newAPI(t)
	cases := map[string]string{
		"missing":        "",
		"garbage":        "not-a-jwt",
		"expired":        idp.token(t, tokenSpec{clientID: "jungle-internal", scope: "wallets.read", exp: -time.Minute}),
		"wrong audience": idp.token(t, tokenSpec{clientID: "jungle-internal", scope: "wallets.read", aud: "account"}),
		"wrong issuer":   idp.token(t, tokenSpec{clientID: "jungle-internal", scope: "wallets.read", iss: "https://evil.test/realms/jungle"}),
		"foreign key":    idp.token(t, tokenSpec{clientID: "jungle-internal", scope: "wallets.read", rogue: true}),
		"id token":       idp.token(t, tokenSpec{clientID: "jungle-internal", scope: "wallets.read", typ: "ID"}),
	}
	for name, token := range cases {
		r := a.call(t, "GET", "/wallets/"+uuid.NewString(), token, nil)
		if r.status != http.StatusUnauthorized || r.body["code"] != "UNAUTHENTICATED" || r.header.Get("WWW-Authenticate") == "" {
			t.Fatalf("%s: %d %v", name, r.status, r.body)
		}
	}
}

func TestAuthorization_wallet_operations_are_internal_only(t *testing.T) {
	a := newAPI(t)
	r := a.call(t, "POST", "/wallets", idp.provider(t, "provider-a"), map[string]any{
		"playerId": player, "initialBalance": map[string]string{"amount": "1.00", "currency": "BRL"},
	})
	if r.status != http.StatusForbidden || r.body["code"] != "INSUFFICIENT_SCOPE" {
		t.Fatalf("provider opening a wallet: %d %v", r.status, r.body)
	}
	w := a.openWallet(t, player, "10.00")
	if r := a.call(t, "GET", "/wallets/"+w["id"].(string), idp.provider(t, "provider-a"), nil); r.status != http.StatusForbidden {
		t.Fatalf("provider reading a wallet: %d", r.status)
	}
	if r := a.call(t, "POST", "/wagering/transactions", idp.internal(t), bet(w, "provider-a", "t1", "BET", "1.00", ""), "Idempotency-Key", "k1"); r.status != http.StatusForbidden {
		t.Fatalf("internal submitting: %d", r.status)
	}
}

func TestWallet_open_read_and_conflict(t *testing.T) {
	a := newAPI(t)
	w := a.openWallet(t, player, "1000.00")
	if w["version"] != float64(1) || w["balance"].(map[string]any)["amount"] != "1000.00" {
		t.Fatalf("wallet = %v", w)
	}
	r := a.call(t, "GET", "/wallets/"+w["id"].(string), idp.internal(t), nil)
	if r.status != http.StatusOK || r.body["playerId"] != player {
		t.Fatalf("get = %d %v", r.status, r.body)
	}
	dup := a.call(t, "POST", "/wallets", idp.internal(t), map[string]any{"playerId": player, "initialBalance": map[string]string{"amount": "0.00", "currency": "BRL"}})
	if dup.status != http.StatusConflict || dup.body["code"] != "WALLET_ALREADY_EXISTS" {
		t.Fatalf("duplicate = %d %v", dup.status, dup.body)
	}
}

func TestSubmit_contract_status_codes(t *testing.T) {
	a := newAPI(t)
	w := a.openWallet(t, player, "1000.00")
	tok := idp.provider(t, "provider-a")
	post := func(body any, key string) response {
		return a.call(t, "POST", "/wagering/transactions", tok, body, "Idempotency-Key", key, "X-Correlation-Id", "corr-42")
	}
	first := post(bet(w, "provider-a", "transaction-123", "BET", "25.00", ""), "provider-a:transaction-123")
	if first.status != http.StatusOK || first.body["status"] != "PROCESSED" || first.body["idempotentReplay"] != false ||
		first.body["balance"].(map[string]any)["amount"] != "975.00" || first.header.Get("X-Correlation-Id") != "corr-42" {
		t.Fatalf("first = %d %v", first.status, first.body)
	}
	replay := post(bet(w, "provider-a", "transaction-123", "BET", "25.00", ""), "provider-a:transaction-123")
	if replay.status != http.StatusOK || replay.body["idempotentReplay"] != true || replay.body["transactionId"] != first.body["transactionId"] {
		t.Fatalf("replay = %d %v", replay.status, replay.body)
	}
	cases := []struct {
		body   any
		key    string
		status int
		code   string
	}{
		{bet(w, "provider-a", "transaction-123", "BET", "30.00", ""), "provider-a:transaction-123", 409, "IDEMPOTENCY_KEY_REUSED"},
		{bet(w, "provider-a", "transaction-123", "BET", "25.00", ""), "another-key", 409, "DUPLICATE_EXTERNAL_TRANSACTION"},
		{bet(w, "provider-a", "t-missing-key", "BET", "1.00", ""), "", 400, "INVALID_REQUEST"},
		{bet(w, "provider-a", "t-opening", "OPENING", "1.00", ""), "k-opening", 400, "RESERVED_KIND"},
		{bet(w, "provider-b", "t-spoof", "BET", "1.00", ""), "k-spoof", 403, "PROVIDER_MISMATCH"},
		{`{"providerId":"provider-a","money":{"amount":25.0,"currency":"BRL"}}`, "k-float", 400, "INVALID_REQUEST"},
		{`{"providerId":"provider-a","unexpected":true}`, "k-unknown", 400, "INVALID_REQUEST"},
		{bet(map[string]any{"id": uuid.NewString(), "playerId": player}, "provider-a", "t-ghost", "BET", "1.00", ""), "k-ghost", 404, "WALLET_NOT_FOUND"},
	}
	for _, c := range cases {
		r := post(c.body, c.key)
		if r.status != c.status || r.body["code"] != c.code {
			t.Fatalf("%v: %d %v", c.body, r.status, r.body)
		}
	}
	rejected := post(bet(w, "provider-a", "t-big", "BET", "5000.00", ""), "k-big")
	if rejected.status != http.StatusUnprocessableEntity || rejected.body["status"] != "REJECTED" || rejected.body["failureCode"] != "INSUFFICIENT_FUNDS" {
		t.Fatalf("rejected = %d %v", rejected.status, rejected.body)
	}
	pending := post(bet(w, "provider-a", "t-refund", "REFUND", "10.00", "bet-later"), "k-refund")
	if pending.status != http.StatusAccepted || pending.body["status"] != "PENDING_REFERENCE" ||
		!strings.HasPrefix(pending.header.Get("Location"), "/wagering/transactions/") {
		t.Fatalf("pending = %d %v %v", pending.status, pending.body, pending.header)
	}
	follow := a.call(t, "GET", pending.header.Get("Location"), tok, nil)
	if follow.status != http.StatusOK || follow.body["status"] != "PENDING_REFERENCE" || follow.body["nextAttemptAt"] == nil {
		t.Fatalf("follow = %d %v", follow.status, follow.body)
	}
	balance := a.call(t, "GET", "/wallets/"+w["id"].(string), idp.internal(t), nil).body["balance"].(map[string]any)["amount"]
	if balance != "975.00" {
		t.Fatalf("errors moved money: balance %v", balance)
	}
}

func TestProviders_are_isolated_on_reads_and_replays(t *testing.T) {
	a := newAPI(t)
	w := a.openWallet(t, player, "100.00")
	tokA, tokB := idp.provider(t, "provider-a"), idp.provider(t, "provider-b")
	r := a.call(t, "POST", "/wagering/transactions", tokA, bet(w, "provider-a", "secret-tx", "BET", "10.00", ""), "Idempotency-Key", "provider-a:secret-tx")
	id := r.body["transactionId"].(string)
	for _, path := range []string{"/wagering/transactions/" + id, "/providers/provider-a/wagering/transactions/secret-tx"} {
		if r := a.call(t, "GET", path, tokB, nil); r.status != http.StatusNotFound || r.body["code"] != "TRANSACTION_NOT_FOUND" {
			t.Fatalf("provider-b read %s: %d %v", path, r.status, r.body)
		}
		if r := a.call(t, "GET", path, tokA, nil); r.status != http.StatusOK || r.body["providerId"] != "provider-a" {
			t.Fatalf("provider-a read %s: %d", path, r.status)
		}
		if r := a.call(t, "GET", path, idp.internal(t), nil); r.status != http.StatusOK {
			t.Fatalf("internal read %s: %d", path, r.status)
		}
	}
	replay := a.call(t, "POST", "/wagering/transactions", tokB, bet(w, "provider-a", "secret-tx", "BET", "10.00", ""), "Idempotency-Key", "provider-a:secret-tx")
	if replay.status != http.StatusForbidden || replay.body["transactionId"] != nil {
		t.Fatalf("provider-b replaying provider-a: %d %v", replay.status, replay.body)
	}
}

func TestLedger_pages_with_opaque_cursor(t *testing.T) {
	a := newAPI(t)
	w := a.openWallet(t, player, "100.00")
	tok := idp.provider(t, "provider-a")
	for i, amount := range []string{"1.00", "2.00", "3.00"} {
		ext := "bet-" + amount
		if r := a.call(t, "POST", "/wagering/transactions", tok, bet(w, "provider-a", ext, "BET", amount, ""), "Idempotency-Key", ext); r.status != 200 {
			t.Fatalf("bet %d: %d", i, r.status)
		}
	}
	path := "/wallets/" + w["id"].(string) + "/ledger?limit=3"
	page1 := a.call(t, "GET", path, idp.internal(t), nil)
	cursor, ok := page1.body["nextCursor"].(string)
	if page1.status != 200 || len(page1.body["items"].([]any)) != 3 || !ok {
		t.Fatalf("page1 = %d %v", page1.status, page1.body)
	}
	page2 := a.call(t, "GET", path+"&cursor="+cursor, idp.internal(t), nil)
	items := page2.body["items"].([]any)
	if len(items) != 1 || page2.body["nextCursor"] != nil || items[0].(map[string]any)["balanceAfter"].(map[string]any)["amount"] != "94.00" {
		t.Fatalf("page2 = %v", page2.body)
	}
	other := a.openWallet(t, "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a2", "1.00")
	if r := a.call(t, "GET", "/wallets/"+other["id"].(string)+"/ledger?cursor="+cursor, idp.internal(t), nil); r.status != 400 {
		t.Fatalf("cursor of another wallet: %d", r.status)
	}
}

func TestReconciliation_endpoint_reports_and_is_internal_only(t *testing.T) {
	a := newAPI(t)
	w := a.openWallet(t, player, "1000.00")
	tok := idp.provider(t, "provider-a")
	a.call(t, "POST", "/wagering/transactions", tok, bet(w, "provider-a", "transaction-123", "BET", "25.00", ""), "Idempotency-Key", "k-rec")
	path := "/wallets/" + w["id"].(string) + "/reconciliation"
	if r := a.call(t, "POST", path, tok, nil); r.status != http.StatusForbidden {
		t.Fatalf("provider reconciling: %d", r.status)
	}
	r := a.call(t, "POST", path, idp.internal(t), nil)
	money := func(k string) string { return r.body[k].(map[string]any)["amount"].(string) }
	if r.status != http.StatusOK || r.body["consistent"] != true || r.body["checkedEntries"] != float64(2) ||
		money("storedBalance") != "975.00" || money("calculatedBalance") != "975.00" || money("difference") != "0.00" {
		t.Fatalf("reconciliation = %d %v", r.status, r.body)
	}
}
