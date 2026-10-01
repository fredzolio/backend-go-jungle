// Package money implements the Money value object: an exact amount in minor units
// (int64 cents) tagged with an ISO 4217 currency. Floating point is never used.
package money

import "fmt"

// Currency is an ISO 4217 code supported by the service. Only currencies with
// exactly two minor digits are accepted, because the external contract fixes the
// scale at two decimal places.
type Currency struct{ code string }

// supported lists the accepted ISO 4217 codes (all with two minor digits).
var supported = map[string]struct{}{
	"BRL": {},
	"USD": {},
	"EUR": {},
}

// ParseCurrency accepts an exact, upper-case, supported ISO 4217 code.
func ParseCurrency(code string) (Currency, error) {
	if _, ok := supported[code]; !ok {
		return Currency{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, code)
	}
	return Currency{code: code}, nil
}

// MustCurrency is for compile-time constants in tests and fixtures only.
func MustCurrency(code string) Currency {
	c, err := ParseCurrency(code)
	if err != nil {
		panic(err)
	}
	return c
}

// Code returns the ISO 4217 code.
func (c Currency) Code() string { return c.code }

// IsValid reports whether the currency was built by ParseCurrency.
func (c Currency) IsValid() bool { return c.code != "" }

func (c Currency) String() string { return c.code }
