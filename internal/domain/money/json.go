package money

import (
	"encoding/json"
	"errors"
	"fmt"
)

type wire struct {
	Amount   *string `json:"amount"`
	Currency *string `json:"currency"`
}

// MarshalJSON renders {"amount":"25.00","currency":"BRL"}.
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.IsValid() {
		return nil, ErrUninitialized
	}
	amount, code := m.Amount(), m.currency.Code()
	out, err := json.Marshal(wire{Amount: &amount, Currency: &code})
	if err != nil {
		return nil, fmt.Errorf("marshal money: %w", err)
	}
	return out, nil
}

// UnmarshalJSON parses the external contract. Amounts must be JSON strings: a
// JSON number is rejected so no value ever passes through a float.
func (m *Money) UnmarshalJSON(data []byte) error {
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return fmt.Errorf("%w: %s must be a string", ErrInvalidAmount, typeErr.Field)
		}
		return fmt.Errorf("%w: %w", ErrInvalidAmount, err)
	}
	if w.Amount == nil {
		return fmt.Errorf("%w: missing amount", ErrInvalidAmount)
	}
	if w.Currency == nil {
		return fmt.Errorf("%w: missing currency", ErrInvalidCurrency)
	}
	parsed, err := Parse(*w.Amount, *w.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}
