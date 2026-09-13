package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx. Repository
// methods accept a Querier instead of a concrete type so the caller
// controls the transaction boundary: pass the pool for a single
// statement, or an active pgx.Tx to make several repository calls commit
// or roll back together.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = (pgx.Tx)(nil)
)

// WithTx runs fn inside a transaction obtained from pool, committing on a
// nil return and rolling back otherwise. It's the caller's job to decide
// which repository calls belong inside one fn — that decision is what
// sets the challenge's required SQL transaction boundary (e.g. a wallet
// balance update, its ledger entry, and its transaction's terminal state
// must all commit together).
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
