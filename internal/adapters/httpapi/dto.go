package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

const maxBodyBytes = 64 << 10

// decodeStrict reads exactly one JSON object, rejecting unknown fields and
// trailing data.
func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) || errors.Is(err, money.ErrInvalidAmount) || errors.Is(err, money.ErrInvalidCurrency) {
			return err
		}
		return &wagering.ValidationError{Field: "body", Reason: "malformed JSON: " + err.Error()}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return &wagering.ValidationError{Field: "body", Reason: "must contain a single JSON object"}
	}
	return nil
}

type walletResponse struct {
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
	ID       uuid.UUID   `json:"id"`
	PlayerID uuid.UUID   `json:"playerId"`
}

func walletBody(s wallet.Snapshot) walletResponse {
	return walletResponse{ID: s.ID, PlayerID: s.PlayerID, Balance: s.Balance, Version: s.Version}
}

type ledgerEntryResponse struct {
	CreatedAt     time.Time        `json:"createdAt"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	Direction     wallet.Direction `json:"direction"`
	WalletVersion int64            `json:"walletVersion"`
	ID            uuid.UUID        `json:"id"`
	TransactionID uuid.UUID        `json:"transactionId"`
}

type ledgerResponse struct {
	NextCursor *string               `json:"nextCursor"`
	Items      []ledgerEntryResponse `json:"items"`
}

// submitResponse is the outcome of POST /wagering/transactions (and its replays).
type submitResponse struct {
	Balance          *money.Money         `json:"balance,omitempty"`
	Status           wagering.Status      `json:"status"`
	FailureCode      wagering.FailureCode `json:"failureCode,omitempty"`
	TransactionID    uuid.UUID            `json:"transactionId"`
	IdempotentReplay bool                 `json:"idempotentReplay"`
}

func submitBody(s wagering.Snapshot, replay bool) submitResponse {
	out := submitResponse{TransactionID: s.ID, Status: s.Status, FailureCode: s.FailureCode, IdempotentReplay: replay}
	if s.Result != nil {
		out.Balance = &s.Result.Balance
	}
	return out
}

// transactionResponse is the full view used to follow a transaction.
type transactionResponse struct {
	CreatedAt                      time.Time            `json:"createdAt"`
	UpdatedAt                      time.Time            `json:"updatedAt"`
	ProcessedAt                    *time.Time           `json:"processedAt,omitempty"`
	NextAttemptAt                  *time.Time           `json:"nextAttemptAt,omitempty"`
	ReferenceDeadline              *time.Time           `json:"referenceDeadline,omitempty"`
	Balance                        *money.Money         `json:"balance,omitempty"`
	WalletVersion                  *int64               `json:"walletVersion,omitempty"`
	ReferenceTransactionID         *uuid.UUID           `json:"referenceTransactionId,omitempty"`
	Money                          money.Money          `json:"money"`
	ProviderID                     string               `json:"providerId,omitempty"`
	ExternalTransactionID          string               `json:"externalTransactionId,omitempty"`
	RoundID                        string               `json:"roundId,omitempty"`
	GameID                         string               `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string               `json:"referenceExternalTransactionId,omitempty"`
	Kind                           wagering.Kind        `json:"kind"`
	Status                         wagering.Status      `json:"status"`
	Origin                         wagering.Origin      `json:"origin"`
	FailureCode                    wagering.FailureCode `json:"failureCode,omitempty"`
	Attempts                       int                  `json:"attempts"`
	TransactionID                  uuid.UUID            `json:"transactionId"`
	WalletID                       uuid.UUID            `json:"walletId"`
	PlayerID                       uuid.UUID            `json:"playerId"`
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func transactionBody(s wagering.Snapshot) transactionResponse {
	out := transactionResponse{
		TransactionID: s.ID, Origin: s.Origin, Kind: s.Kind, Status: s.Status, WalletID: s.WalletID, PlayerID: s.PlayerID,
		Money: s.Money, FailureCode: s.FailureCode, Attempts: s.Attempts, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
		ProcessedAt: optionalTime(s.ProcessedAt), NextAttemptAt: optionalTime(s.NextAttemptAt), ReferenceDeadline: optionalTime(s.ReferenceDeadline),
	}
	if e := s.External; e != nil {
		out.ProviderID, out.ExternalTransactionID, out.RoundID, out.GameID = e.ProviderID, e.ExternalTransactionID, e.RoundID, e.GameID
		out.ReferenceExternalTransactionID = e.ReferenceExternalTransactionID
	}
	if r := s.Result; r != nil {
		out.Balance, out.WalletVersion = &r.Balance, &r.WalletVersion
	}
	if s.ReferenceTransactionID != uuid.Nil {
		out.ReferenceTransactionID = &s.ReferenceTransactionID
	}
	return out
}

// cursor is opaque to clients: base64url(JSON) bound to the wallet it pages.
type cursor struct {
	WalletID uuid.UUID `json:"w"`
	Version  int64     `json:"v"`
}

func encodeCursor(walletID uuid.UUID, version int64) string {
	raw, _ := json.Marshal(cursor{WalletID: walletID, Version: version}) // infallible
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string, walletID uuid.UUID) (int64, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	var c cursor
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err != nil || c.WalletID != walletID || c.Version < 0 {
		return 0, &wagering.ValidationError{Field: "cursor", Reason: "invalid cursor for this wallet"}
	}
	return c.Version, nil
}

func parseUUID(field, s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil || len(s) != 36 {
		return uuid.Nil, &wagering.ValidationError{Field: field, Reason: fmt.Sprintf("must be a canonical UUID, got %q", s)}
	}
	return id, nil
}
