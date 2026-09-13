package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/ledger"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/money"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wallet"
)

var (
	// ErrLedgerEntryNotFound is returned when a lookup finds no matching
	// row.
	ErrLedgerEntryNotFound = errors.New("postgres: ledger entry not found")

	// ErrLedgerEntryAlreadyExists is returned by Create when a
	// (walletId, transactionId) pair already has an entry — the schema's
	// UNIQUE constraint, which is also the last line of defense against
	// ever recording two movements for the same transaction.
	ErrLedgerEntryAlreadyExists = errors.New("postgres: ledger entry already exists for this wallet and transaction")
)

const selectLedgerColumns = `
	id, wallet_id, transaction_id, direction, amount_minor_units, currency,
	balance_before_minor_units, balance_after_minor_units, created_at
	FROM wallet_ledger_entries`

// LedgerCursor is an opaque-to-callers keyset pagination position: the
// (createdAt, id) of the last entry seen. The HTTP layer (not built yet)
// owns turning this into and out of the wire's opaque cursor string;
// this package only deals in the typed value the query actually needs.
type LedgerCursor struct {
	CreatedAt time.Time
	ID        ledger.ID
}

// LedgerRepository reads and writes the wallet_ledger_entries table. It
// exposes no Update or Delete: the ledger is append-only end to end, not
// just at the schema level — there is no method here that could violate
// that even if misused.
type LedgerRepository struct{}

// NewLedgerRepository constructs a LedgerRepository. It holds no state —
// the pool or transaction is supplied per call.
func NewLedgerRepository() *LedgerRepository {
	return &LedgerRepository{}
}

// Create inserts a new ledger entry. It classifies Postgres errors so
// callers never need to inspect a raw pgconn.PgError: a uniqueness
// violation on (walletId, transactionId) becomes
// ErrLedgerEntryAlreadyExists, and a foreign key violation becomes
// ErrWalletNotFound or ErrTransactionNotFound depending on which FK
// fired.
func (r *LedgerRepository) Create(ctx context.Context, q Querier, e ledger.Entry) error {
	_, err := q.Exec(ctx, `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_minor_units, currency,
			balance_before_minor_units, balance_after_minor_units, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		string(e.ID()), string(e.WalletID()), string(e.TransactionID()), string(e.Direction()),
		e.Amount().MinorUnits(), e.Amount().Currency(),
		e.BalanceBefore().MinorUnits(), e.BalanceAfter().MinorUnits(), e.CreatedAt(),
	)
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return ErrLedgerEntryAlreadyExists
	}
	if fk, ok := foreignKeyConstraint(err); ok {
		switch fk {
		case "wallet_ledger_entries_wallet_id_fkey":
			return ErrWalletNotFound
		case "wallet_ledger_entries_transaction_id_fkey":
			return ErrTransactionNotFound
		}
	}
	return err
}

// FindByWalletAndTransaction looks up the entry for one (walletId,
// transactionId) pair — the same natural key the schema's uniqueness
// constraint protects.
func (r *LedgerRepository) FindByWalletAndTransaction(ctx context.Context, q Querier, walletID wallet.ID, transactionID ledger.TransactionID) (ledger.Entry, error) {
	return scanLedgerEntry(q.QueryRow(ctx, `SELECT `+selectLedgerColumns+` WHERE wallet_id = $1 AND transaction_id = $2`, string(walletID), string(transactionID)))
}

// FindByWalletID lists a wallet's entries in stable (createdAt, id)
// order, oldest first, using keyset pagination: pass after as nil for
// the first page, then as the returned cursor for each subsequent page.
// A nil returned cursor means there is no next page. limit entries are
// returned per page (one extra row is fetched internally to detect
// whether more remain, then trimmed).
func (r *LedgerRepository) FindByWalletID(ctx context.Context, q Querier, walletID wallet.ID, after *LedgerCursor, limit int) ([]ledger.Entry, *LedgerCursor, error) {
	query := `SELECT ` + selectLedgerColumns + ` WHERE wallet_id = $1`
	args := []any{string(walletID)}
	if after != nil {
		query += fmt.Sprintf(` AND (created_at, id) > ($%d, $%d)`, len(args)+1, len(args)+2)
		args = append(args, after.CreatedAt, string(after.ID))
	}
	query += fmt.Sprintf(` ORDER BY created_at ASC, id ASC LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var entries []ledger.Entry
	for rows.Next() {
		entry, err := scanLedgerEntry(rows)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var next *LedgerCursor
	if len(entries) > limit {
		entries = entries[:limit]
		last := entries[limit-1]
		next = &LedgerCursor{CreatedAt: last.CreatedAt(), ID: last.ID()}
	}
	return entries, next, nil
}

// SumBalanceByWallet reconstructs a wallet's balance as credits minus
// debits over every recorded entry, for the reconciliation endpoint. It
// also returns the number of entries summed (the response's
// checkedEntries). currency is supplied by the caller (the wallet's own
// currency) rather than inferred, since a wallet with no entries yet
// (zero initial balance, no OPENING) has none to infer it from.
func (r *LedgerRepository) SumBalanceByWallet(ctx context.Context, q Querier, walletID wallet.ID, currency string) (money.Money, int, error) {
	var netMinorUnits int64
	var count int
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount_minor_units ELSE -amount_minor_units END), 0), COUNT(*)
		FROM wallet_ledger_entries WHERE wallet_id = $1`,
		string(walletID),
	).Scan(&netMinorUnits, &count)
	if err != nil {
		return money.Money{}, 0, err
	}

	balance, err := money.FromMinorUnits(netMinorUnits, currency)
	if err != nil {
		return money.Money{}, 0, err
	}
	return balance, count, nil
}

// rowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows
// (Query, scanned per-row in a Next() loop), so scanLedgerEntry works
// for both a single lookup and a page of results.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanLedgerEntry(row rowScanner) (ledger.Entry, error) {
	var (
		id, walletID, transactionID, direction, currency string
		amountMinorUnits                                 int64
		balanceBeforeMinorUnits, balanceAfterMinorUnits  int64
		createdAt                                        time.Time
	)
	if err := row.Scan(&id, &walletID, &transactionID, &direction, &amountMinorUnits, &currency, &balanceBeforeMinorUnits, &balanceAfterMinorUnits, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ledger.Entry{}, ErrLedgerEntryNotFound
		}
		return ledger.Entry{}, err
	}

	amount, err := money.FromMinorUnits(amountMinorUnits, currency)
	if err != nil {
		return ledger.Entry{}, err
	}
	balanceBefore, err := money.FromMinorUnits(balanceBeforeMinorUnits, currency)
	if err != nil {
		return ledger.Entry{}, err
	}
	balanceAfter, err := money.FromMinorUnits(balanceAfterMinorUnits, currency)
	if err != nil {
		return ledger.Entry{}, err
	}

	return ledger.Rehydrate(
		ledger.ID(id), wallet.ID(walletID), ledger.TransactionID(transactionID),
		wallet.Direction(direction), amount, balanceBefore, balanceAfter, createdAt,
	)
}

func foreignKeyConstraint(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
		return pgErr.ConstraintName, true
	}
	return "", false
}
