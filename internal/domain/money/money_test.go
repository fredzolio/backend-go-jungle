package money_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
)

var brl = money.MustCurrency("BRL")

func mustMinor(t *testing.T, minor int64, c money.Currency) money.Money {
	t.Helper()
	m, err := money.FromMinor(minor, c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParse_accepts_canonical_two_decimal_amounts(t *testing.T) {
	cases := map[string]int64{
		"0.00":                 0,
		"0.01":                 1,
		"25.00":                2500,
		"1000.00":              100000,
		"92233720368547758.07": math.MaxInt64,
	}
	for in, want := range cases {
		got, err := money.Parse(in, "BRL")
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got.Minor() != want || got.Amount() != in {
			t.Fatalf("Parse(%q) = %d (%s), want %d", in, got.Minor(), got.Amount(), want)
		}
	}
}

func TestParse_rejects_non_canonical_or_unsafe_amounts(t *testing.T) {
	cases := map[string]error{
		"":                      money.ErrInvalidAmount,
		"NaN":                   money.ErrInvalidAmount,
		"Infinity":              money.ErrInvalidAmount,
		"-Infinity":             money.ErrInvalidAmount,
		"1e3":                   money.ErrInvalidAmount,
		"1.5e2":                 money.ErrInvalidAmount,
		"25":                    money.ErrInvalidAmount,
		"25.0":                  money.ErrInvalidAmount,
		"25.000":                money.ErrInvalidAmount,
		"-1.00":                 money.ErrInvalidAmount,
		"+1.00":                 money.ErrInvalidAmount,
		"01.00":                 money.ErrInvalidAmount,
		" 1.00":                 money.ErrInvalidAmount,
		"1.00 ":                 money.ErrInvalidAmount,
		"1,00":                  money.ErrInvalidAmount,
		".50":                   money.ErrInvalidAmount,
		"0x10.00":               money.ErrInvalidAmount,
		"92233720368547758.08":  money.ErrOverflow,
		"99999999999999999.99":  money.ErrOverflow,
		"999999999999999999.00": money.ErrInvalidAmount,
	}
	for in, want := range cases {
		if _, err := money.Parse(in, "BRL"); !errors.Is(err, want) {
			t.Fatalf("Parse(%q) error = %v, want %v", in, err, want)
		}
	}
}

func TestParse_rejects_unsupported_or_malformed_currency(t *testing.T) {
	for _, code := range []string{"", "brl", "BR", "BRLL", "JPY", "XXX", " BRL"} {
		if _, err := money.Parse("1.00", code); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Fatalf("Parse currency %q error = %v, want ErrInvalidCurrency", code, err)
		}
	}
}

func TestArithmetic_requires_matching_currencies(t *testing.T) {
	a := mustMinor(t, 100, brl)
	b := mustMinor(t, 100, money.MustCurrency("USD"))
	if _, err := a.Add(b); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Add: %v", err)
	}
	if _, err := a.Sub(b); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Sub: %v", err)
	}
	if _, err := a.Cmp(b); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Cmp: %v", err)
	}
}

func TestArithmetic_detects_int64_overflow(t *testing.T) {
	maxV, minV, one := mustMinor(t, math.MaxInt64, brl), mustMinor(t, math.MinInt64, brl), mustMinor(t, 1, brl)
	if _, err := maxV.Add(one); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("max+1: %v", err)
	}
	if _, err := minV.Sub(one); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("min-1: %v", err)
	}
	if _, err := minV.Neg(); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("-min: %v", err)
	}
	if _, err := one.Sub(minV); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("1-min: %v", err)
	}
}

func TestArithmetic_allows_negative_intermediate_results(t *testing.T) {
	a, b := mustMinor(t, 2000, brl), mustMinor(t, 8000, brl)
	diff, err := a.Sub(b)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Amount() != "-60.00" || !diff.IsNegative() {
		t.Fatalf("diff = %s", diff.Amount())
	}
	back, err := diff.Neg()
	if err != nil || back.Amount() != "60.00" {
		t.Fatalf("neg = %s, %v", back.Amount(), err)
	}
	if got := mustMinor(t, math.MinInt64, brl).Amount(); got != "-92233720368547758.08" {
		t.Fatalf("min amount = %s", got)
	}
}

func TestCmp_orders_amounts(t *testing.T) {
	a, b := mustMinor(t, 100, brl), mustMinor(t, 200, brl)
	for _, tc := range []struct {
		x, y money.Money
		want int
	}{{a, b, -1}, {b, a, 1}, {a, a, 0}} {
		got, err := tc.x.Cmp(tc.y)
		if err != nil || got != tc.want {
			t.Fatalf("Cmp = %d, %v; want %d", got, err, tc.want)
		}
	}
}

func TestZeroValue_is_rejected_by_every_operation(t *testing.T) {
	var zero money.Money
	one := mustMinor(t, 1, brl)
	if _, err := zero.Add(one); !errors.Is(err, money.ErrUninitialized) {
		t.Fatalf("Add: %v", err)
	}
	if _, err := one.Sub(zero); !errors.Is(err, money.ErrUninitialized) {
		t.Fatalf("Sub: %v", err)
	}
	if _, err := zero.Neg(); !errors.Is(err, money.ErrUninitialized) {
		t.Fatalf("Neg: %v", err)
	}
	if _, err := json.Marshal(zero); !errors.Is(err, money.ErrUninitialized) {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := money.FromMinor(1, money.Currency{}); !errors.Is(err, money.ErrUninitialized) {
		t.Fatalf("FromMinor: %v", err)
	}
}

func TestJSON_round_trips_the_external_contract(t *testing.T) {
	var m money.Money
	if err := json.Unmarshal([]byte(`{"amount":"25.00","currency":"BRL"}`), &m); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("marshal = %s", out)
	}
}

func TestJSON_rejects_numbers_and_missing_fields(t *testing.T) {
	cases := map[string]error{
		`{"amount":25.00,"currency":"BRL"}`:   money.ErrInvalidAmount,
		`{"amount":"25.00"}`:                  money.ErrInvalidCurrency,
		`{"currency":"BRL"}`:                  money.ErrInvalidAmount,
		`{"amount":"-1.00","currency":"BRL"}`: money.ErrInvalidAmount,
		`{"amount":null,"currency":"BRL"}`:    money.ErrInvalidAmount,
	}
	for in, want := range cases {
		var m money.Money
		if err := json.Unmarshal([]byte(in), &m); !errors.Is(err, want) {
			t.Fatalf("Unmarshal(%s) = %v, want %v", in, err, want)
		}
	}
}

// FuzzParse: any accepted input is already canonical (round-trips byte for byte),
// and parsing never panics.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{"0.00", "25.00", "92233720368547758.07", "1e3", "-1.00", "NaN", "01.00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		m, err := money.Parse(in, "BRL")
		if err != nil {
			return
		}
		if m.Amount() != in {
			t.Fatalf("Parse(%q) round-trips to %q", in, m.Amount())
		}
		if m.IsNegative() {
			t.Fatalf("Parse(%q) produced a negative amount", in)
		}
	})
}
