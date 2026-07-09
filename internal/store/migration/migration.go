// Package migration은 내장 스키마 마이그레이션 러너다.
//
// *.up.sql 파일을 embed해서 기동 시 순서대로 적용한다. 적용 이력은
// schema_migration 테이블에 기록. docker initdb 훅과 달리 k8s 등
// 어디서든 동작하고, 빈 DB든 기존 DB든 같은 경로로 수렴한다.
//
// 기존 DB 승계(baseline): schema_migration이 비어있는데 domain 테이블이
// 이미 존재하면 initdb 훅으로 만들어진 DB로 판단하고, 현재 알고 있는
// 마이그레이션 전부를 "이미 적용됨"으로 기록만 한다 (재실행 X).
// 이 시점(0001~0005)의 dev DB에만 해당하는 일회성 규칙이다.
package migration

import (
	"context"
	"embed"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.up.sql
var upFS embed.FS

// 동시 기동(레플리카/재시작 경합) 방지용 advisory lock 키.
const lockKey = 7231005 // 'mail' 스키마 락 (임의 고정값)

// Run은 미적용 마이그레이션을 순서대로 적용한다.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	nameList, err := versionList()
	if err != nil {
		return err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migration: 커넥션 획득: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("migration: advisory lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockKey)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migration (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("migration: 이력 테이블 생성: %w", err)
	}

	appliedMap, err := loadApplied(ctx, conn)
	if err != nil {
		return err
	}

	// baseline: 이력은 없는데 스키마는 있음 → initdb 훅 시절 DB 승계.
	// ★baseline 대상은 훅 시절에 존재했던 0001~0005로 고정 — 이후 추가된
	// 마이그레이션(0006+)까지 "적용됨" 처리하면 실제로는 스킵돼 버린다.
	if len(appliedMap) == 0 {
		var domainExists *string
		if err := conn.QueryRow(ctx, `SELECT to_regclass('public.domain')::text`).Scan(&domainExists); err != nil {
			return fmt.Errorf("migration: baseline 감지: %w", err)
		}
		if domainExists != nil {
			baselineList := []string{
				"0001_init", "0002_outbound_queue", "0003_dkim_key", "0004_alias", "0005_relay",
			}
			for _, name := range baselineList {
				if _, err := conn.Exec(ctx,
					`INSERT INTO schema_migration (version) VALUES ($1)`, name); err != nil {
					return fmt.Errorf("migration: baseline 기록(%s): %w", name, err)
				}
				appliedMap[name] = true
			}
			log.Printf("migration: 기존 스키마 감지 — %d개 baseline 기록", len(baselineList))
		}
	}

	for _, name := range nameList {
		if appliedMap[name] {
			continue
		}
		sqlBody, err := upFS.ReadFile(name + ".up.sql")
		if err != nil {
			return fmt.Errorf("migration: %s 읽기: %w", name, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migration: %s 트랜잭션: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBody)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration: %s 적용 실패: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migration (version) VALUES ($1)`, name); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration: %s 기록: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migration: %s 커밋: %w", name, err)
		}
		log.Printf("migration: %s 적용", name)
	}
	return nil
}

func versionList() ([]string, error) {
	entryList, err := upFS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("migration: embed 읽기: %w", err)
	}
	var nameList []string
	for _, e := range entryList {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			nameList = append(nameList, strings.TrimSuffix(e.Name(), ".up.sql"))
		}
	}
	sort.Strings(nameList)
	return nameList, nil
}

func loadApplied(ctx context.Context, conn *pgxpool.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migration`)
	if err != nil {
		return nil, fmt.Errorf("migration: 이력 조회: %w", err)
	}
	defer rows.Close()
	appliedMap := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		appliedMap[v] = true
	}
	return appliedMap, rows.Err()
}
