//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/httpapi"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/oidc"
	"github.com/fredzolio/backend-go-jungle/internal/adapters/postgres"
	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/platform/health"
	"github.com/fredzolio/backend-go-jungle/internal/testsupport/pgtest"
)

const issuer = "https://idp.test/auth/realms/jungle"

var (
	env    *pgtest.Env
	idp    *testIdP
	silent = slog.New(slog.NewTextHandler(io.Discard, nil))
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	if env, err = pgtest.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	idp = newTestIdP()
	code := m.Run()
	idp.jwks.Close()
	_ = env.Stop(ctx)
	os.Exit(code)
}

// testIdP signs RS256 tokens and serves its JWKS, like Keycloak does. The API
// verifies them with the production verifier (go-oidc), not a stub.
type testIdP struct {
	key, rogue *rsa.PrivateKey
	jwks       *httptest.Server
}

func newTestIdP() *testIdP {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	rogue, _ := rsa.GenerateKey(rand.Reader, 2048)
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(set)
	}))
	return &testIdP{key: key, rogue: rogue, jwks: srv}
}

// tokenSpec describes a token; zero values mean "valid default".
type tokenSpec struct {
	clientID, provider, scope, iss, aud, typ string
	exp                                      time.Duration
	rogue                                    bool
}

func (p *testIdP) token(t *testing.T, s tokenSpec) string {
	t.Helper()
	def := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	signingKey := p.key
	if s.rogue {
		signingKey = p.rogue
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signingKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	exp := s.exp
	if exp == 0 {
		exp = 5 * time.Minute
	}
	claims := map[string]any{
		"iss": def(s.iss, issuer), "aud": def(s.aud, "jungle-api"), "sub": "svc-" + s.clientID,
		"azp": s.clientID, "typ": def(s.typ, "Bearer"), "scope": s.scope,
		"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(exp).Unix(),
	}
	if s.provider != "" {
		claims["provider_id"] = s.provider
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (p *testIdP) provider(t *testing.T, id string) string {
	return p.token(t, tokenSpec{clientID: id, provider: id, scope: "wagering.write wagering.read"})
}

func (p *testIdP) internal(t *testing.T) string {
	return p.token(t, tokenSpec{clientID: "jungle-internal", scope: "wallets.write wallets.read wallets.reconcile wagering.read"})
}

// api is one HTTP server over an isolated database.
type api struct {
	srv *httptest.Server
	db  pgtest.DB
}

func newAPI(t *testing.T) api {
	t.Helper()
	db := env.NewDatabase(t)
	deps := app.Deps{UoW: postgres.NewUnitOfWork(db.App), Clock: app.SystemClock{}, IDs: app.UUIDv7{}}
	verifier, err := oidc.NewVerifier(oidc.Config{Issuer: issuer, JWKSURL: idp.jwks.URL, Audience: "jungle-api"})
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewHandler(httpapi.Routes{
		Health: health.NewRegistry(silent, nil), Verifier: verifier, Log: silent,
		Handlers: httpapi.Handlers{
			Wallets: app.NewWallets(deps), Wagering: app.NewWagering(deps, app.DefaultReferencePolicy),
			Queries: app.NewQueries(deps), Log: silent,
		},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return api{srv: srv, db: db}
}

type response struct {
	header http.Header
	body   map[string]any
	status int
}

func (a api) call(t *testing.T, method, path, token string, body any, headers ...string) response {
	t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := response{status: res.StatusCode, header: res.Header, body: map[string]any{}}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (a api) openWallet(t *testing.T, playerID, balance string) map[string]any {
	t.Helper()
	r := a.call(t, "POST", "/wallets", idp.internal(t), map[string]any{
		"playerId": playerID, "initialBalance": map[string]string{"amount": balance, "currency": "BRL"},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("open wallet: %d %v", r.status, r.body)
	}
	return r.body
}

func bet(wallet map[string]any, provider, extID, kind, amount, ref string) map[string]any {
	b := map[string]any{
		"providerId": provider, "externalTransactionId": extID, "playerId": wallet["playerId"], "walletId": wallet["id"],
		"roundId": "round-987", "gameId": "fortune-chimp", "kind": kind,
		"money": map[string]string{"amount": amount, "currency": "BRL"},
	}
	if ref != "" {
		b["referenceExternalTransactionId"] = ref
	}
	return b
}
