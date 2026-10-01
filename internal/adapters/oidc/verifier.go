// Package oidc validates OAuth 2.0 access tokens issued by the external IdP
// (Keycloak) and turns them into a Principal.
package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
)

// ErrInvalidToken covers missing, malformed, badly signed, expired, wrong-issuer
// or wrong-audience tokens. It maps to 401.
var ErrInvalidToken = errors.New("oidc: invalid token")

// Principal is the authenticated caller.
type Principal struct {
	Subject  string
	ClientID string // azp
	// ProviderID is set only for game-provider clients (hardcoded claim).
	ProviderID string
	scopes     []string
}

// HasScope reports whether the token grants scope.
func (p Principal) HasScope(scope string) bool { return slices.Contains(p.scopes, scope) }

// IsProvider reports a game-provider identity.
func (p Principal) IsProvider() bool { return p.ProviderID != "" }

// NewPrincipal builds a principal (used by tests and adapters).
func NewPrincipal(subject, clientID, providerID string, scopes ...string) Principal {
	return Principal{Subject: subject, ClientID: clientID, ProviderID: providerID, scopes: scopes}
}

// Config of the verifier. Issuer is the public URL tokens carry in `iss`
// (what clients outside the stack see); JWKSURL is where this process fetches the
// signing keys (the in-network address). Keeping them separate makes tokens
// obtained through the public edge valid inside the network and vice versa.
type Config struct {
	Issuer   string
	JWKSURL  string
	Audience string
}

// Verifier checks signature (RS256, keys cached and refreshed on unknown kid),
// issuer, audience, expiry and token type.
type Verifier struct {
	verifier *gooidc.IDTokenVerifier
}

// NewVerifier builds a verifier. Keys are fetched lazily on first use.
func NewVerifier(cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" || cfg.JWKSURL == "" || cfg.Audience == "" {
		return nil, errors.New("oidc: issuer, jwks url and audience are required")
	}
	// The key set lives as long as the process; its fetches use this client.
	ctx := gooidc.ClientContext(context.Background(), &http.Client{Timeout: 5 * time.Second})
	keys := gooidc.NewRemoteKeySet(ctx, cfg.JWKSURL)
	return &Verifier{verifier: gooidc.NewVerifier(cfg.Issuer, keys, &gooidc.Config{
		ClientID:             cfg.Audience, // enforces aud contains the audience
		SupportedSigningAlgs: []string{gooidc.RS256},
	})}, nil
}

type claims struct {
	Type       string `json:"typ"`
	ClientID   string `json:"azp"`
	ProviderID string `json:"provider_id"`
	Scope      string `json:"scope"`
}

// Verify validates a raw bearer token.
func (v *Verifier) Verify(ctx context.Context, raw string) (Principal, error) {
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	var c claims
	if err := token.Claims(&c); err != nil {
		return Principal{}, fmt.Errorf("%w: claims: %w", ErrInvalidToken, err)
	}
	if !strings.EqualFold(c.Type, "Bearer") {
		return Principal{}, fmt.Errorf("%w: not an access token (typ %q)", ErrInvalidToken, c.Type)
	}
	return Principal{Subject: token.Subject, ClientID: c.ClientID, ProviderID: c.ProviderID, scopes: strings.Fields(c.Scope)}, nil
}
