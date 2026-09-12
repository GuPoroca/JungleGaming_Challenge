package ledger

import (
	"errors"
	"testing"
	"time"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/money"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wallet"
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

func TestNew_FromRealWalletDebit(t *testing.T) {
	balance := mustMoney(t, "100.00", "BRL")
	w, err := wallet.New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("wallet.New unexpected error: %v", err)
	}

	_, movement, err := w.Debit(mustMoney(t, "25.00", "BRL"), fixedNow)
	if err != nil {
		t.Fatalf("Debit unexpected error: %v", err)
	}

	entry, err := New("entry-1", w.ID(), "tx-1", movement, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}
	if entry.Direction() != wallet.Debit {
		t.Errorf("Direction = %q, want DEBIT", entry.Direction())
	}
	if entry.BalanceBefore().String() != "100.00" || entry.BalanceAfter().String() != "75.00" {
		t.Errorf("balances = (%s, %s), want (100.00, 75.00)", entry.BalanceBefore().String(), entry.BalanceAfter().String())
	}
}

func TestNew_FromRealWalletCredit(t *testing.T) {
	balance := mustMoney(t, "100.00", "BRL")
	w, err := wallet.New("wallet-1", "player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("wallet.New unexpected error: %v", err)
	}

	_, movement, err := w.Credit(mustMoney(t, "25.00", "BRL"), fixedNow)
	if err != nil {
		t.Fatalf("Credit unexpected error: %v", err)
	}

	entry, err := New("entry-1", w.ID(), "tx-1", movement, fixedNow)
	if err != nil {
		t.Fatalf("New unexpected error: %v", err)
	}
	if entry.Direction() != wallet.Credit {
		t.Errorf("Direction = %q, want CREDIT", entry.Direction())
	}
	if entry.BalanceBefore().String() != "100.00" || entry.BalanceAfter().String() != "125.00" {
		t.Errorf("balances = (%s, %s), want (100.00, 125.00)", entry.BalanceBefore().String(), entry.BalanceAfter().String())
	}
}

func TestNew_BalanceMismatch(t *testing.T) {
	movement := wallet.Movement{
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "25.00", "BRL"),
		BalanceBefore: mustMoney(t, "100.00", "BRL"),
		BalanceAfter:  mustMoney(t, "80.00", "BRL"), // should be 75.00
	}
	if _, err := New("entry-1", "wallet-1", "tx-1", movement, fixedNow); !errors.Is(err, ErrBalanceMismatch) {
		t.Fatalf("New(mismatched balances) error = %v, want ErrBalanceMismatch", err)
	}
}

func TestNew_InvalidDirection(t *testing.T) {
	movement := wallet.Movement{
		Direction:     "REFUND", // not DEBIT or CREDIT
		Amount:        mustMoney(t, "25.00", "BRL"),
		BalanceBefore: mustMoney(t, "100.00", "BRL"),
		BalanceAfter:  mustMoney(t, "75.00", "BRL"),
	}
	if _, err := New("entry-1", "wallet-1", "tx-1", movement, fixedNow); !errors.Is(err, ErrInvalidDirection) {
		t.Fatalf("New(invalid direction) error = %v, want ErrInvalidDirection", err)
	}
}

func TestNew_InvalidAmount(t *testing.T) {
	zero, _ := money.Zero("BRL")
	movement := wallet.Movement{
		Direction:     wallet.Debit,
		Amount:        zero,
		BalanceBefore: mustMoney(t, "100.00", "BRL"),
		BalanceAfter:  mustMoney(t, "100.00", "BRL"),
	}
	if _, err := New("entry-1", "wallet-1", "tx-1", movement, fixedNow); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("New(zero amount) error = %v, want ErrInvalidAmount", err)
	}
}

func TestNew_CurrencyMismatch(t *testing.T) {
	movement := wallet.Movement{
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "25.00", "USD"),
		BalanceBefore: mustMoney(t, "100.00", "BRL"),
		BalanceAfter:  mustMoney(t, "75.00", "BRL"),
	}
	if _, err := New("entry-1", "wallet-1", "tx-1", movement, fixedNow); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("New(currency mismatch) error = %v, want money.ErrCurrencyMismatch", err)
	}
}

func TestNew_InvalidIdentifiers(t *testing.T) {
	movement := wallet.Movement{
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "25.00", "BRL"),
		BalanceBefore: mustMoney(t, "100.00", "BRL"),
		BalanceAfter:  mustMoney(t, "75.00", "BRL"),
	}
	if _, err := New("", "wallet-1", "tx-1", movement, fixedNow); !errors.Is(err, ErrInvalidID) {
		t.Errorf("New(empty id) error = %v, want ErrInvalidID", err)
	}
	if _, err := New("entry-1", "", "tx-1", movement, fixedNow); !errors.Is(err, ErrInvalidWalletID) {
		t.Errorf("New(empty walletId) error = %v, want ErrInvalidWalletID", err)
	}
	if _, err := New("entry-1", "wallet-1", "", movement, fixedNow); !errors.Is(err, ErrInvalidTransactionID) {
		t.Errorf("New(empty transactionId) error = %v, want ErrInvalidTransactionID", err)
	}
}

func TestRehydrate_Valid(t *testing.T) {
	entry, err := Rehydrate(
		"entry-1", "wallet-1", "tx-1", wallet.Debit,
		mustMoney(t, "25.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "75.00", "BRL"),
		fixedNow,
	)
	if err != nil {
		t.Fatalf("Rehydrate unexpected error: %v", err)
	}
	if entry.ID() != "entry-1" || entry.WalletID() != "wallet-1" || entry.TransactionID() != "tx-1" {
		t.Errorf("Rehydrate identifiers = (%q, %q, %q)", entry.ID(), entry.WalletID(), entry.TransactionID())
	}
	if entry.CreatedAt() != fixedNow {
		t.Errorf("Rehydrate createdAt = %v, want %v", entry.CreatedAt(), fixedNow)
	}
}

func TestRehydrate_BalanceMismatch(t *testing.T) {
	_, err := Rehydrate(
		"entry-1", "wallet-1", "tx-1", wallet.Credit,
		mustMoney(t, "25.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "100.00", "BRL"), // should be 125.00 for a credit
		fixedNow,
	)
	if !errors.Is(err, ErrBalanceMismatch) {
		t.Fatalf("Rehydrate(mismatched balances) error = %v, want ErrBalanceMismatch", err)
	}
}
