//go:build e2e

// Package e2e drives the running compose stack through its public edge with
// tokens issued by the real Keycloak (provisioned by Terraform).
//
//	make up && make test-e2e
//
// Inputs: JUNGLE_BASE_URL (default http://localhost:18080), JUNGLE_SQS_ENDPOINT
// (default http://localhost:14566) and JUNGLE_PROVISIONED_DIR (credentials written
// by the provisioner; `make test-e2e` copies them to a temporary directory).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type client struct {
	Secret     string `json:"client_secret"`
	ProviderID string `json:"provider_id"`
}

func baseURL() string {
	if v := os.Getenv("JUNGLE_BASE_URL"); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	return "http://localhost:18080"
}

func clients(t *testing.T) map[string]client {
	t.Helper()
	raw, err := os.ReadFile(provisioned(t, "keycloak/clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]client
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func provisioned(t *testing.T, rel string) string {
	t.Helper()
	dir := os.Getenv("JUNGLE_PROVISIONED_DIR")
	if dir == "" {
		t.Skip("JUNGLE_PROVISIONED_DIR not set (run `make test-e2e`)")
	}
	return dir + "/" + rel
}

func token(t *testing.T, id string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {clients(t)[id].Secret}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL()+"/auth/realms/jungle/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || body.AccessToken == "" {
		t.Fatalf("token for %s: status %d, %v", id, res.StatusCode, err)
	}
	return body.AccessToken
}

type response struct {
	body   map[string]any
	status int
}

func call(t *testing.T, method, path, tok string, body any, headers ...string) response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, baseURL()+path, reader)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := response{status: res.StatusCode, body: map[string]any{}}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func openWallet(t *testing.T, balance string) map[string]any {
	t.Helper()
	r := call(t, "POST", "/wallets", token(t, "jungle-internal"), map[string]any{
		"playerId": uuid.NewString(), "initialBalance": map[string]string{"amount": balance, "currency": "BRL"},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("open wallet: %d %v", r.status, r.body)
	}
	return r.body
}

func betBody(w map[string]any, provider, ext, amount string) map[string]any {
	return map[string]any{
		"providerId": provider, "externalTransactionId": ext, "playerId": w["playerId"], "walletId": w["id"],
		"roundId": "round-e2e", "gameId": "fortune-chimp", "kind": "BET", "money": map[string]string{"amount": amount, "currency": "BRL"},
	}
}

func TestRealIdP_rejects_missing_tampered_and_expired_tokens(t *testing.T) {
	path := "/wallets/" + uuid.NewString()
	if r := call(t, "GET", path, "", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("missing: %d", r.status)
	}
	tok := token(t, "jungle-internal")
	tampered := tok[:len(tok)-4] + "AAAA"
	if r := call(t, "GET", path, tampered, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("tampered: %d", r.status)
	}
	short := token(t, "provider-c-shortlived") // 5 s lifespan configured in the realm
	time.Sleep(7 * time.Second)
	if r := call(t, "GET", "/wagering/transactions/"+uuid.NewString(), short, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("expired: %d %v", r.status, r.body)
	}
}

func TestRealIdP_end_to_end_flow_with_provider_isolation(t *testing.T) {
	w := openWallet(t, "100.00")
	tokA, tokB := token(t, "provider-a"), token(t, "provider-b")
	ext := "e2e-" + uuid.NewString()[:8]
	first := call(t, "POST", "/wagering/transactions", tokA, betBody(w, "provider-a", ext, "80.00"), "Idempotency-Key", "provider-a:"+ext)
	if first.status != http.StatusOK || first.body["balance"].(map[string]any)["amount"] != "20.00" {
		t.Fatalf("bet: %d %v", first.status, first.body)
	}
	if r := call(t, "POST", "/wagering/transactions", tokA, betBody(w, "provider-a", ext, "80.00"), "Idempotency-Key", "provider-a:"+ext); r.body["idempotentReplay"] != true {
		t.Fatalf("replay: %d %v", r.status, r.body)
	}
	second := call(t, "POST", "/wagering/transactions", tokA, betBody(w, "provider-a", ext+"-2", "80.00"), "Idempotency-Key", "provider-a:"+ext+"-2")
	if second.status != http.StatusUnprocessableEntity || second.body["failureCode"] != "INSUFFICIENT_FUNDS" {
		t.Fatalf("second bet: %d %v", second.status, second.body)
	}
	id := first.body["transactionId"].(string)
	if r := call(t, "GET", "/wagering/transactions/"+id, tokB, nil); r.status != http.StatusNotFound {
		t.Fatalf("provider-b reading provider-a: %d", r.status)
	}
	if r := call(t, "GET", "/providers/provider-a/wagering/transactions/"+ext, tokA, nil); r.status != http.StatusOK {
		t.Fatalf("provider-a reading by external id: %d", r.status)
	}
	if r := call(t, "POST", "/wallets", tokA, map[string]any{"playerId": uuid.NewString(), "initialBalance": map[string]string{"amount": "1.00", "currency": "BRL"}}); r.status != http.StatusForbidden {
		t.Fatalf("provider opening wallet: %d", r.status)
	}
	if r := call(t, "GET", "/wallets/"+w["id"].(string), token(t, "jungle-internal"), nil); r.body["balance"].(map[string]any)["amount"] != "20.00" {
		t.Fatalf("final wallet: %v", r.body)
	}
}
