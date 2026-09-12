package wallet

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/money"
)

var fixedNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("money.Parse(%q, %q) unexpected error: %v", amount, currency, err)
	}
	return m
}

func TestNew_Valid(t *testing.T) {
	balance := mustMoney(t, "1000.00", "BRL")
	w, err := New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}
	if w.ID() != "wallet-1" || w.PlayerID() != "player-1" {
		t.Errorf("New identity = (%q, %q), want (wallet-1, player-1)", w.ID(), w.PlayerID())
	}
	if w.Balance() != balance {
		t.Errorf("New balance = %v, want %v", w.Balance(), balance)
	}
	if w.Version() != 1 {
		t.Errorf("New version = %d, want 1", w.Version())
	}
	if w.CreatedAt() != fixedNow || w.UpdatedAt() != fixedNow {
		t.Errorf("New timestamps = (%v, %v), want (%v, %v)", w.CreatedAt(), w.UpdatedAt(), fixedNow, fixedNow)
	}
	if w.Currency() != "BRL" {
		t.Errorf("New currency = %q, want BRL", w.Currency())
	}
}

func TestNew_ZeroBalanceAllowed(t *testing.T) {
	zero, _ := money.Zero("BRL")
	w, err := New("wallet-1", "player-1", zero, fixedNow)
	if err != nil {
		t.Fatalf("New(zero balance) unexpected error: %v", err)
	}
	if w.Version() != 1 {
		t.Errorf("New(zero balance) version = %d, want 1", w.Version())
	}
}

func TestNew_InvalidInputs(t *testing.T) {
	balance := mustMoney(t, "10.00", "BRL")
	negative, err := money.FromMinorUnits(-1, "BRL")
	if err != nil {
		t.Fatalf("FromMinorUnits unexpected error: %v", err)
	}

	if _, err := New("", "player-1", balance, fixedNow); !errors.Is(err, ErrInvalidID) {
		t.Errorf("New(empty id) error = %v, want ErrInvalidID", err)
	}
	if _, err := New("wallet-1", "", balance, fixedNow); !errors.Is(err, ErrInvalidPlayerID) {
		t.Errorf("New(empty playerId) error = %v, want ErrInvalidPlayerID", err)
	}
	if _, err := New("wallet-1", "player-1", negative, fixedNow); !errors.Is(err, ErrInvalidBalance) {
		t.Errorf("New(negative balance) error = %v, want ErrInvalidBalance", err)
	}
}

func TestRehydrate_Valid(t *testing.T) {
	balance := mustMoney(t, "500.00", "BRL")
	created := fixedNow
	updated := fixedNow.Add(time.Hour)

	w, err := Rehydrate("wallet-1", "player-1", balance, 7, created, updated)
	if err != nil {
		t.Fatalf("Rehydrate unexpected error: %v", err)
	}
	if w.Version() != 7 {
		t.Errorf("Rehydrate version = %d, want 7", w.Version())
	}
	if w.CreatedAt() != created || w.UpdatedAt() != updated {
		t.Errorf("Rehydrate timestamps = (%v, %v), want (%v, %v)", w.CreatedAt(), w.UpdatedAt(), created, updated)
	}
	if w.Balance() != balance {
		t.Errorf("Rehydrate balance = %v, want %v", w.Balance(), balance)
	}
}

func TestRehydrate_InvalidInputs(t *testing.T) {
	balance := mustMoney(t, "10.00", "BRL")
	negative, _ := money.FromMinorUnits(-1, "BRL")

	if _, err := Rehydrate("", "player-1", balance, 1, fixedNow, fixedNow); !errors.Is(err, ErrInvalidID) {
		t.Errorf("Rehydrate(empty id) error = %v, want ErrInvalidID", err)
	}
	if _, err := Rehydrate("wallet-1", "", balance, 1, fixedNow, fixedNow); !errors.Is(err, ErrInvalidPlayerID) {
		t.Errorf("Rehydrate(empty playerId) error = %v, want ErrInvalidPlayerID", err)
	}
	if _, err := Rehydrate("wallet-1", "player-1", negative, 1, fixedNow, fixedNow); !errors.Is(err, ErrInvalidBalance) {
		t.Errorf("Rehydrate(negative balance) error = %v, want ErrInvalidBalance", err)
	}
	if _, err := Rehydrate("wallet-1", "player-1", balance, 0, fixedNow, fixedNow); !errors.Is(err, ErrInvalidVersion) {
		t.Errorf("Rehydrate(version 0) error = %v, want ErrInvalidVersion", err)
	}
}

func TestDebit_Success(t *testing.T) {
	balance := mustMoney(t, "100.00", "BRL")
	w, err := New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}

	amount := mustMoney(t, "25.00", "BRL")
	later := fixedNow.Add(time.Minute)
	next, movement, err := w.Debit(amount, later)
	if err != nil {
		t.Fatalf("Debit unexpected error: %v", err)
	}

	if next.Balance().String() != "75.00" {
		t.Errorf("Debit balance = %s, want 75.00", next.Balance().String())
	}
	if next.Version() != 2 {
		t.Errorf("Debit version = %d, want 2", next.Version())
	}
	if next.UpdatedAt() != later {
		t.Errorf("Debit updatedAt = %v, want %v", next.UpdatedAt(), later)
	}
	if movement.Direction != Debit {
		t.Errorf("Debit movement direction = %q, want DEBIT", movement.Direction)
	}
	if movement.Amount != amount || movement.BalanceBefore != balance || movement.BalanceAfter != next.Balance() {
		t.Errorf("Debit movement = %+v, mismatched amount/before/after", movement)
	}

	// Original wallet must be unaffected (value semantics / immutability).
	if w.Balance().String() != "100.00" || w.Version() != 1 {
		t.Errorf("original wallet mutated: balance=%s version=%d", w.Balance().String(), w.Version())
	}
}

func TestDebit_ExactBalanceAllowed(t *testing.T) {
	balance := mustMoney(t, "25.00", "BRL")
	w, err := New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}

	next, _, err := w.Debit(balance, fixedNow)
	if err != nil {
		t.Fatalf("Debit(exact balance) unexpected error: %v", err)
	}
	if !next.Balance().IsZero() {
		t.Errorf("Debit(exact balance) result = %s, want 0.00", next.Balance().String())
	}
}

func TestDebit_InsufficientBalance(t *testing.T) {
	balance := mustMoney(t, "100.00", "BRL")
	w, err := New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}

	amount := mustMoney(t, "150.00", "BRL")
	_, _, err = w.Debit(amount, fixedNow)
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("Debit(over balance) error = %v, want ErrInsufficientBalance", err)
	}

	// Original wallet must be unaffected by the failed operation.
	if w.Balance().String() != "100.00" || w.Version() != 1 {
		t.Errorf("original wallet mutated after failed debit: balance=%s version=%d", w.Balance().String(), w.Version())
	}
}

func TestDebit_InvalidAmount(t *testing.T) {
	balance := mustMoney(t, "100.00", "BRL")
	w, err := New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}

	zero, _ := money.Zero("BRL")
	if _, _, err := w.Debit(zero, fixedNow); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("Debit(zero) error = %v, want ErrInvalidAmount", err)
	}

	negative, _ := money.FromMinorUnits(-100, "BRL")
	if _, _, err := w.Debit(negative, fixedNow); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("Debit(negative) error = %v, want ErrInvalidAmount", err)
	}
}

func TestDebit_CurrencyMismatch(t *testing.T) {
	balance := mustMoney(t, "100.00", "BRL")
	w, err := New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}

	usd := mustMoney(t, "10.00", "USD")
	if _, _, err := w.Debit(usd, fixedNow); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Debit(USD amount on BRL wallet) error = %v, want money.ErrCurrencyMismatch", err)
	}
}

func TestCredit_Success(t *testing.T) {
	balance := mustMoney(t, "100.00", "BRL")
	w, err := New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}

	amount := mustMoney(t, "25.00", "BRL")
	later := fixedNow.Add(time.Minute)
	next, movement, err := w.Credit(amount, later)
	if err != nil {
		t.Fatalf("Credit unexpected error: %v", err)
	}

	if next.Balance().String() != "125.00" {
		t.Errorf("Credit balance = %s, want 125.00", next.Balance().String())
	}
	if next.Version() != 2 {
		t.Errorf("Credit version = %d, want 2", next.Version())
	}
	if movement.Direction != Credit {
		t.Errorf("Credit movement direction = %q, want CREDIT", movement.Direction)
	}
	if movement.BalanceBefore != balance || movement.BalanceAfter != next.Balance() {
		t.Errorf("Credit movement before/after mismatch: %+v", movement)
	}
}

func TestCredit_InvalidAmount(t *testing.T) {
	balance := mustMoney(t, "100.00", "BRL")
	w, err := New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}

	zero, _ := money.Zero("BRL")
	if _, _, err := w.Credit(zero, fixedNow); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("Credit(zero) error = %v, want ErrInvalidAmount", err)
	}
}

func TestCredit_CurrencyMismatch(t *testing.T) {
	balance := mustMoney(t, "100.00", "BRL")
	w, err := New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}

	usd := mustMoney(t, "10.00", "USD")
	if _, _, err := w.Credit(usd, fixedNow); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Credit(USD amount on BRL wallet) error = %v, want money.ErrCurrencyMismatch", err)
	}
}

func TestCredit_Overflow(t *testing.T) {
	maxBalance, err := money.FromMinorUnits(math.MaxInt64, "BRL")
	if err != nil {
		t.Fatalf("FromMinorUnits unexpected error: %v", err)
	}
	w, err := New("wallet-1", "player-1", maxBalance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}

	one := mustMoney(t, "0.01", "BRL")
	if _, _, err := w.Credit(one, fixedNow); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("Credit(overflow) error = %v, want money.ErrOverflow", err)
	}
}

func TestVersionIncrementsOnlyOnSuccess(t *testing.T) {
	balance := mustMoney(t, "10.00", "BRL")
	w, err := New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}

	tooMuch := mustMoney(t, "20.00", "BRL")
	if _, _, err := w.Debit(tooMuch, fixedNow); err == nil {
		t.Fatal("expected Debit to fail")
	}
	if w.Version() != 1 {
		t.Errorf("version after failed debit = %d, want 1", w.Version())
	}

	next, _, err := w.Debit(mustMoney(t, "5.00", "BRL"), fixedNow)
	if err != nil {
		t.Fatalf("Debit unexpected error: %v", err)
	}
	if next.Version() != 2 {
		t.Errorf("version after successful debit = %d, want 2", next.Version())
	}
}
