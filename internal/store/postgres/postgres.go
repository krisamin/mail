// Package postgres is the PostgreSQL implementation of store.Store.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/krisamin/mail/internal/store"
)

// Store is the pgxpool-based store.Store implementation.
type Store struct {
	pool *pgxpool.Pool
}

// Compile-time check that the interface is satisfied.
var _ store.Store = (*Store)(nil)

// New creates a connection pool from the DSN and returns a Store.
// DSN example: postgres://mail:maildev@localhost:55432/mail
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool create: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close closes the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Pool is exposed for low-level access such as migrations.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}
