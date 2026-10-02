package httpapi

import (
	"net/http"

	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

type submitRequest struct {
	Money                          *money.Money `json:"money"`
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           string       `json:"kind"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId"`
}

// submit: POST /wagering/transactions (game providers). The Idempotency-Key header
// is mandatory and used as sent; it is never replaced by a computed key.
//
//	200 PROCESSED (first answer and replays alike)
//	202 PENDING_REFERENCE (Location to follow it)
//	422 REJECTED (business rule; body carries failureCode)
func (h Handlers) submit(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context()) // set by secured()
	if !principal.IsProvider() {
		writeProblem(w, r, http.StatusForbidden, codeInsufficientScope, "only game-provider identities submit operations")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, r, h.Log, &wagering.ValidationError{Field: "Idempotency-Key", Reason: "header is required"})
		return
	}
	var body submitRequest
	if err := decodeStrict(w, r, &body); err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	raw := wagering.RawRequest{
		ProviderID: body.ProviderID, ExternalTransactionID: body.ExternalTransactionID, IdempotencyKey: key,
		PlayerID: body.PlayerID, WalletID: body.WalletID, RoundID: body.RoundID, GameID: body.GameID,
		Kind: body.Kind, ReferenceExternalTransactionID: body.ReferenceExternalTransactionID,
	}
	if body.Money != nil {
		raw.Money = *body.Money
	}
	req, err := wagering.NewExternalRequest(raw)
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	res, err := h.Wagering.Submit(r.Context(), app.SubmitCommand{Request: req, AuthenticatedProvider: principal.ProviderID, Meta: h.meta(r)})
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	status := http.StatusOK
	switch res.Transaction.Status {
	case wagering.StatusPendingReference, wagering.StatusPending:
		status = http.StatusAccepted
		w.Header().Set("Location", "/wagering/transactions/"+res.Transaction.ID.String())
	case wagering.StatusRejected:
		status = http.StatusUnprocessableEntity
	case wagering.StatusFailed:
		status = http.StatusInternalServerError
	case wagering.StatusProcessed:
	}
	writeJSON(w, status, submitBody(res.Transaction, res.Replay))
}

// getTransaction: GET /wagering/transactions/{transactionId}. A provider only sees
// its own transactions; anything else is 404 (existence is not disclosed).
func (h Handlers) getTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID("transactionId", r.PathValue("transactionId"))
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	snap, err := h.Queries.Transaction(r.Context(), id)
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	if principal, _ := principalFrom(r.Context()); principal.IsProvider() && // set by secured()
		(snap.External == nil || snap.External.ProviderID != principal.ProviderID) {
		writeError(w, r, h.Log, app.ErrTransactionNotFound)
		return
	}
	writeJSON(w, http.StatusOK, transactionBody(snap))
}

// getProviderTransaction: GET /providers/{providerId}/wagering/transactions/{externalTransactionId}.
func (h Handlers) getProviderTransaction(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerId")
	if principal, _ := principalFrom(r.Context()); principal.IsProvider() && principal.ProviderID != providerID { // set by secured()
		writeError(w, r, h.Log, app.ErrTransactionNotFound)
		return
	}
	snap, err := h.Queries.ProviderTransaction(r.Context(), providerID, r.PathValue("externalTransactionId"))
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionBody(snap))
}
