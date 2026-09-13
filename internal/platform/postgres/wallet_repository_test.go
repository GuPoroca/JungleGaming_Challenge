//go:build integration

// These tests run against a real PostgreSQL instance — no mocks — per
// the challenge's requirement that integration tests exercise real
// infrastructure. Bring one up and apply the schema first:
//
//	docker compose up -d postgres
//	docker compose --profile tools run --rm migrate up
//	go test -tags=integration ./internal/platform/postgres/...
//
// DATABASE_URL overrides the default, which matches docker-compose.yml.
package postgres_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/money"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wallet"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/platform/postgres"
)

func testDatabaseURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://app:app@localhost:5432/jungle_gaming?sslmode=disable"
}

func setupPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, testDatabaseURL())
	if err != nil {
		t.Fatalf("NewPool unexpected error: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, "TRUNCATE TABLE wallet_ledger_entries, wager_transactions, wallets CASCADE"); err != nil {
		t.Fatalf("truncate fixture tables: %v", err)
	}
	return pool
}

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("money.Parse(%q, %q) unexpected error: %v", amount, currency, err)
	}
	return m
}

func TestWalletRepository_CreateAndFindByID(t *testing.T) {
	pool := setupPool(t)
	repo := postgres.NewWalletRepository()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	balance := mustMoney(t, "1000.00", "BRL")
	w, err := wallet.New("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", balance, now)
	if err != nil {
		t.Fatalf("wallet.New unexpected error: %v", err)
	}

	if err := repo.Create(ctx, pool, w); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, pool, w.ID())
	if err != nil {
		t.Fatalf("FindByID unexpected error: %v", err)
	}
	if found.ID() != w.ID() || found.PlayerID() != w.PlayerID() {
		t.Errorf("FindByID identity = (%q, %q), want (%q, %q)", found.ID(), found.PlayerID(), w.ID(), w.PlayerID())
	}
	if found.Balance() != w.Balance() {
		t.Errorf("FindByID balance = %v, want %v", found.Balance(), w.Balance())
	}
	if found.Version() != 1 {
		t.Errorf("FindByID version = %d, want 1", found.Version())
	}
	if !found.CreatedAt().Equal(now) || !found.UpdatedAt().Equal(now) {
		t.Errorf("FindByID timestamps = (%v, %v), want (%v, %v)", found.CreatedAt(), found.UpdatedAt(), now, now)
	}
}

func TestWalletRepository_FindByID_NotFound(t *testing.T) {
	pool := setupPool(t)
	repo := postgres.NewWalletRepository()

	_, err := repo.FindByID(context.Background(), pool, "99999999-9999-9999-9999-999999999999")
	if !errors.Is(err, postgres.ErrWalletNotFound) {
		t.Fatalf("FindByID(missing) error = %v, want ErrWalletNotFound", err)
	}
}

func TestWalletRepository_Create_DuplicatePlayerCurrencyRejected(t *testing.T) {
	pool := setupPool(t)
	repo := postgres.NewWalletRepository()
	ctx := context.Background()
	now := time.Now().UTC()
	balance := mustMoney(t, "10.00", "BRL")

	first, err := wallet.New("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", balance, now)
	if err != nil {
		t.Fatalf("wallet.New unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, first); err != nil {
		t.Fatalf("Create(first) unexpected error: %v", err)
	}

	second, err := wallet.New("33333333-3333-3333-3333-333333333333", "22222222-2222-2222-2222-222222222222", balance, now)
	if err != nil {
		t.Fatalf("wallet.New unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, second); !errors.Is(err, postgres.ErrWalletAlreadyExists) {
		t.Fatalf("Create(duplicate player+currency) error = %v, want ErrWalletAlreadyExists", err)
	}
}

func TestWalletRepository_FindByPlayerAndCurrency(t *testing.T) {
	pool := setupPool(t)
	repo := postgres.NewWalletRepository()
	ctx := context.Background()
	now := time.Now().UTC()
	balance := mustMoney(t, "50.00", "BRL")

	w, err := wallet.New("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", balance, now)
	if err != nil {
		t.Fatalf("wallet.New unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, w); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	found, err := repo.FindByPlayerAndCurrency(ctx, pool, "22222222-2222-2222-2222-222222222222", "BRL")
	if err != nil {
		t.Fatalf("FindByPlayerAndCurrency unexpected error: %v", err)
	}
	if found.ID() != w.ID() {
		t.Errorf("FindByPlayerAndCurrency id = %q, want %q", found.ID(), w.ID())
	}
}

func TestWalletRepository_Update(t *testing.T) {
	pool := setupPool(t)
	repo := postgres.NewWalletRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	w, err := wallet.New("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", mustMoney(t, "100.00", "BRL"), now)
	if err != nil {
		t.Fatalf("wallet.New unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, w); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	later := now.Add(time.Minute)
	debited, _, err := w.Debit(mustMoney(t, "25.00", "BRL"), later)
	if err != nil {
		t.Fatalf("Debit unexpected error: %v", err)
	}

	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		locked, err := repo.FindByIDForUpdate(ctx, tx, w.ID())
		if err != nil {
			return err
		}
		if locked.Balance() != w.Balance() {
			t.Fatalf("locked balance = %v, want %v", locked.Balance(), w.Balance())
		}
		return repo.Update(ctx, tx, debited)
	})
	if err != nil {
		t.Fatalf("WithTx unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, pool, w.ID())
	if err != nil {
		t.Fatalf("FindByID unexpected error: %v", err)
	}
	if found.Balance().String() != "75.00" {
		t.Errorf("Balance() = %s, want 75.00", found.Balance().String())
	}
	if found.Version() != 2 {
		t.Errorf("Version() = %d, want 2", found.Version())
	}
}

func TestWalletRepository_Update_NotFound(t *testing.T) {
	pool := setupPool(t)
	repo := postgres.NewWalletRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	ghost, err := wallet.New("99999999-9999-9999-9999-999999999999", "22222222-2222-2222-2222-222222222222", mustMoney(t, "10.00", "BRL"), now)
	if err != nil {
		t.Fatalf("wallet.New unexpected error: %v", err)
	}

	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return repo.Update(ctx, tx, ghost)
	})
	if !errors.Is(err, postgres.ErrWalletNotFound) {
		t.Fatalf("Update(missing wallet) error = %v, want ErrWalletNotFound", err)
	}
}

// TestWalletRepository_ConcurrentDebits_PessimisticLock is the mandatory
// scenario from §8: a wallet holding 100.00 BRL receives two concurrent
// 80.00 BRL debits. Exactly one must succeed, the other must be rejected
// for insufficient balance, and the final balance must be exactly 20.00
// with a single balance change recorded (version advances by 1, not 2).
func TestWalletRepository_ConcurrentDebits_PessimisticLock(t *testing.T) {
	pool := setupPool(t)
	repo := postgres.NewWalletRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	w, err := wallet.New("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", mustMoney(t, "100.00", "BRL"), now)
	if err != nil {
		t.Fatalf("wallet.New unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, w); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	debit := mustMoney(t, "80.00", "BRL")

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
				current, err := repo.FindByIDForUpdate(ctx, tx, w.ID())
				if err != nil {
					return err
				}
				next, _, err := current.Debit(debit, time.Now().UTC())
				if err != nil {
					return err
				}
				return repo.Update(ctx, tx, next)
			})
		}()
	}
	wg.Wait()
	close(results)

	var succeeded, rejectedForInsufficientBalance int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, wallet.ErrInsufficientBalance):
			rejectedForInsufficientBalance++
		default:
			t.Fatalf("unexpected error from concurrent debit: %v", err)
		}
	}
	if succeeded != 1 {
		t.Errorf("succeeded = %d, want 1", succeeded)
	}
	if rejectedForInsufficientBalance != 1 {
		t.Errorf("rejectedForInsufficientBalance = %d, want 1", rejectedForInsufficientBalance)
	}

	final, err := repo.FindByID(ctx, pool, w.ID())
	if err != nil {
		t.Fatalf("FindByID unexpected error: %v", err)
	}
	if final.Balance().String() != "20.00" {
		t.Errorf("final balance = %s, want 20.00", final.Balance().String())
	}
	if final.Version() != 2 {
		t.Errorf("final version = %d, want 2 (exactly one successful debit)", final.Version())
	}
}
