package money

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

// Errors are classifiable with errors.Is.
var (
	ErrInvalidAmount    = errors.New("money: invalid amount")
	ErrInvalidCurrency  = errors.New("money: invalid currency")
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	ErrOverflow         = errors.New("money: amount overflow")
	ErrUninitialized    = errors.New("money: uninitialized value")
)

// Money is immutable. Representation: int64 minor units, so the range is
// ±92,233,720,368,547,758.07 in a two-decimal currency.
type Money struct {
	currency Currency
	minor    int64
}

// externalAmount is the only accepted external format: non-negative, no sign, no
// leading zeros, exactly two decimals. Rejects "", NaN, Infinity, 1e3, 1.0, 1.000,
// -1.00, +1.00, 01.00 and whitespace. Nothing is normalized, so the string a client
// sent is already canonical for the idempotency hash.
var externalAmount = regexp.MustCompile(`^(0|[1-9][0-9]{0,16})\.([0-9]{2})$`)

// Parse builds Money from the external decimal contract (e.g. "25.00", "BRL").
func Parse(amount, currency string) (Money, error) {
	cur, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	match := externalAmount.FindStringSubmatch(amount)
	if match == nil {
		return Money{}, fmt.Errorf("%w: %q (expected non-negative decimal with exactly two places)", ErrInvalidAmount, amount)
	}
	units, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}
	cents, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidAmount, amount)
	}
	if units > (math.MaxInt64-cents)/100 {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}
	return Money{currency: cur, minor: units*100 + cents}, nil
}

// FromMinor rebuilds Money from persisted minor units (any sign).
func FromMinor(minor int64, currency Currency) (Money, error) {
	if !currency.IsValid() {
		return Money{}, ErrUninitialized
	}
	return Money{currency: currency, minor: minor}, nil
}

// Zero returns the zero amount of a currency.
func Zero(currency Currency) (Money, error) { return FromMinor(0, currency) }

// Minor returns the amount in minor units.
func (m Money) Minor() int64 { return m.minor }

// Currency returns the currency.
func (m Money) Currency() Currency { return m.currency }

// IsValid reports whether m was built by a constructor.
func (m Money) IsValid() bool { return m.currency.IsValid() }

// IsZero, IsPositive and IsNegative inspect the sign.
func (m Money) IsZero() bool     { return m.minor == 0 }
func (m Money) IsPositive() bool { return m.minor > 0 }
func (m Money) IsNegative() bool { return m.minor < 0 }

// Add returns m + o.
func (m Money) Add(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	if (o.minor > 0 && m.minor > math.MaxInt64-o.minor) || (o.minor < 0 && m.minor < math.MinInt64-o.minor) {
		return Money{}, ErrOverflow
	}
	return Money{currency: m.currency, minor: m.minor + o.minor}, nil
}

// Sub returns m - o.
func (m Money) Sub(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	if (o.minor < 0 && m.minor > math.MaxInt64+o.minor) || (o.minor > 0 && m.minor < math.MinInt64+o.minor) {
		return Money{}, ErrOverflow
	}
	return Money{currency: m.currency, minor: m.minor - o.minor}, nil
}

// Neg returns -m.
func (m Money) Neg() (Money, error) {
	if !m.IsValid() {
		return Money{}, ErrUninitialized
	}
	if m.minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{currency: m.currency, minor: -m.minor}, nil
}

// Cmp returns -1, 0 or +1 comparing m with o.
func (m Money) Cmp(o Money) (int, error) {
	if err := m.compatible(o); err != nil {
		return 0, err
	}
	switch {
	case m.minor < o.minor:
		return -1, nil
	case m.minor > o.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports value and currency equality.
func (m Money) Equal(o Money) bool { return m.currency == o.currency && m.minor == o.minor }

func (m Money) compatible(o Money) error {
	if !m.IsValid() || !o.IsValid() {
		return ErrUninitialized
	}
	if m.currency != o.currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	return nil
}

// Amount renders the decimal amount with two places, e.g. "975.00" or "-20.00".
func (m Money) Amount() string {
	sign := ""
	abs := uint64(m.minor) //nolint:gosec // reinterpreted below for negatives (handles MinInt64)
	if m.minor < 0 {
		sign = "-"
		abs = uint64(^m.minor) + 1 //nolint:gosec // two's complement magnitude of a negative int64
	}
	return fmt.Sprintf("%s%d.%02d", sign, abs/100, abs%100)
}

func (m Money) String() string { return m.Amount() + " " + m.currency.Code() }
