package wagering

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// PayloadHash is the deterministic fingerprint of the business content of a
// request, identical for HTTP and SQS.
//
// Algorithm (v1): SHA-256, lower-case hex, over the RFC 8785 (JCS) canonical JSON of
//
//	{externalTransactionId, gameId, kind, money{amount, currency}, playerId,
//	 providerId, [referenceExternalTransactionId], roundId, walletId}
//
// Keys sorted, no whitespace. referenceExternalTransactionId is omitted when absent.
// The idempotency key and transport metadata (headers, SQS envelope) are excluded.
// Normalization before hashing: UUIDs in canonical lower case. Amounts need none
// (the parser accepts only the canonical "0.00" form); identifiers are restricted
// to [A-Za-z0-9._:-], so JSON string encoding is escape-free and JCS-exact.
func (r ExternalRequest) PayloadHash() string {
	f := r.f
	doc := map[string]any{
		"externalTransactionId": f.ExternalTransactionID,
		"gameId":                f.GameID,
		"kind":                  string(f.Kind),
		"money":                 map[string]string{"amount": f.Money.Amount(), "currency": f.Money.Currency().Code()},
		"playerId":              f.PlayerID.String(),
		"providerId":            f.ProviderID,
		"roundId":               f.RoundID,
		"walletId":              f.WalletID.String(),
	}
	if f.HasReference() {
		doc["referenceExternalTransactionId"] = f.ReferenceExternalTransactionID
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// Encoding a map of strings cannot fail; maps are emitted with sorted keys.
	_ = enc.Encode(doc) //nolint:errcheck // infallible for map[string]any of strings
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}
