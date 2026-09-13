//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wagertransaction"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wallet"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/platform/postgres"
)

// seedWallet creates a wallet row so wager_transactions' FK on wallet_id
// is satisfied, and returns its id.
func seedWallet(t *testing.T, pool *pgxpool.Pool, id wallet.ID) wallet.ID {
	t.Helper()
	w, err := wallet.New(id, "22222222-2222-2222-2222-222222222222", mustMoney(t, "1000.00", "BRL"), time.Now().UTC())
	if err != nil {
		t.Fatalf("wallet.New unexpected error: %v", err)
	}
	if err := postgres.NewWalletRepository().Create(context.Background(), pool, w); err != nil {
		t.Fatalf("seed wallet Create unexpected error: %v", err)
	}
	return w.ID()
}

func validBetInput(t *testing.T, walletID wallet.ID) wagertransaction.ExternalInput {
	t.Helper()
	return wagertransaction.ExternalInput{
		ID:                    "44444444-4444-4444-4444-444444444444",
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-1",
		IdempotencyKey:        "provider-a:ext-1",
		PayloadHash:           "hash-1",
		WalletID:              walletID,
		PlayerID:              "22222222-2222-2222-2222-222222222222",
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  wagertransaction.Bet,
		Money:                 mustMoney(t, "25.00", "BRL"),
	}
}

func TestWagerTransactionRepository_CreateAndFindByID(t *testing.T) {
	pool := setupPool(t)
	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	input := validBetInput(t, walletID)
	input.Money = mustMoney(t, "25.00", "BRL")
	tx, err := wagertransaction.NewExternal(input, now)
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}

	if err := repo.Create(ctx, pool, tx); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, pool, tx.ID())
	if err != nil {
		t.Fatalf("FindByID unexpected error: %v", err)
	}
	if found.Kind() != wagertransaction.Bet || found.State() != wagertransaction.Pending {
		t.Errorf("FindByID kind/state = (%q, %q), want (BET, PENDING)", found.Kind(), found.State())
	}
	if found.ProviderID() != tx.ProviderID() || found.ExternalTransactionID() != tx.ExternalTransactionID() {
		t.Errorf("FindByID provider/external id mismatch: got (%q, %q)", found.ProviderID(), found.ExternalTransactionID())
	}
	if found.Money() != tx.Money() {
		t.Errorf("FindByID money = %v, want %v", found.Money(), tx.Money())
	}
	if _, ok := found.ResultingBalance(); ok {
		t.Errorf("FindByID ResultingBalance ok = true for a fresh PENDING row")
	}
}

func TestWagerTransactionRepository_CreateOpening(t *testing.T) {
	pool := setupPool(t)
	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	tx, err := wagertransaction.NewOpening(wagertransaction.OpeningInput{
		ID:       "55555555-5555-5555-5555-555555555555",
		WalletID: walletID,
		PlayerID: "22222222-2222-2222-2222-222222222222",
		Money:    mustMoney(t, "1000.00", "BRL"),
	}, now)
	if err != nil {
		t.Fatalf("NewOpening unexpected error: %v", err)
	}

	if err := repo.Create(ctx, pool, tx); err != nil {
		t.Fatalf("Create(OPENING) unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, pool, tx.ID())
	if err != nil {
		t.Fatalf("FindByID unexpected error: %v", err)
	}
	if found.Kind() != wagertransaction.Opening {
		t.Errorf("Kind() = %q, want OPENING", found.Kind())
	}
	if found.ProviderID() != "" || found.ExternalTransactionID() != "" || found.RoundID() != "" || found.GameID() != "" {
		t.Errorf("OPENING round-tripped with external metadata set: %+v", found)
	}
}

func TestWagerTransactionRepository_Create_DuplicateExternalIDRejected(t *testing.T) {
	pool := setupPool(t)
	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	first, err := wagertransaction.NewExternal(validBetInput(t, walletID), now)
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, first); err != nil {
		t.Fatalf("Create(first) unexpected error: %v", err)
	}

	dup := validBetInput(t, walletID)
	dup.ID = "66666666-6666-6666-6666-666666666666"
	dup.IdempotencyKey = "provider-a:different-key" // different key, same (provider, external id)
	second, err := wagertransaction.NewExternal(dup, now)
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, second); !errors.Is(err, postgres.ErrTransactionAlreadyExists) {
		t.Fatalf("Create(duplicate external id) error = %v, want ErrTransactionAlreadyExists", err)
	}
}

func TestWagerTransactionRepository_Create_SecondOpeningForSameWalletRejected(t *testing.T) {
	pool := setupPool(t)
	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	first, err := wagertransaction.NewOpening(wagertransaction.OpeningInput{
		ID: "55555555-5555-5555-5555-555555555555", WalletID: walletID, PlayerID: "22222222-2222-2222-2222-222222222222", Money: mustMoney(t, "1000.00", "BRL"),
	}, now)
	if err != nil {
		t.Fatalf("NewOpening unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, first); err != nil {
		t.Fatalf("Create(first OPENING) unexpected error: %v", err)
	}

	second, err := wagertransaction.NewOpening(wagertransaction.OpeningInput{
		ID: "77777777-7777-7777-7777-777777777777", WalletID: walletID, PlayerID: "22222222-2222-2222-2222-222222222222", Money: mustMoney(t, "1.00", "BRL"),
	}, now)
	if err != nil {
		t.Fatalf("NewOpening unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, second); !errors.Is(err, postgres.ErrTransactionAlreadyExists) {
		t.Fatalf("Create(second OPENING, same wallet) error = %v, want ErrTransactionAlreadyExists", err)
	}
}

func TestWagerTransactionRepository_Create_UnknownWalletRejected(t *testing.T) {
	pool := setupPool(t)
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()

	tx, err := wagertransaction.NewExternal(validBetInput(t, "99999999-9999-9999-9999-999999999999"), time.Now().UTC())
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, tx); !errors.Is(err, postgres.ErrWalletNotFound) {
		t.Fatalf("Create(unknown wallet) error = %v, want ErrWalletNotFound", err)
	}
}

func TestWagerTransactionRepository_FindByProviderAndExternalID(t *testing.T) {
	pool := setupPool(t)
	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()

	tx, err := wagertransaction.NewExternal(validBetInput(t, walletID), time.Now().UTC())
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, tx); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	found, err := repo.FindByProviderAndExternalID(ctx, pool, tx.ProviderID(), tx.ExternalTransactionID())
	if err != nil {
		t.Fatalf("FindByProviderAndExternalID unexpected error: %v", err)
	}
	if found.ID() != tx.ID() {
		t.Errorf("FindByProviderAndExternalID id = %q, want %q", found.ID(), tx.ID())
	}

	found, err = repo.FindByProviderAndIdempotencyKey(ctx, pool, tx.ProviderID(), tx.IdempotencyKey())
	if err != nil {
		t.Fatalf("FindByProviderAndIdempotencyKey unexpected error: %v", err)
	}
	if found.ID() != tx.ID() {
		t.Errorf("FindByProviderAndIdempotencyKey id = %q, want %q", found.ID(), tx.ID())
	}
}

func TestWagerTransactionRepository_UpdateToProcessed(t *testing.T) {
	pool := setupPool(t)
	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	pending, err := wagertransaction.NewExternal(validBetInput(t, walletID), now)
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, pending); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	later := now.Add(time.Minute)
	processed, err := pending.MarkProcessed(mustMoney(t, "975.00", "BRL"), "", later)
	if err != nil {
		t.Fatalf("MarkProcessed unexpected error: %v", err)
	}

	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		locked, err := repo.FindByIDForUpdate(ctx, tx, pending.ID())
		if err != nil {
			return err
		}
		if locked.State() != wagertransaction.Pending {
			t.Fatalf("locked state = %q, want PENDING", locked.State())
		}
		return repo.Update(ctx, tx, processed)
	})
	if err != nil {
		t.Fatalf("WithTx unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, pool, pending.ID())
	if err != nil {
		t.Fatalf("FindByID unexpected error: %v", err)
	}
	if found.State() != wagertransaction.Processed {
		t.Errorf("State() = %q, want PROCESSED", found.State())
	}
	balance, ok := found.ResultingBalance()
	if !ok || balance.String() != "975.00" {
		t.Errorf("ResultingBalance() = (%s, %v), want (975.00, true)", balance.String(), ok)
	}
}

func TestWagerTransactionRepository_UpdateToRejected(t *testing.T) {
	pool := setupPool(t)
	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	pending, err := wagertransaction.NewExternal(validBetInput(t, walletID), now)
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, pending); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	rejected, err := pending.MarkRejected(wagertransaction.FailureCodeInsufficientBalanceForBet, now)
	if err != nil {
		t.Fatalf("MarkRejected unexpected error: %v", err)
	}

	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return repo.Update(ctx, tx, rejected)
	})
	if err != nil {
		t.Fatalf("WithTx unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, pool, pending.ID())
	if err != nil {
		t.Fatalf("FindByID unexpected error: %v", err)
	}
	if found.State() != wagertransaction.Rejected {
		t.Errorf("State() = %q, want REJECTED", found.State())
	}
	if found.FailureCode() != wagertransaction.FailureCodeInsufficientBalanceForBet {
		t.Errorf("FailureCode() = %q, want %q", found.FailureCode(), wagertransaction.FailureCodeInsufficientBalanceForBet)
	}
}

func TestWagerTransactionRepository_Update_TerminalRowRejectedByTrigger(t *testing.T) {
	pool := setupPool(t)
	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	pending, err := wagertransaction.NewExternal(validBetInput(t, walletID), now)
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, pending); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	processed, err := pending.MarkProcessed(mustMoney(t, "975.00", "BRL"), "", now)
	if err != nil {
		t.Fatalf("MarkProcessed unexpected error: %v", err)
	}
	if err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error { return repo.Update(ctx, tx, processed) }); err != nil {
		t.Fatalf("first Update unexpected error: %v", err)
	}

	// A second write against the same now-terminal row — simulating a
	// bug or a stale in-memory value — must be rejected by the database
	// itself, not just by the Go-level state machine.
	rejectedAttempt, err := pending.MarkRejected(wagertransaction.FailureCodeInsufficientBalanceForBet, now)
	if err != nil {
		t.Fatalf("MarkRejected unexpected error: %v", err)
	}
	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error { return repo.Update(ctx, tx, rejectedAttempt) })
	if err == nil {
		t.Fatal("Update on a terminal row succeeded, want the schema's trigger to reject it")
	}
}

func TestWagerTransactionRepository_Update_NotFound(t *testing.T) {
	pool := setupPool(t)
	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	ghost, err := wagertransaction.NewExternal(validBetInput(t, walletID), now)
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	// Never Create()d.

	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return repo.Update(ctx, tx, ghost)
	})
	if !errors.Is(err, postgres.ErrTransactionNotFound) {
		t.Fatalf("Update(never created) error = %v, want ErrTransactionNotFound", err)
	}
}

func TestWagerTransactionRepository_RefundRoundTripsResolvedReference(t *testing.T) {
	pool := setupPool(t)
	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()
	now := time.Now().UTC()

	bet, err := wagertransaction.NewExternal(validBetInput(t, walletID), now)
	if err != nil {
		t.Fatalf("NewExternal(BET) unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, bet); err != nil {
		t.Fatalf("Create(BET) unexpected error: %v", err)
	}

	refundInput := wagertransaction.ExternalInput{
		ID:                             "88888888-8888-8888-8888-888888888888",
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "ext-refund-1",
		IdempotencyKey:                 "provider-a:ext-refund-1",
		PayloadHash:                    "hash-refund-1",
		WalletID:                       walletID,
		PlayerID:                       "22222222-2222-2222-2222-222222222222",
		RoundID:                        "round-1",
		GameID:                         "game-1",
		Kind:                           wagertransaction.Refund,
		Money:                          mustMoney(t, "25.00", "BRL"),
		ReferenceExternalTransactionID: "ext-1",
	}
	refund, err := wagertransaction.NewExternal(refundInput, now)
	if err != nil {
		t.Fatalf("NewExternal(REFUND) unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, refund); err != nil {
		t.Fatalf("Create(REFUND) unexpected error: %v", err)
	}

	processedRefund, err := refund.MarkProcessed(mustMoney(t, "1000.00", "BRL"), bet.ID(), now)
	if err != nil {
		t.Fatalf("MarkProcessed unexpected error: %v", err)
	}
	if err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error { return repo.Update(ctx, tx, processedRefund) }); err != nil {
		t.Fatalf("Update unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, pool, refund.ID())
	if err != nil {
		t.Fatalf("FindByID unexpected error: %v", err)
	}
	if found.ResolvedReferenceID() != bet.ID() {
		t.Errorf("ResolvedReferenceID() = %q, want %q", found.ResolvedReferenceID(), bet.ID())
	}
	if found.ReferenceExternalTransactionID() != "ext-1" {
		t.Errorf("ReferenceExternalTransactionID() = %q, want ext-1", found.ReferenceExternalTransactionID())
	}
}
