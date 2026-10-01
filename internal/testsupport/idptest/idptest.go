//go:build integration || system

// Package idptest is a minimal OAuth2 token issuer for tests: it signs RS256
// access tokens shaped like Keycloak's and serves the matching JWKS, so services
// verify them with their production verifier.
package idptest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Issuer is the `iss` of every token.
const Issuer = "https://idp.test/auth/realms/jungle"

// IdP signs tokens and serves its JWKS.
type IdP struct {
	key  *rsa.PrivateKey
	JWKS *httptest.Server
}

// Start creates a key pair and the JWKS server.
func Start() *IdP {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(set) }))
	return &IdP{key: key, JWKS: srv}
}

// Token signs an access token for client with the given scopes and provider.
func (p *IdP) Token(t testing.TB, client, provider, scope string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": Issuer, "aud": "jungle-api", "sub": "svc-" + client, "azp": client, "typ": "Bearer",
		"scope": scope, "iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	if provider != "" {
		claims["provider_id"] = provider
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Provider returns a game-provider token.
func (p *IdP) Provider(t testing.TB, id string) string {
	return p.Token(t, id, id, "wagering.write wagering.read")
}

// Internal returns an internal-service token.
func (p *IdP) Internal(t testing.TB) string {
	return p.Token(t, "jungle-internal", "", "wallets.write wallets.read wallets.reconcile wagering.read")
}
