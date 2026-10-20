// Package db opens the pgx pool, runs the embedded goose migrations and
// maintains the monthly audit partitions.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Querier is satisfied by *pgxpool.Pool, *pgxpool.Conn and pgx.Tx, so store
// helpers work both inside and outside transactions.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Open connects (retrying while Postgres starts), migrates and ensures the
// current and next audit partitions exist.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if cfg.MaxConns > 20 || cfg.MaxConns == 0 {
		cfg.MaxConns = 20
	}
	var pool *pgxpool.Pool
	deadline := time.Now().Add(60 * time.Second)
	for {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				break
			}
			pool.Close()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err := Migrate(pool); err != nil {
		pool.Close()
		return nil, err
	}
	if err := EnsurePartitions(ctx, pool, time.Now()); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func Migrate(pool *pgxpool.Pool) error {
	sqldb := stdlib.OpenDBFromPool(pool)
	defer sqldb.Close()
	return migrate(sqldb)
}

func migrate(sqldb *sql.DB) error {
	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.Up(sqldb, "migrations"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// EnsurePartitions creates the audit partitions for now's month and the next.
func EnsurePartitions(ctx context.Context, q Querier, now time.Time) error {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, m := range []time.Time{first, first.AddDate(0, 1, 0)} {
		if _, err := q.Exec(ctx, `SELECT ensure_audit_partition($1::date)`, m.Format("2006-01-02")); err != nil {
			return fmt.Errorf("ensure audit partition %s: %w", m.Format("2006-01"), err)
		}
	}
	return nil
}
