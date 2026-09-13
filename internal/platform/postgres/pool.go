// Package postgres provides the pgx-backed connection pool and repository
// implementations over the schema in migrations/. Transaction boundaries
// are explicit: a repository method takes a Querier (either the pool
// directly, for a single statement, or an active pgx.Tx, for several
// statements that must commit or roll back together), and it is always
// the caller — eventually the use-case layer — that decides which one to
// pass, never the repository itself.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a connection pool against databaseURL and verifies it can
// actually reach the database (Ping) before returning, so a bad
// connection string fails at startup rather than on the first query.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse config: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}
