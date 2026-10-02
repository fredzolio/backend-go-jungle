package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/adapters/oidc"
)

type ctxKey int

const (
	keyCorrelation ctxKey = iota
	keyPrincipal
	keyInfo
)

// requestInfo lets inner handlers report data (the principal) to the request log.
type requestInfo struct{ principal *oidc.Principal }

// TokenVerifier validates bearer tokens.
type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (oidc.Principal, error)
}

var correlationFormat = regexp.MustCompile(`^[A-Za-z0-9._:\-]{1,64}$`)

func correlationID(ctx context.Context) string {
	id, _ := ctx.Value(keyCorrelation).(string) // absent => ""
	return id
}

func principalFrom(ctx context.Context) (oidc.Principal, bool) {
	p, ok := ctx.Value(keyPrincipal).(oidc.Principal)
	return p, ok
}

// observe assigns the correlation id (client-supplied when well-formed), logs one
// structured line per request (never bodies or credentials) and recovers panics.
func observe(log *slog.Logger, obs RequestObserver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Correlation-Id")
		if !correlationFormat.MatchString(id) {
			id = uuid.Must(uuid.NewV7()).String()
		}
		w.Header().Set("X-Correlation-Id", id)
		info := &requestInfo{}
		r = r.WithContext(context.WithValue(context.WithValue(r.Context(), keyCorrelation, id), keyInfo, info))
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				log.ErrorContext(r.Context(), "panic", slog.Any("panic", v), slog.String("correlationId", id))
				writeProblem(rec, r, http.StatusInternalServerError, codeInternal, "internal error")
			}
			attrs := []any{
				slog.String("method", r.Method), slog.String("route", r.Pattern), slog.Int("status", rec.status),
				slog.Int64("durationMs", time.Since(start).Milliseconds()), slog.String("correlationId", id),
			}
			if p := info.principal; p != nil {
				attrs = append(attrs, slog.String("clientId", p.ClientID), slog.String("providerId", p.ProviderID))
			}
			log.InfoContext(r.Context(), "http request", attrs...)
			if obs != nil {
				obs.ObserveHTTP(r.Pattern, rec.status, time.Since(start))
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// secured authenticates the bearer token and requires scope before calling h.
func secured(v TokenVerifier, scope string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(raw) == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="jungle"`)
			writeProblem(w, r, http.StatusUnauthorized, codeUnauthenticated, "missing bearer token")
			return
		}
		p, err := v.Verify(r.Context(), strings.TrimSpace(raw))
		if err != nil {
			if !errors.Is(err, oidc.ErrInvalidToken) {
				writeProblem(w, r, http.StatusServiceUnavailable, codeTemporarilyUnavail, "cannot validate credentials right now")
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="jungle", error="invalid_token"`)
			writeProblem(w, r, http.StatusUnauthorized, codeUnauthenticated, "invalid or expired token")
			return
		}
		ctx := context.WithValue(r.Context(), keyPrincipal, p)
		if info, ok := r.Context().Value(keyInfo).(*requestInfo); ok {
			info.principal = &p
		}
		if !p.HasScope(scope) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="jungle", error="insufficient_scope", scope="`+scope+`"`)
			writeProblem(w, r.WithContext(ctx), http.StatusForbidden, codeInsufficientScope, "token lacks scope "+scope)
			return
		}
		h(w, r.WithContext(ctx))
	}
}
