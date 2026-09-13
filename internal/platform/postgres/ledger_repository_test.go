//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/ledger"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wagertransaction"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wallet"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/platform/postgres"
)

// seedProcessedBet creates a wager_transactions row (satisfying the
// ledger's FK) with the given id and amount, already PROCESSED, and
// returns it.
func seedProcessedBet(t *testing.T, pool *pgxpool.Pool, walletID wallet.ID, txID wagertransaction.ID, extID wagertransaction.ExternalTransactionID, amount string, now time.Time) wagertransaction.WagerTransaction {
	t.Helper()
	repo := postgres.NewWagerTransactionRepository()
	ctx := context.Background()

	pending, err := wagertransaction.NewExternal(wagertransaction.ExternalInput{
		ID:                    txID,
		ProviderID:            "provider-a",
		ExternalTransactionID: extID,
		IdempotencyKey:        wagertransaction.IdempotencyKey("provider-a:" + string(extID)),
		PayloadHash:           "hash",
		WalletID:              walletID,
		PlayerID:              "22222222-2222-2222-2222-222222222222",
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  wagertransaction.Bet,
		Money:                 mustMoney(t, amount, "BRL"),
	}, now)
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, pending); err != nil {
		t.Fatalf("Create(wager transaction) unexpected error: %v", err)
	}
	processed, err := pending.MarkProcessed(mustMoney(t, "1000.00", "BRL"), "", now)
	if err != nil {
		t.Fatalf("MarkProcessed unexpected error: %v", err)
	}
	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error { return repo.Update(ctx, tx, processed) })
	if err != nil {
		t.Fatalf("Update(processed) unexpected error: %v", err)
	}
	return processed
}

func TestLedgerRepository_CreateFromRealDebit_AndFindByWalletAndTransaction(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	betTx := seedProcessedBet(t, pool, walletID, "44444444-4444-4444-4444-444444444444", "ext-1", "25.00", now)

	w, err := wallet.Rehydrate(walletID, "22222222-2222-2222-2222-222222222222", mustMoney(t, "1000.00", "BRL"), 1, now, now)
	if err != nil {
		t.Fatalf("wallet.Rehydrate unexpected error: %v", err)
	}
	_, movement, err := w.Debit(mustMoney(t, "25.00", "BRL"), now)
	if err != nil {
		t.Fatalf("Debit unexpected error: %v", err)
	}

	entry, err := ledger.New("55555555-5555-5555-5555-555555555555", walletID, ledger.TransactionID(betTx.ID()), movement, now)
	if err != nil {
		t.Fatalf("ledger.New unexpected error: %v", err)
	}

	repo := postgres.NewLedgerRepository()
	if err := repo.Create(ctx, pool, entry); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	found, err := repo.FindByWalletAndTransaction(ctx, pool, walletID, ledger.TransactionID(betTx.ID()))
	if err != nil {
		t.Fatalf("FindByWalletAndTransaction unexpected error: %v", err)
	}
	if found.Direction() != wallet.Debit {
		t.Errorf("Direction() = %q, want DEBIT", found.Direction())
	}
	if found.BalanceBefore().String() != "1000.00" || found.BalanceAfter().String() != "975.00" {
		t.Errorf("balances = (%s, %s), want (1000.00, 975.00)", found.BalanceBefore().String(), found.BalanceAfter().String())
	}
}

func TestLedgerRepository_Create_DuplicateWalletTransactionRejected(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	betTx := seedProcessedBet(t, pool, walletID, "44444444-4444-4444-4444-444444444444", "ext-1", "25.00", now)

	movement := wallet.Movement{
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "25.00", "BRL"),
		BalanceBefore: mustMoney(t, "1000.00", "BRL"),
		BalanceAfter:  mustMoney(t, "975.00", "BRL"),
	}
	first, err := ledger.New("55555555-5555-5555-5555-555555555555", walletID, ledger.TransactionID(betTx.ID()), movement, now)
	if err != nil {
		t.Fatalf("ledger.New unexpected error: %v", err)
	}
	repo := postgres.NewLedgerRepository()
	if err := repo.Create(ctx, pool, first); err != nil {
		t.Fatalf("Create(first) unexpected error: %v", err)
	}

	second, err := ledger.New("66666666-6666-6666-6666-666666666666", walletID, ledger.TransactionID(betTx.ID()), movement, now)
	if err != nil {
		t.Fatalf("ledger.New unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, second); !errors.Is(err, postgres.ErrLedgerEntryAlreadyExists) {
		t.Fatalf("Create(duplicate wallet+transaction) error = %v, want ErrLedgerEntryAlreadyExists", err)
	}
}

func TestLedgerRepository_Create_UnknownWalletRejected(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	betTx := seedProcessedBet(t, pool, walletID, "44444444-4444-4444-4444-444444444444", "ext-1", "25.00", now)

	movement := wallet.Movement{
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "25.00", "BRL"),
		BalanceBefore: mustMoney(t, "1000.00", "BRL"),
		BalanceAfter:  mustMoney(t, "975.00", "BRL"),
	}
	entry, err := ledger.New("55555555-5555-5555-5555-555555555555", "99999999-9999-9999-9999-999999999999", ledger.TransactionID(betTx.ID()), movement, now)
	if err != nil {
		t.Fatalf("ledger.New unexpected error: %v", err)
	}
	repo := postgres.NewLedgerRepository()
	if err := repo.Create(ctx, pool, entry); !errors.Is(err, postgres.ErrWalletNotFound) {
		t.Fatalf("Create(unknown wallet) error = %v, want ErrWalletNotFound", err)
	}
}

func TestLedgerRepository_Create_UnknownTransactionRejected(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")

	movement := wallet.Movement{
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "25.00", "BRL"),
		BalanceBefore: mustMoney(t, "1000.00", "BRL"),
		BalanceAfter:  mustMoney(t, "975.00", "BRL"),
	}
	entry, err := ledger.New("55555555-5555-5555-5555-555555555555", walletID, "99999999-9999-9999-9999-999999999999", movement, now)
	if err != nil {
		t.Fatalf("ledger.New unexpected error: %v", err)
	}
	repo := postgres.NewLedgerRepository()
	if err := repo.Create(ctx, pool, entry); !errors.Is(err, postgres.ErrTransactionNotFound) {
		t.Fatalf("Create(unknown transaction) error = %v, want ErrTransactionNotFound", err)
	}
}

func TestLedgerRepository_AppendOnly_RawUpdateAndDeleteRejected(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	betTx := seedProcessedBet(t, pool, walletID, "44444444-4444-4444-4444-444444444444", "ext-1", "25.00", now)

	movement := wallet.Movement{
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "25.00", "BRL"),
		BalanceBefore: mustMoney(t, "1000.00", "BRL"),
		BalanceAfter:  mustMoney(t, "975.00", "BRL"),
	}
	entry, err := ledger.New("55555555-5555-5555-5555-555555555555", walletID, ledger.TransactionID(betTx.ID()), movement, now)
	if err != nil {
		t.Fatalf("ledger.New unexpected error: %v", err)
	}
	if err := postgres.NewLedgerRepository().Create(ctx, pool, entry); err != nil {
		t.Fatalf("Create unexpected error: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE wallet_ledger_entries SET amount_minor_units = 1 WHERE id = $1`, string(entry.ID())); err == nil {
		t.Error("raw UPDATE on wallet_ledger_entries succeeded, want the append-only trigger to reject it")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM wallet_ledger_entries WHERE id = $1`, string(entry.ID())); err == nil {
		t.Error("raw DELETE on wallet_ledger_entries succeeded, want the append-only trigger to reject it")
	}
}

func TestLedgerRepository_FindByWalletID_Pagination(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	base := time.Now().UTC()

	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewLedgerRepository()

	ids := []struct {
		txID  wagertransaction.ID
		extID wagertransaction.ExternalTransactionID
		entry ledger.ID
	}{
		{"44444444-4444-4444-4444-444444444401", "ext-1", "55555555-5555-5555-5555-555555555501"},
		{"44444444-4444-4444-4444-444444444402", "ext-2", "55555555-5555-5555-5555-555555555502"},
		{"44444444-4444-4444-4444-444444444403", "ext-3", "55555555-5555-5555-5555-555555555503"},
	}
	for i, row := range ids {
		createdAt := base.Add(time.Duration(i) * time.Second)
		betTx := seedProcessedBet(t, pool, walletID, row.txID, row.extID, "10.00", createdAt)
		movement := wallet.Movement{
			Direction:     wallet.Debit,
			Amount:        mustMoney(t, "10.00", "BRL"),
			BalanceBefore: mustMoney(t, "1000.00", "BRL"),
			BalanceAfter:  mustMoney(t, "990.00", "BRL"),
		}
		entry, err := ledger.New(row.entry, walletID, ledger.TransactionID(betTx.ID()), movement, createdAt)
		if err != nil {
			t.Fatalf("ledger.New unexpected error: %v", err)
		}
		if err := repo.Create(ctx, pool, entry); err != nil {
			t.Fatalf("Create(%d) unexpected error: %v", i, err)
		}
	}

	firstPage, cursor, err := repo.FindByWalletID(ctx, pool, walletID, nil, 2)
	if err != nil {
		t.Fatalf("FindByWalletID(page 1) unexpected error: %v", err)
	}
	if len(firstPage) != 2 {
		t.Fatalf("len(firstPage) = %d, want 2", len(firstPage))
	}
	if firstPage[0].ID() != ids[0].entry || firstPage[1].ID() != ids[1].entry {
		t.Errorf("firstPage order = (%q, %q), want (%q, %q)", firstPage[0].ID(), firstPage[1].ID(), ids[0].entry, ids[1].entry)
	}
	if cursor == nil {
		t.Fatal("cursor after page 1 is nil, want a cursor for page 2")
	}

	secondPage, cursor2, err := repo.FindByWalletID(ctx, pool, walletID, cursor, 2)
	if err != nil {
		t.Fatalf("FindByWalletID(page 2) unexpected error: %v", err)
	}
	if len(secondPage) != 1 {
		t.Fatalf("len(secondPage) = %d, want 1", len(secondPage))
	}
	if secondPage[0].ID() != ids[2].entry {
		t.Errorf("secondPage[0].ID() = %q, want %q", secondPage[0].ID(), ids[2].entry)
	}
	if cursor2 != nil {
		t.Errorf("cursor after final page = %+v, want nil", cursor2)
	}
}

func TestLedgerRepository_SumBalanceByWallet(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	walletID := seedWallet(t, pool, "11111111-1111-1111-1111-111111111111")
	repo := postgres.NewLedgerRepository()

	zero, count, err := repo.SumBalanceByWallet(ctx, pool, walletID, "BRL")
	if err != nil {
		t.Fatalf("SumBalanceByWallet(no entries) unexpected error: %v", err)
	}
	if !zero.IsZero() || count != 0 {
		t.Errorf("SumBalanceByWallet(no entries) = (%s, %d), want (0.00, 0)", zero.String(), count)
	}

	betTx := seedProcessedBet(t, pool, walletID, "44444444-4444-4444-4444-444444444444", "ext-1", "40.00", now)
	debit := wallet.Movement{
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "40.00", "BRL"),
		BalanceBefore: mustMoney(t, "1000.00", "BRL"),
		BalanceAfter:  mustMoney(t, "960.00", "BRL"),
	}
	entry, err := ledger.New("55555555-5555-5555-5555-555555555555", walletID, ledger.TransactionID(betTx.ID()), debit, now)
	if err != nil {
		t.Fatalf("ledger.New(debit) unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, entry); err != nil {
		t.Fatalf("Create(debit) unexpected error: %v", err)
	}

	winTx := seedProcessedBet(t, pool, walletID, "77777777-7777-7777-7777-777777777777", "ext-2", "15.00", now)
	credit := wallet.Movement{
		Direction:     wallet.Credit,
		Amount:        mustMoney(t, "15.00", "BRL"),
		BalanceBefore: mustMoney(t, "960.00", "BRL"),
		BalanceAfter:  mustMoney(t, "975.00", "BRL"),
	}
	creditEntry, err := ledger.New("88888888-8888-8888-8888-888888888888", walletID, ledger.TransactionID(winTx.ID()), credit, now)
	if err != nil {
		t.Fatalf("ledger.New(credit) unexpected error: %v", err)
	}
	if err := repo.Create(ctx, pool, creditEntry); err != nil {
		t.Fatalf("Create(credit) unexpected error: %v", err)
	}

	sum, count, err := repo.SumBalanceByWallet(ctx, pool, walletID, "BRL")
	if err != nil {
		t.Fatalf("SumBalanceByWallet unexpected error: %v", err)
	}
	if sum.String() != "-25.00" {
		t.Errorf("SumBalanceByWallet = %s, want -25.00 (a -40.00 debit plus a +15.00 credit)", sum.String())
	}
	if count != 2 {
		t.Errorf("checkedEntries = %d, want 2", count)
	}
}
