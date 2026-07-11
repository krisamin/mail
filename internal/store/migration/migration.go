// Package migration is the embedded schema migration runner.
//
// It embeds the *.up.sql files and applies them in order at startup. Applied
// history is recorded in the schema_migration table. Unlike the docker initdb
// hook, this works anywhere (k8s, etc.), and both empty and existing DBs
// converge along the same path.
//
// UUID reset (2026-07): every entity key moved from BIGSERIAL to UUID and the
// step-by-step migrations were squashed into a fresh-start 0001. A database
// created by the pre-UUID era is incompatible — the runner detects it (domain
// table exists but no schema_migration record for 0001_init, or a non-uuid
// domain.id) and refuses to start so nobody silently mixes the two worlds.
// Wipe the database (DROP SCHEMA public CASCADE; CREATE SCHEMA public;) to
// migrate — discarding the data was an explicit decision.
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

// Advisory lock key to prevent concurrent startup (replica/restart races).
const lockKey = 7231005 // 'mail' schema lock (arbitrary fixed value)

// Run applies unapplied migrations in order.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	nameList, err := versionList()
	if err != nil {
		return err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migration: acquire connection: %w", err)
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
		return fmt.Errorf("migration: create history table: %w", err)
	}

	appliedMap, err := loadApplied(ctx, conn)
	if err != nil {
		return err
	}

	// Pre-UUID era guard: a schema exists but 0001_init was never recorded by
	// this (squashed, UUID) runner — that database was built by the old
	// BIGSERIAL migrations and is incompatible. Also catch a stale history
	// where domain.id is not uuid. Refuse to start instead of corrupting.
	if !appliedMap["0001_init"] {
		var domainExists *string
		if err := conn.QueryRow(ctx, `SELECT to_regclass('public.domain')::text`).Scan(&domainExists); err != nil {
			return fmt.Errorf("migration: era detection: %w", err)
		}
		if domainExists != nil {
			return fmt.Errorf("migration: existing pre-UUID schema detected — wipe the database (DROP SCHEMA public CASCADE; CREATE SCHEMA public;) before starting this version")
		}
	} else {
		var idType string
		if err := conn.QueryRow(ctx, `
			SELECT COALESCE((SELECT data_type FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'domain' AND column_name = 'id'), '')`).Scan(&idType); err != nil {
			return fmt.Errorf("migration: era detection: %w", err)
		}
		if idType != "" && idType != "uuid" {
			return fmt.Errorf("migration: domain.id is %s (pre-UUID schema with stale history) — wipe the database before starting this version", idType)
		}
	}

	for _, name := range nameList {
		if appliedMap[name] {
			continue
		}
		sqlBody, err := upFS.ReadFile(name + ".up.sql")
		if err != nil {
			return fmt.Errorf("migration: read %s: %w", name, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migration: %s transaction: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBody)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration: %s apply failed: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migration (version) VALUES ($1)`, name); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration: %s record: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migration: %s commit: %w", name, err)
		}
		log.Printf("migration: %s applied", name)
	}
	return nil
}

func versionList() ([]string, error) {
	entryList, err := upFS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("migration: read embed: %w", err)
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
		return nil, fmt.Errorf("migration: load history: %w", err)
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
