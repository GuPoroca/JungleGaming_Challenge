package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/money"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wallet"
)

var (
	// ErrWalletNotFound is returned when a lookup or update finds no
	// matching row.
	ErrWalletNotFound = errors.New("postgres: wallet not found")

	// ErrWalletAlreadyExists is returned by Create when a wallet already
	// exists for the given (playerId, currency) pair.
	ErrWalletAlreadyExists = errors.New("postgres: wallet already exists for this player and currency")
)

// uniqueViolation is Postgres's stable SQLSTATE code for a unique
// constraint violation (23505). Hardcoded rather than pulling in a
// dependency just for this one constant.
const uniqueViolation = "23505"

const selectWalletColumns = `id, player_id, currency, balance_minor_units, version, created_at, updated_at FROM wallets`

// WalletRepository reads and writes the wallets table. Every method takes
// a Querier so the caller controls the transaction boundary.
type WalletRepository struct{}

// NewWalletRepository constructs a WalletRepository. It holds no state —
// the pool or transaction is supplied per call.
func NewWalletRepository() *WalletRepository {
	return &WalletRepository{}
}

// Create inserts a new wallet row.
func (r *WalletRepository) Create(ctx context.Context, q Querier, w wallet.Wallet) error {
	_, err := q.Exec(ctx, `
		INSERT INTO wallets (id, player_id, currency, balance_minor_units, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		string(w.ID()), string(w.PlayerID()), w.Currency(), w.Balance().MinorUnits(), w.Version(), w.CreatedAt(), w.UpdatedAt(),
	)
	if isUniqueViolation(err) {
		return ErrWalletAlreadyExists
	}
	return err
}

// FindByID reads a wallet without locking it.
func (r *WalletRepository) FindByID(ctx context.Context, q Querier, id wallet.ID) (wallet.Wallet, error) {
	return scanWallet(q.QueryRow(ctx, `SELECT `+selectWalletColumns+` WHERE id = $1`, string(id)))
}

// FindByIDForUpdate reads a wallet and locks its row for the rest of tx
// (SELECT ... FOR UPDATE), so a concurrent Debit/Credit against the same
// wallet blocks until this transaction commits or rolls back instead of
// racing on the later Update. This is the pessimistic-locking strategy
// documented in ARCHITECTURE.md: different wallets are different rows,
// so they never contend, but two operations on the same wallet are
// forced to serialize here rather than needing an optimistic-CAS retry
// loop around the ledger/transaction writes that go with the update.
func (r *WalletRepository) FindByIDForUpdate(ctx context.Context, tx pgx.Tx, id wallet.ID) (wallet.Wallet, error) {
	return scanWallet(tx.QueryRow(ctx, `SELECT `+selectWalletColumns+` WHERE id = $1 FOR UPDATE`, string(id)))
}

// FindByPlayerAndCurrency reads the wallet identified by the
// (playerId, currency) pair that §6.2 makes unique.
func (r *WalletRepository) FindByPlayerAndCurrency(ctx context.Context, q Querier, playerID wallet.PlayerID, currency string) (wallet.Wallet, error) {
	return scanWallet(q.QueryRow(ctx, `SELECT `+selectWalletColumns+` WHERE player_id = $1 AND currency = $2`, string(playerID), currency))
}

// Update persists the balance and version resulting from a Debit or
// Credit. Call it with the same tx that obtained the row via
// FindByIDForUpdate, inside the SQL transaction that also writes the
// resulting ledger entry and the triggering WagerTransaction's state —
// the challenge requires all three to commit together.
func (r *WalletRepository) Update(ctx context.Context, tx pgx.Tx, w wallet.Wallet) error {
	tag, err := tx.Exec(ctx, `
		UPDATE wallets SET balance_minor_units = $1, version = $2, updated_at = $3
		WHERE id = $4`,
		w.Balance().MinorUnits(), w.Version(), w.UpdatedAt(), string(w.ID()),
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWalletNotFound
	}
	return nil
}

func scanWallet(row pgx.Row) (wallet.Wallet, error) {
	var (
		id, playerID, currency     string
		balanceMinorUnits, version int64
		createdAt, updatedAt       time.Time
	)
	if err := row.Scan(&id, &playerID, &currency, &balanceMinorUnits, &version, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return wallet.Wallet{}, ErrWalletNotFound
		}
		return wallet.Wallet{}, err
	}

	balance, err := money.FromMinorUnits(balanceMinorUnits, currency)
	if err != nil {
		return wallet.Wallet{}, err
	}
	return wallet.Rehydrate(wallet.ID(id), wallet.PlayerID(playerID), balance, version, createdAt, updatedAt)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
