// Package sqsconsumer consumes wager-transactions.fifo: it parses and authorizes
// each message, applies it through the same use case as HTTP and removes it from
// the queue only after the durable handling committed.
package sqsconsumer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

// MessageType is the only accepted envelope type.
const MessageType = "WagerTransactionRequested"

var messageIDFormat = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\-]{0,127}$`)

// errMalformed marks permanently invalid messages (straight to the DLQ).
var errMalformed = errors.New("sqsconsumer: malformed message")

type envelopeData struct {
	Money                          *money.Money `json:"money"`
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	IdempotencyKey                 string       `json:"idempotencyKey"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           string       `json:"kind"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId"`
}

type envelope struct {
	Data       *envelopeData `json:"data"`
	MessageID  string        `json:"messageId"`
	Type       string        `json:"type"`
	OccurredAt string        `json:"occurredAt"`
}

// message is a parsed, validated envelope.
type message struct {
	request wagering.ExternalRequest
	id      string
	hash    string
}

// parse decodes the body strictly and validates it into a domain request.
// The message hash covers the envelope identity and the business payload hash
// (plus the idempotency key), so a redelivery with altered content is detected.
func parse(body string) (message, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(body)))
	dec.DisallowUnknownFields()
	var env envelope
	if err := dec.Decode(&env); err != nil {
		return message{}, fmt.Errorf("%w: %w", errMalformed, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return message{}, fmt.Errorf("%w: trailing data", errMalformed)
	}
	switch {
	case !messageIDFormat.MatchString(env.MessageID):
		return message{}, fmt.Errorf("%w: invalid messageId", errMalformed)
	case env.Type != MessageType:
		return message{}, fmt.Errorf("%w: unsupported type %q", errMalformed, env.Type)
	case env.Data == nil || env.Data.Money == nil:
		return message{}, fmt.Errorf("%w: missing data or money", errMalformed)
	}
	if _, err := time.Parse(time.RFC3339Nano, env.OccurredAt); err != nil {
		return message{}, fmt.Errorf("%w: occurredAt must be RFC 3339", errMalformed)
	}
	d := env.Data
	req, err := wagering.NewExternalRequest(wagering.RawRequest{
		ProviderID: d.ProviderID, ExternalTransactionID: d.ExternalTransactionID, IdempotencyKey: d.IdempotencyKey,
		PlayerID: d.PlayerID, WalletID: d.WalletID, RoundID: d.RoundID, GameID: d.GameID, Kind: d.Kind,
		Money: *d.Money, ReferenceExternalTransactionID: d.ReferenceExternalTransactionID,
	})
	if err != nil {
		return message{}, fmt.Errorf("%w: %w", errMalformed, err)
	}
	canonical, _ := json.Marshal(map[string]string{ //nolint:errcheck // map of strings cannot fail
		"idempotencyKey": d.IdempotencyKey, "messageId": env.MessageID, "occurredAt": env.OccurredAt,
		"payloadHash": req.PayloadHash(), "type": env.Type,
	})
	sum := sha256.Sum256(canonical)
	return message{request: req, id: env.MessageID, hash: hex.EncodeToString(sum[:])}, nil
}
