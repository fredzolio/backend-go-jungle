package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/fredzolio/backend-go-jungle/api"
	"github.com/fredzolio/backend-go-jungle/internal/platform/health"
)

// Scopes granted by the IdP (see infra/terraform/modules/keycloak_realm).
const (
	scopeWalletsWrite     = "wallets.write"
	scopeWalletsRead      = "wallets.read"
	scopeWalletsReconcile = "wallets.reconcile"
	scopeWageringWrite    = "wagering.write"
	scopeWageringRead     = "wagering.read"
)

// RequestObserver records one measurement per request (metrics); optional.
type RequestObserver interface {
	ObserveHTTP(route string, status int, elapsed time.Duration)
}

// Routes groups what the router needs.
type Routes struct {
	Health   *health.Registry
	Verifier TokenVerifier
	Observer RequestObserver
	Log      *slog.Logger
	Handlers Handlers
}

// NewHandler registers every route. Wallet operations are internal-only (wallet
// scopes exist only on the internal client); providers hold wagering scopes.
func NewHandler(rt Routes) http.Handler {
	h, v := rt.Handlers, rt.Verifier
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", rt.Health.Live)
	mux.HandleFunc("GET /health/ready", rt.Health.Ready)
	mux.HandleFunc("GET /openapi.yaml", serveOpenAPI)
	mux.HandleFunc("GET /docs", serveDocs)

	mux.HandleFunc("POST /wallets", secured(v, scopeWalletsWrite, h.openWallet))
	mux.HandleFunc("GET /wallets/{walletId}", secured(v, scopeWalletsRead, h.getWallet))
	mux.HandleFunc("GET /wallets/{walletId}/ledger", secured(v, scopeWalletsRead, h.ledger))
	mux.HandleFunc("POST /wallets/{walletId}/reconciliation", secured(v, scopeWalletsReconcile, h.reconcile))

	mux.HandleFunc("POST /wagering/transactions", secured(v, scopeWageringWrite, h.submit))
	mux.HandleFunc("GET /wagering/transactions/{transactionId}", secured(v, scopeWageringRead, h.getTransaction))
	mux.HandleFunc("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}",
		secured(v, scopeWageringRead, h.getProviderTransaction))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, "NOT_FOUND", "no such route")
	})
	return observe(rt.Log, rt.Observer, mux)
}

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(api.OpenAPI) //nolint:errcheck // client went away
}

// serveDocs renders the contract with Scalar (OAuth2 client-credentials flow).
func serveDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html><head><title>Jungle Wallet API</title><meta charset="utf-8"/>` + //nolint:errcheck // client went away
		`<meta name="viewport" content="width=device-width, initial-scale=1"/></head><body>` +
		`<script id="api-reference" data-url="/openapi.yaml"></script>` +
		`<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1"></script></body></html>`))
}
