// Package money implements Money, an immutable value object combining an
// exact monetary amount with an ISO 4217 currency code. It has no
// dependency on any other package in this module, HTTP, or persistence
// libraries, so it can be reused unchanged by the wallet, ledger, and
// wager transaction domain types.
//
// Representation: amounts are stored as int64 minor units (e.g. cents for
// BRL) at a fixed scale of two decimal digits. Money never passes through
// float32 or float64 during parsing, arithmetic, or formatting. The usable
// range is the full int64 domain: roughly ±92.23 quadrillion minor units,
// i.e. ±922.33 trillion units of the major currency at scale 2. Arithmetic
// that would exceed this range returns ErrOverflow rather than wrapping.
//
// Currency validation checks only the ISO 4217 alphabetic shape (three
// uppercase letters); it does not check the code against the real ISO 4217
// list, which is out of scope for this package.
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

var (
	// ErrInvalidFormat is returned when an amount string does not match
	// the required decimal shape: optional sign, one or more integer
	// digits, a dot, and exactly two fractional digits. This covers
	// empty input, NaN, Infinity, scientific notation, and any scale
	// other than two digits.
	ErrInvalidFormat = errors.New("money: invalid amount format")

	// ErrNegativeAmount is returned by Parse when the input amount is a
	// well-formed negative decimal. Parse is used for external
	// financial inputs, which must never be negative; negative Money
	// values can still be produced internally via Sub or Negate.
	ErrNegativeAmount = errors.New("money: negative amount not allowed")

	// ErrInvalidCurrency is returned when a currency code is not exactly
	// three uppercase ASCII letters.
	ErrInvalidCurrency = errors.New("money: invalid currency code")

	// ErrCurrencyMismatch is returned by arithmetic and comparison
	// methods when both operands do not share the same currency.
	ErrCurrencyMismatch = errors.New("money: currency mismatch")

	// ErrOverflow is returned when parsing or arithmetic would exceed
	// the int64 minor-unit range.
	ErrOverflow = errors.New("money: amount overflow")
)

// decimalPattern accepts an optional leading '-', one or more integer
// digits, a dot, and exactly two fractional digits. Any other shape
// (empty, NaN, Infinity, scientific notation, wrong scale) is rejected
// outright rather than normalized, so no equivalent-form documentation is
// owed to the idempotency hash.
var decimalPattern = regexp.MustCompile(`^-?[0-9]+\.[0-9]{2}$`)

// currencyPattern accepts the ISO 4217 alphabetic shape only.
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Money is an immutable amount in a single currency, backed by int64 minor
// units. The zero value is not a valid Money; use Zero, Parse, or
// FromMinorUnits to construct one.
type Money struct {
	minorUnits int64
	currency   string
}

// Zero returns the zero amount for the given currency.
func Zero(currency string) (Money, error) {
	if !currencyPattern.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, currency)
	}
	return Money{minorUnits: 0, currency: currency}, nil
}

// FromMinorUnits reconstructs a Money from its persisted minor-unit
// representation (e.g. a BIGINT column), preserving the exact value. It
// validates only the currency shape; callers rehydrating from a trusted
// store are not re-running external input validation.
func FromMinorUnits(minorUnits int64, currency string) (Money, error) {
	if !currencyPattern.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, currency)
	}
	return Money{minorUnits: minorUnits, currency: currency}, nil
}

// Parse builds a Money from the external decimal contract, e.g. "25.00".
// It rejects empty input, NaN, Infinity, scientific notation, any scale
// other than two fractional digits, and negative amounts, matching the
// challenge's rules for external financial inputs. It never rounds an
// out-of-shape input.
func Parse(amount, currency string) (Money, error) {
	if !currencyPattern.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, currency)
	}
	if !decimalPattern.MatchString(amount) {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidFormat, amount)
	}
	if amount[0] == '-' {
		return Money{}, fmt.Errorf("%w: %q", ErrNegativeAmount, amount)
	}

	dot := len(amount) - 3 // fractional part is fixed at 2 digits + '.'
	whole, err := strconv.ParseInt(amount[:dot], 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}
	frac, err := strconv.ParseInt(amount[dot+1:], 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidFormat, amount)
	}

	minorFromWhole, ok := mulOverflow(whole, 100)
	if !ok {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}
	total, ok := addOverflow(minorFromWhole, frac)
	if !ok {
		return Money{}, fmt.Errorf("%w: %q", ErrOverflow, amount)
	}

	return Money{minorUnits: total, currency: currency}, nil
}

// Add returns m + other. Both operands must share the same currency.
func (m Money) Add(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	sum, ok := addOverflow(m.minorUnits, other.minorUnits)
	if !ok {
		return Money{}, fmt.Errorf("%w: %s + %s", ErrOverflow, m.String(), other.String())
	}
	return Money{minorUnits: sum, currency: m.currency}, nil
}

// Sub returns m - other. Both operands must share the same currency. The
// result may be negative; callers enforcing a non-negativity invariant
// (e.g. a wallet balance) must check that separately.
func (m Money) Sub(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	negated, err := other.Negate()
	if err != nil {
		return Money{}, err
	}
	return m.Add(negated)
}

// Negate returns -m.
func (m Money) Negate() (Money, error) {
	if m.minorUnits == math.MinInt64 {
		return Money{}, fmt.Errorf("%w: negating %s", ErrOverflow, m.String())
	}
	return Money{minorUnits: -m.minorUnits, currency: m.currency}, nil
}

// Compare returns -1, 0, or 1 as m is less than, equal to, or greater than
// other. Both operands must share the same currency.
func (m Money) Compare(other Money) (int, error) {
	if m.currency != other.currency {
		return 0, fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	switch {
	case m.minorUnits < other.minorUnits:
		return -1, nil
	case m.minorUnits > other.minorUnits:
		return 1, nil
	default:
		return 0, nil
	}
}

// IsZero reports whether m is exactly zero.
func (m Money) IsZero() bool { return m.minorUnits == 0 }

// IsNegative reports whether m is less than zero.
func (m Money) IsNegative() bool { return m.minorUnits < 0 }

// IsPositive reports whether m is greater than zero.
func (m Money) IsPositive() bool { return m.minorUnits > 0 }

// Currency returns the ISO 4217 currency code.
func (m Money) Currency() string { return m.currency }

// MinorUnits returns the raw int64 minor-unit value, for persisting it
// exactly (e.g. into a BIGINT column) and reconstructing it later via
// FromMinorUnits. It is the inverse of FromMinorUnits.
func (m Money) MinorUnits() int64 { return m.minorUnits }

// String formats the amount at a fixed scale of two decimal digits, e.g.
// "25.00" or "-5.00". It does not include the currency code.
func (m Money) String() string {
	u := uint64(m.minorUnits)
	negative := m.IsNegative()
	if negative {
		u = -u
	}
	whole, frac := u/100, u%100
	if negative {
		return fmt.Sprintf("-%d.%02d", whole, frac)
	}
	return fmt.Sprintf("%d.%02d", whole, frac)
}

// wireFormat is the external JSON contract: {"amount":"25.00","currency":"BRL"}.
type wireFormat struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// MarshalJSON encodes m using the external decimal-string contract.
func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireFormat{Amount: m.String(), Currency: m.currency})
}

// UnmarshalJSON decodes the external decimal-string contract via Parse,
// so every rejection rule in Parse (format, scale, sign) applies uniformly
// wherever Money is read from an HTTP body or SQS message.
func (m *Money) UnmarshalJSON(data []byte) error {
	var w wireFormat
	if err := json.Unmarshal(data, &w); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFormat, err)
	}
	parsed, err := Parse(w.Amount, w.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

func mulOverflow(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	result := a * b
	if result/b != a {
		return 0, false
	}
	return result, true
}

func addOverflow(a, b int64) (int64, bool) {
	result := a + b
	if (b > 0 && result < a) || (b < 0 && result > a) {
		return 0, false
	}
	return result, true
}
