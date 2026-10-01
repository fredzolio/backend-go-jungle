package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/fredzolio/backend-go-jungle/internal/app"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

// Handlers implements the business routes.
type Handlers struct {
	Wallets  *app.Wallets
	Wagering *app.Wagering
	Queries  *app.Queries
	Log      *slog.Logger
}

func (h Handlers) meta(r *http.Request) app.Meta {
	return app.Meta{CorrelationID: correlationID(r.Context())}
}

type openWalletRequest struct {
	InitialBalance *money.Money `json:"initialBalance"`
	PlayerID       string       `json:"playerId"`
}

// openWallet: POST /wallets (internal service only).
func (h Handlers) openWallet(w http.ResponseWriter, r *http.Request) {
	var req openWalletRequest
	if err := decodeStrict(w, r, &req); err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	playerID, err := parseUUID("playerId", req.PlayerID)
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	if req.InitialBalance == nil {
		writeError(w, r, h.Log, &wagering.ValidationError{Field: "initialBalance", Reason: "is required"})
		return
	}
	snap, err := h.Wallets.Open(r.Context(), app.OpenWalletCommand{PlayerID: playerID, InitialBalance: *req.InitialBalance, Meta: h.meta(r)})
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	w.Header().Set("Location", "/wallets/"+snap.ID.String())
	writeJSON(w, http.StatusCreated, walletBody(snap))
}

// getWallet: GET /wallets/{walletId}.
func (h Handlers) getWallet(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	snap, err := h.Queries.Wallet(r.Context(), id)
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	writeJSON(w, http.StatusOK, walletBody(snap))
}

// ledger: GET /wallets/{walletId}/ledger?cursor=...&limit=50.
func (h Handlers) ledger(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	after, err := decodeCursor(r.URL.Query().Get("cursor"), id)
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			writeError(w, r, h.Log, &wagering.ValidationError{Field: "limit", Reason: "must be an integer between 1 and 200"})
			return
		}
	}
	page, err := h.Queries.Ledger(r.Context(), id, after, limit)
	if err != nil {
		writeError(w, r, h.Log, err)
		return
	}
	out := ledgerResponse{Items: make([]ledgerEntryResponse, 0, len(page.Entries))}
	for _, e := range page.Entries {
		d := e.Data()
		out.Items = append(out.Items, ledgerEntryResponse{
			ID: d.ID, TransactionID: d.TransactionID, Direction: d.Direction, Money: d.Amount,
			BalanceBefore: d.BalanceBefore, BalanceAfter: d.BalanceAfter, WalletVersion: d.WalletVersion, CreatedAt: d.CreatedAt,
		})
	}
	if page.More {
		next := encodeCursor(id, out.Items[len(out.Items)-1].WalletVersion)
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}
