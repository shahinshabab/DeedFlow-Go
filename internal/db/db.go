// Package db wraps the pgx connection pool, applies the embedded SQL
// migrations and offers the few Postgres features the app leans on instead of
// Redis: advisory locks and LISTEN/NOTIFY.
package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shahinshabab/deedflow-go/internal/migrations"
)

// Querier is satisfied by both the pool and a transaction, so store code
// works inside or outside one.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// DB is the application's database handle.
type DB struct {
	Pool *pgxpool.Pool
	url  string
}

// ErrNotFound is returned by lookups that match no row.
var ErrNotFound = errors.New("not found")

// Connect opens the pool and waits (up to a minute) for Postgres to accept
// connections, like the Django entrypoint's wait-for loop.
func Connect(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 20
	var pool *pgxpool.Pool
	deadline := time.Now().Add(time.Minute)
	for {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				break
			}
			pool.Close()
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("connecting to postgres: %w", err)
		}
		slog.Info("waiting for postgres", "err", err)
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &DB{Pool: pool, url: url}, nil
}

// Close releases the pool.
func (d *DB) Close() { d.Pool.Close() }

// Tx runs fn in a transaction, committing if it returns nil.
func (d *DB) Tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// Migrate applies every embedded migration not yet recorded. 0001_baseline.sql
// is the Django project's schema; it is skipped (but recorded) when the
// database already has those tables, i.e. when adopting a Django database.
func (d *DB) Migrate(ctx context.Context) error {
	// Two processes starting together must not both migrate (nor both create
	// the bookkeeping table: CREATE TABLE IF NOT EXISTS races too).
	conn, err := d.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(8472001)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(8472001)`)
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS deedflow_migrations (
		name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var applied bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deedflow_migrations WHERE name = $1)`, name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return err
		}
		skip := false
		if name == "0001_baseline.sql" {
			if err := conn.QueryRow(ctx, `SELECT to_regclass('auth_user') IS NOT NULL`).Scan(&skip); err != nil {
				return err
			}
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if !skip {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("migration %s: %w", name, err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deedflow_migrations (name) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		slog.Info("applied migration", "name", name, "skipped_existing_schema", skip)
	}
	return nil
}

// Listener holds a dedicated connection that LISTENs on a channel.
type Listener struct {
	conn *pgx.Conn
}

// Listen opens a dedicated connection listening on channel.
func (d *DB) Listen(ctx context.Context, channel string) (*Listener, error) {
	conn, err := pgx.Connect(ctx, d.url)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		conn.Close(ctx)
		return nil, err
	}
	return &Listener{conn: conn}, nil
}

// Wait blocks for the next notification payload.
func (l *Listener) Wait(ctx context.Context) (string, error) {
	n, err := l.conn.WaitForNotification(ctx)
	if err != nil {
		return "", err
	}
	return n.Payload, nil
}

// Close drops the connection.
func (l *Listener) Close() { l.conn.Close(context.Background()) }

// Notify sends payload on channel (Postgres limits it to 8000 bytes).
func (d *DB) Notify(ctx context.Context, channel, payload string) error {
	_, err := d.Pool.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, payload)
	return err
}

// AdvisoryLock is a session-level Postgres advisory lock held on its own
// connection until Release - the replacement for the Redis locks.
type AdvisoryLock struct {
	conn *pgxpool.Conn
	key  int64
}

// TryLock takes the lock without waiting; ok is false if someone holds it.
func (d *DB) TryLock(ctx context.Context, key int64) (*AdvisoryLock, bool, error) {
	conn, err := d.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !got {
		conn.Release()
		return nil, false, nil
	}
	return &AdvisoryLock{conn: conn, key: key}, true, nil
}

// Release frees the lock. Calling it twice is harmless.
func (l *AdvisoryLock) Release() {
	if l == nil || l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, l.key); err != nil {
		// A connection in an unknown state must not return to the pool still holding the lock.
		l.conn.Conn().Close(context.Background())
	}
	l.conn.Release()
	l.conn = nil
}

// IsLocked reports whether some session currently holds the advisory lock
// (used to tell a live submission from a dead one).
func (d *DB) IsLocked(ctx context.Context, key int64) (bool, error) {
	var locked bool
	err := d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory'
		AND ((classid::bigint << 32) | objid::bigint) = $1 AND granted)`, key).Scan(&locked)
	return locked, err
}
