package wagering

import (
	"errors"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
)

// identifier restricts provider-supplied identifiers to a safe ASCII alphabet, so
// they need no escaping in logs, cursors or the canonical JSON of the hash.
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\-]{0,127}$`)

// RawRequest is what a transport adapter (HTTP or SQS) hands over after decoding.
// Money is already parsed by its strict JSON decoder.
type RawRequest struct {
	Money                          money.Money
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           string
	ReferenceExternalTransactionID string
}

// RequestFields is the validated content of an ExternalRequest.
type RequestFields struct {
	Money                          money.Money
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string // empty when absent
	Kind                           Kind
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
}

// HasReference reports whether the request names a referenced transaction.
func (f RequestFields) HasReference() bool { return f.ReferenceExternalTransactionID != "" }

// ExternalRequest is a provider operation that passed every input rule. It can
// only be built by NewExternalRequest.
type ExternalRequest struct{ f RequestFields }

// Fields returns a copy of the validated content.
func (r ExternalRequest) Fields() RequestFields { return r.f }

// NewExternalRequest parses a raw request. Every failure is a *ValidationError
// (correctable, never persisted) except ErrReservedKind for OPENING.
func NewExternalRequest(raw RawRequest) (ExternalRequest, error) {
	kind, err := ParseExternalKind(raw.Kind)
	if err != nil {
		return ExternalRequest{}, err
	}
	var errs []error
	ident := func(field, v string) string {
		if !identifier.MatchString(v) {
			errs = append(errs, fieldError(field, "must match "+identifier.String()))
		}
		return v
	}
	id := func(field, v string) uuid.UUID {
		u, parseErr := uuid.Parse(v)
		if parseErr != nil || len(v) != 36 || u == uuid.Nil {
			errs = append(errs, fieldError(field, "must be a canonical non-nil UUID"))
		}
		return u
	}
	f := RequestFields{
		ProviderID:            ident("providerId", raw.ProviderID),
		ExternalTransactionID: ident("externalTransactionId", raw.ExternalTransactionID),
		IdempotencyKey:        ident("idempotencyKey", raw.IdempotencyKey),
		RoundID:               ident("roundId", raw.RoundID),
		GameID:                ident("gameId", raw.GameID),
		PlayerID:              id("playerId", strings.ToLower(raw.PlayerID)),
		WalletID:              id("walletId", strings.ToLower(raw.WalletID)),
		Kind:                  kind,
		Money:                 raw.Money,
	}
	if raw.ReferenceExternalTransactionID != "" {
		f.ReferenceExternalTransactionID = ident("referenceExternalTransactionId", raw.ReferenceExternalTransactionID)
	}
	errs = append(errs, kindRules(f)...)
	if len(errs) > 0 {
		return ExternalRequest{}, errors.Join(errs...)
	}
	return ExternalRequest{f: f}, nil
}

// kindRules enforces the zero-amount and reference policy of each kind.
func kindRules(f RequestFields) []error {
	var errs []error
	if !f.Money.IsValid() {
		return []error{fieldError("money", "is required")}
	}
	switch f.Kind {
	case KindLoss:
		if !f.Money.IsZero() {
			errs = append(errs, fieldError("money.amount", "LOSS requires 0.00"))
		}
	case KindBet, KindWin, KindRefund, KindRollback:
		if !f.Money.IsPositive() {
			errs = append(errs, fieldError("money.amount", string(f.Kind)+" requires an amount greater than 0.00"))
		}
	case KindOpening:
		errs = append(errs, ErrReservedKind)
	}
	switch f.Kind {
	case KindRefund, KindRollback:
		if !f.HasReference() {
			errs = append(errs, fieldError("referenceExternalTransactionId", "is required for "+string(f.Kind)))
		}
	case KindBet, KindLoss:
		if f.HasReference() {
			errs = append(errs, fieldError("referenceExternalTransactionId", "is not allowed for "+string(f.Kind)))
		}
	case KindWin, KindOpening:
	}
	if f.HasReference() && f.ReferenceExternalTransactionID == f.ExternalTransactionID {
		errs = append(errs, fieldError("referenceExternalTransactionId", "cannot reference itself"))
	}
	return errs
}
