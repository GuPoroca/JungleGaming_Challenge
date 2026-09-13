package money

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func mustParse(t *testing.T, amount, currency string) Money {
	t.Helper()
	m, err := Parse(amount, currency)
	if err != nil {
		t.Fatalf("Parse(%q, %q) unexpected error: %v", amount, currency, err)
	}
	return m
}

func TestMinorUnits_RoundTrip(t *testing.T) {
	original := mustParse(t, "1234.56", "BRL")
	reconstructed, err := FromMinorUnits(original.MinorUnits(), original.Currency())
	if err != nil {
		t.Fatalf("FromMinorUnits unexpected error: %v", err)
	}
	if reconstructed != original {
		t.Errorf("round trip = %+v, want %+v", reconstructed, original)
	}
	if original.MinorUnits() != 123456 {
		t.Errorf("MinorUnits() = %d, want 123456", original.MinorUnits())
	}
}

func TestParse_Valid(t *testing.T) {
	cases := []struct {
		amount   string
		currency string
		want     int64
	}{
		{"0.00", "BRL", 0},
		{"25.00", "BRL", 2500},
		{"1000.00", "BRL", 100000},
		{"0.01", "BRL", 1},
	}
	for _, c := range cases {
		m := mustParse(t, c.amount, c.currency)
		if m.minorUnits != c.want {
			t.Errorf("Parse(%q, %q).minorUnits = %d, want %d", c.amount, c.currency, m.minorUnits, c.want)
		}
		if m.Currency() != c.currency {
			t.Errorf("Parse(%q, %q).Currency() = %q, want %q", c.amount, c.currency, m.Currency(), c.currency)
		}
	}
}

func TestParse_InvalidFormat(t *testing.T) {
	invalid := []string{
		"",
		"NaN",
		"Infinity",
		"-Infinity",
		"1e10",
		"2.5e3",
		"25.000", // excess scale
		"25.0",   // under scale
		"25",     // no fractional part
		".25",    // no integer part
		"25.",    // no fractional digits
		"twenty",
		"25.0a",
		" 25.00",
		"25.00 ",
		"25,00",
		"+25.00",
	}
	for _, amount := range invalid {
		if _, err := Parse(amount, "BRL"); !errors.Is(err, ErrInvalidFormat) {
			t.Errorf("Parse(%q, BRL) error = %v, want ErrInvalidFormat", amount, err)
		}
	}
}

func TestParse_NegativeRejected(t *testing.T) {
	_, err := Parse("-25.00", "BRL")
	if !errors.Is(err, ErrNegativeAmount) {
		t.Fatalf("Parse(-25.00) error = %v, want ErrNegativeAmount", err)
	}
}

func TestParse_InvalidCurrency(t *testing.T) {
	invalid := []string{"", "brl", "BR", "BRLL", "123", "Brl"}
	for _, currency := range invalid {
		if _, err := Parse("25.00", currency); !errors.Is(err, ErrInvalidCurrency) {
			t.Errorf("Parse(25.00, %q) error = %v, want ErrInvalidCurrency", currency, err)
		}
	}
}

func TestParse_Overflow(t *testing.T) {
	// math.MaxInt64 = 9223372036854775807, so the whole-unit part alone
	// already overflows once multiplied by 100.
	_, err := Parse("92233720368547758080.00", "BRL")
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("Parse(huge amount) error = %v, want ErrOverflow", err)
	}
}

func TestZero(t *testing.T) {
	z, err := Zero("BRL")
	if err != nil {
		t.Fatalf("Zero(BRL) unexpected error: %v", err)
	}
	if !z.IsZero() {
		t.Errorf("Zero(BRL).IsZero() = false, want true")
	}
	if _, err := Zero("brl"); !errors.Is(err, ErrInvalidCurrency) {
		t.Errorf("Zero(brl) error = %v, want ErrInvalidCurrency", err)
	}
}

func TestAdd(t *testing.T) {
	a := mustParse(t, "25.00", "BRL")
	b := mustParse(t, "10.50", "BRL")

	sum, err := a.Add(b)
	if err != nil {
		t.Fatalf("Add unexpected error: %v", err)
	}
	if sum.String() != "35.50" {
		t.Errorf("Add = %s, want 35.50", sum.String())
	}
}

func TestAdd_CurrencyMismatch(t *testing.T) {
	a := mustParse(t, "25.00", "BRL")
	b := mustParse(t, "10.00", "USD")
	if _, err := a.Add(b); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Add(BRL, USD) error = %v, want ErrCurrencyMismatch", err)
	}
}

func TestAdd_Overflow(t *testing.T) {
	max, err := FromMinorUnits(math.MaxInt64, "BRL")
	if err != nil {
		t.Fatalf("FromMinorUnits unexpected error: %v", err)
	}
	one := mustParse(t, "0.01", "BRL")
	if _, err := max.Add(one); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Add at MaxInt64 error = %v, want ErrOverflow", err)
	}
}

func TestSub_CanGoNegative(t *testing.T) {
	a := mustParse(t, "10.00", "BRL")
	b := mustParse(t, "25.00", "BRL")

	diff, err := a.Sub(b)
	if err != nil {
		t.Fatalf("Sub unexpected error: %v", err)
	}
	if !diff.IsNegative() {
		t.Errorf("Sub result IsNegative() = false, want true")
	}
	if diff.String() != "-15.00" {
		t.Errorf("Sub = %s, want -15.00", diff.String())
	}
}

func TestSub_CurrencyMismatch(t *testing.T) {
	a := mustParse(t, "25.00", "BRL")
	b := mustParse(t, "10.00", "USD")
	if _, err := a.Sub(b); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Sub(BRL, USD) error = %v, want ErrCurrencyMismatch", err)
	}
}

func TestNegate(t *testing.T) {
	a := mustParse(t, "25.00", "BRL")
	neg, err := a.Negate()
	if err != nil {
		t.Fatalf("Negate unexpected error: %v", err)
	}
	if neg.String() != "-25.00" {
		t.Errorf("Negate = %s, want -25.00", neg.String())
	}
	back, err := neg.Negate()
	if err != nil {
		t.Fatalf("Negate unexpected error: %v", err)
	}
	if back.String() != "25.00" {
		t.Errorf("double Negate = %s, want 25.00", back.String())
	}
}

func TestNegate_Overflow(t *testing.T) {
	minVal, err := FromMinorUnits(math.MinInt64, "BRL")
	if err != nil {
		t.Fatalf("FromMinorUnits unexpected error: %v", err)
	}
	if _, err := minVal.Negate(); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Negate(MinInt64) error = %v, want ErrOverflow", err)
	}
}

func TestCompare(t *testing.T) {
	small := mustParse(t, "10.00", "BRL")
	big := mustParse(t, "25.00", "BRL")
	sameAsSmall := mustParse(t, "10.00", "BRL")

	if got, err := small.Compare(big); err != nil || got != -1 {
		t.Errorf("small.Compare(big) = (%d, %v), want (-1, nil)", got, err)
	}
	if got, err := big.Compare(small); err != nil || got != 1 {
		t.Errorf("big.Compare(small) = (%d, %v), want (1, nil)", got, err)
	}
	if got, err := small.Compare(sameAsSmall); err != nil || got != 0 {
		t.Errorf("small.Compare(sameAsSmall) = (%d, %v), want (0, nil)", got, err)
	}

	usd := mustParse(t, "10.00", "USD")
	if _, err := small.Compare(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Compare(BRL, USD) error = %v, want ErrCurrencyMismatch", err)
	}
}

func TestJSON_RoundTrip(t *testing.T) {
	original := mustParse(t, "25.00", "BRL")

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal unexpected error: %v", err)
	}
	if string(data) != `{"amount":"25.00","currency":"BRL"}` {
		t.Errorf("Marshal = %s, want {\"amount\":\"25.00\",\"currency\":\"BRL\"}", data)
	}

	var decoded Money
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal unexpected error: %v", err)
	}
	if decoded != original {
		t.Errorf("round trip = %+v, want %+v", decoded, original)
	}
}

func TestJSON_NegativeMarshalsButDoesNotUnmarshal(t *testing.T) {
	neg, err := FromMinorUnits(-500, "BRL")
	if err != nil {
		t.Fatalf("FromMinorUnits unexpected error: %v", err)
	}

	data, err := json.Marshal(neg)
	if err != nil {
		t.Fatalf("Marshal unexpected error: %v", err)
	}
	if string(data) != `{"amount":"-5.00","currency":"BRL"}` {
		t.Errorf("Marshal = %s, want {\"amount\":\"-5.00\",\"currency\":\"BRL\"}", data)
	}

	var decoded Money
	if err := json.Unmarshal(data, &decoded); !errors.Is(err, ErrNegativeAmount) {
		t.Errorf("Unmarshal(negative) error = %v, want ErrNegativeAmount", err)
	}
}

func TestJSON_RejectsNumericAmount(t *testing.T) {
	var decoded Money
	err := json.Unmarshal([]byte(`{"amount":25.00,"currency":"BRL"}`), &decoded)
	if err == nil {
		t.Fatal("Unmarshal(numeric amount) succeeded, want error")
	}
}

func TestPredicates(t *testing.T) {
	zero, _ := Zero("BRL")
	pos := mustParse(t, "1.00", "BRL")
	neg, _ := FromMinorUnits(-1, "BRL")

	if !zero.IsZero() || zero.IsPositive() || zero.IsNegative() {
		t.Errorf("zero predicates wrong: IsZero=%v IsPositive=%v IsNegative=%v", zero.IsZero(), zero.IsPositive(), zero.IsNegative())
	}
	if !pos.IsPositive() || pos.IsZero() || pos.IsNegative() {
		t.Errorf("positive predicates wrong")
	}
	if !neg.IsNegative() || neg.IsZero() || neg.IsPositive() {
		t.Errorf("negative predicates wrong")
	}
}
