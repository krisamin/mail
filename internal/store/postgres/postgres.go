// Package postgres는 store.Store의 PostgreSQL 구현체다.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/krisamin/mail/internal/store"
)

// Store는 pgxpool 기반 store.Store 구현체.
type Store struct {
	pool *pgxpool.Pool
}

// 컴파일 타임에 인터페이스 만족 여부 확인.
var _ store.Store = (*Store)(nil)

// New는 DSN으로 연결 풀을 만들고 Store를 반환한다.
// DSN 예: postgres://mail:maildev@localhost:55432/mail
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool 생성: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close는 연결 풀을 닫는다.
func (s *Store) Close() {
	s.pool.Close()
}

// Pool은 마이그레이션 등 저수준 접근이 필요할 때 노출.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}
