package sqsconsumer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
)

// errUnauthorizedSender: the SQS sender may not speak for the payload provider.
var errUnauthorizedSender = errors.New("sqsconsumer: sender not authorized for provider")

// Senders authorizes the SQS sender identity (the SenderId system attribute) for
// the providerId in the payload. The broker policy already restricts who can
// send; this binds each sender to its own provider (no cross-provider spoofing).
//
// MiniStack reports the account id as SenderId (not the IAM user), so locally the
// account is listed in TrustedAccounts: any known provider is accepted from it and
// isolation relies on the broker policy. In real AWS SenderId is the IAM user id
// (AIDA...) recorded in the senders file, and TrustedAccounts stays empty.
type Senders struct {
	byProvider      map[string]senderIdentity
	trustedAccounts []string
}

type senderIdentity struct {
	AccessKeyID string `json:"access_key_id"`
	UserID      string `json:"user_id"`
}

// LoadSenders reads the provider => identity map written by Terraform.
func LoadSenders(path string, trustedAccounts []string) (*Senders, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path comes from trusted configuration
	if err != nil {
		return nil, fmt.Errorf("read senders file: %w", err)
	}
	var byProvider map[string]senderIdentity
	if err := json.Unmarshal(raw, &byProvider); err != nil {
		return nil, fmt.Errorf("parse senders file: %w", err)
	}
	return &Senders{byProvider: byProvider, trustedAccounts: trustedAccounts}, nil
}

// Authorize checks that sender may send operations of providerID.
func (s *Senders) Authorize(sender, providerID string) error {
	id, known := s.byProvider[providerID]
	switch {
	case !known:
		return fmt.Errorf("%w: unknown provider %q", errUnauthorizedSender, providerID)
	case sender != "" && (sender == id.UserID || sender == id.AccessKeyID):
		return nil
	case sender != "" && slices.Contains(s.trustedAccounts, sender):
		return nil
	default:
		return fmt.Errorf("%w: sender %q, provider %q", errUnauthorizedSender, sender, providerID)
	}
}
