package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

// problem is the RFC 9457 error body. `code` is the stable machine-readable value.
type problem struct {
	Type          string       `json:"type"`
	Title         string       `json:"title"`
	Code          string       `json:"code"`
	Detail        string       `json:"detail,omitempty"`
	CorrelationID string       `json:"correlationId,omitempty"`
	Errors        []fieldIssue `json:"errors,omitempty"`
	Status        int          `json:"status"`
}

type fieldIssue struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Stable error codes of the HTTP contract (see api/openapi.yaml).
const (
	codeInvalidRequest      = "INVALID_REQUEST"
	codeReservedKind        = "RESERVED_KIND"
	codeUnauthenticated     = "UNAUTHENTICATED"
	codeInsufficientScope   = "INSUFFICIENT_SCOPE"
	codeProviderMismatch    = "PROVIDER_MISMATCH"
	codeWalletNotFound      = "WALLET_NOT_FOUND"
	codeTransactionNotFound = "TRANSACTION_NOT_FOUND"
	codeIdempotencyConflict = "IDEMPOTENCY_KEY_REUSED"
	codeDuplicateExternal   = "DUPLICATE_EXTERNAL_TRANSACTION"
	codeWalletExists        = "WALLET_ALREADY_EXISTS"
	codeTemporarilyUnavail  = "TEMPORARILY_UNAVAILABLE"
	codeInternal            = "INTERNAL_ERROR"
	codePayloadTooLarge     = "PAYLOAD_TOO_LARGE"
)

func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string, issues ...fieldIssue) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{ // client went away
		Type: "https://jungle.lab.fredzol.io/problems/" + code, Title: http.StatusText(status), Status: status,
		Code: code, Detail: detail, CorrelationID: correlationID(r.Context()), Errors: issues,
	})
}

// writeError maps use-case and domain errors to the HTTP contract.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if issues := validationIssues(err); len(issues) > 0 {
		writeProblem(w, r, http.StatusBadRequest, codeInvalidRequest, "the request has invalid fields", issues...)
		return
	}
	var maxBytes *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytes):
		writeProblem(w, r, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "request body too large")
	case errors.Is(err, money.ErrInvalidAmount), errors.Is(err, money.ErrInvalidCurrency):
		writeProblem(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error(), fieldIssue{Field: "money", Reason: err.Error()})
	case errors.Is(err, wagering.ErrReservedKind):
		writeProblem(w, r, http.StatusBadRequest, codeReservedKind, "OPENING is reserved for internal wallet opening")
	case errors.Is(err, app.ErrProviderMismatch):
		writeProblem(w, r, http.StatusForbidden, codeProviderMismatch, "providerId does not match the authenticated provider")
	case errors.Is(err, app.ErrWalletNotFound):
		writeProblem(w, r, http.StatusNotFound, codeWalletNotFound, "wallet not found")
	case errors.Is(err, app.ErrTransactionNotFound):
		writeProblem(w, r, http.StatusNotFound, codeTransactionNotFound, "transaction not found")
	case errors.Is(err, app.ErrIdempotencyConflict):
		writeProblem(w, r, http.StatusConflict, codeIdempotencyConflict, "Idempotency-Key was already used with a different payload")
	case errors.Is(err, app.ErrDuplicateExternalTransaction):
		writeProblem(w, r, http.StatusConflict, codeDuplicateExternal, "this externalTransactionId was already submitted with another Idempotency-Key")
	case errors.Is(err, app.ErrWalletExists):
		writeProblem(w, r, http.StatusConflict, codeWalletExists, "a wallet for this player and currency already exists")
	case errors.Is(err, app.ErrTransient), errors.Is(err, context.DeadlineExceeded):
		log.WarnContext(r.Context(), "transient failure", slog.Any("error", err))
		w.Header().Set("Retry-After", "1")
		writeProblem(w, r, http.StatusServiceUnavailable, codeTemporarilyUnavail, "temporarily unavailable; retry with the same Idempotency-Key")
	default:
		log.ErrorContext(r.Context(), "request failed", slog.Any("error", err))
		writeProblem(w, r, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// validationIssues flattens (possibly joined) domain validation errors.
func validationIssues(err error) []fieldIssue {
	var out []fieldIssue
	var walk func(error)
	walk = func(e error) {
		var verr *wagering.ValidationError
		if errors.As(e, &verr) && verr == e { //nolint:errorlint // exact match on purpose: wrapped and joined errors are walked via Unwrap below
			out = append(out, fieldIssue{Field: verr.Field, Reason: verr.Reason})
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, inner := range joined.Unwrap() {
				walk(inner)
			}
			return
		}
		if errors.As(e, &verr) {
			out = append(out, fieldIssue{Field: verr.Field, Reason: verr.Reason})
		}
	}
	if err != nil {
		walk(err)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) // client went away
}
