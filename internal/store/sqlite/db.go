// Package sqlite implements the store.Store interface on top of an embedded
// SQLite database (modernc.org/sqlite), which is pure Go and therefore
// cross-compiles cleanly for linux/amd64 and linux/arm64 without cgo.
//
// All data operations are expressed against a narrow execer interface so the
// same methods run inside a *sql.Tx or against the top-level *sql.DB. This
// gives the workflow engines real ACID transactions: stage progression,
// evidence indexing, lease changes and idempotency responses commit together
// or not at all.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"

	"thermal-vacuum-test-gate/internal/store"
)

// execer is the common query surface of *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB is the concrete SQLite-backed store. It implements store.Store and, when
// operating inside WithTx, store.Tx.
type DB struct {
	exec execer
	tx   *sql.Tx // non-nil only inside a transaction
	db   *sql.DB // non-nil only at the top level
}

// Open opens (or creates) the database at the given DSN. An empty DSN selects
// a private in-memory database. The caller must run Migrate before use.
func Open(dsn string) (*DB, error) {
	if dsn == "" {
		dsn = "file::memory:?cache=shared"
	}
	// Enforce foreign keys for transactional integrity.
	if u, err := url.Parse(dsn); err == nil && u.Scheme == "file" {
		q := u.Query()
		if q.Get("_pragma") == "" {
			q.Set("_pragma", "foreign_keys(1)")
			u.RawQuery = q.Encode()
			dsn = u.String()
		}
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite serialises writers; a single connection avoids busy contention in
	// the embedded single-process use case.
	sqlDB.SetMaxOpenConns(1)
	return &DB{exec: sqlDB, db: sqlDB}, nil
}

// Close releases the underlying database handle.
func (d *DB) Close() error {
	if d.db == nil {
		return nil
	}
	return d.db.Close()
}

// Migrate applies the schema. It is idempotent so it can run on every start.
func (d *DB) Migrate(ctx context.Context) error {
	for _, stmt := range schema {
		if _, err := d.exec.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// WithTx runs fn inside a single transaction. If fn returns an error the
// transaction is rolled back; otherwise it is committed. Nested WithTx reuses
// the enclosing transaction.
func (d *DB) WithTx(ctx context.Context, fn func(store.Tx) error) error {
	if d.tx != nil {
		return fn(d)
	}
	if d.db == nil {
		return fmt.Errorf("store: transaction on closed database")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	inner := &DB{exec: tx, tx: tx}
	if err := fn(inner); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// Commit finalises the transaction. It is only valid inside WithTx.
func (d *DB) Commit() error {
	if d.tx == nil {
		return fmt.Errorf("store: commit outside transaction")
	}
	return d.tx.Commit()
}

// Rollback aborts the transaction. It is only valid inside WithTx.
func (d *DB) Rollback() error {
	if d.tx == nil {
		return fmt.Errorf("store: rollback outside transaction")
	}
	return d.tx.Rollback()
}

// schema is applied in order by Migrate. Every statement is idempotent.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS plans (
		id TEXT PRIMARY KEY,
		version INTEGER NOT NULL,
		specimen_id TEXT NOT NULL,
		cycles INTEGER NOT NULL,
		calibration_summary TEXT NOT NULL,
		locked_at_millis INTEGER NOT NULL,
		sensors_json TEXT NOT NULL,
		stages_json TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS runs (
		id TEXT PRIMARY KEY,
		plan_id TEXT NOT NULL,
		plan_version INTEGER NOT NULL,
		generation INTEGER NOT NULL,
		stage TEXT NOT NULL,
		current_cycle INTEGER NOT NULL,
		completed_cycles INTEGER NOT NULL,
		baseline_done INTEGER NOT NULL,
		frozen INTEGER NOT NULL,
		freeze_reason TEXT NOT NULL,
		completed INTEGER NOT NULL,
		event_seq INTEGER NOT NULL,
		created_at_millis INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS run_events (
		seq INTEGER NOT NULL,
		run_id TEXT NOT NULL,
		type TEXT NOT NULL,
		payload BLOB,
		at_millis INTEGER NOT NULL,
		PRIMARY KEY (run_id, seq)
	)`,
	`CREATE TABLE IF NOT EXISTS baselines (
		run_id TEXT PRIMARY KEY,
		install_check_ok INTEGER NOT NULL,
		door_closed INTEGER NOT NULL,
		initial_pressure_milli_pa INTEGER NOT NULL,
		sensor_zeros_json TEXT NOT NULL,
		completed INTEGER NOT NULL,
		completed_at_millis INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS leases (
		id TEXT PRIMARY KEY,
		equipment_id TEXT NOT NULL UNIQUE,
		holder TEXT NOT NULL,
		token TEXT NOT NULL,
		valid_until_millis INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS measurements (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		generation INTEGER NOT NULL,
		stage TEXT NOT NULL,
		cycle INTEGER NOT NULL,
		sensor_id TEXT NOT NULL,
		temperature_milli_kelvin INTEGER NOT NULL,
		pressure_milli_pa INTEGER NOT NULL,
		at_millis INTEGER NOT NULL,
		valid INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS evidence_windows (
		run_id TEXT NOT NULL,
		stage TEXT NOT NULL,
		cycle INTEGER NOT NULL,
		coverage_ppm INTEGER NOT NULL,
		samples INTEGER NOT NULL,
		range_milli_kelvin INTEGER NOT NULL,
		drift_ppm INTEGER NOT NULL,
		passed INTEGER NOT NULL,
		PRIMARY KEY (run_id, stage, cycle)
	)`,
	`CREATE TABLE IF NOT EXISTS measurement_calls (
		id TEXT PRIMARY KEY,
		attempt INTEGER NOT NULL,
		equipment_id TEXT NOT NULL,
		success INTEGER NOT NULL,
		failure_type TEXT NOT NULL,
		payload_summary TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS anomalies (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		summary TEXT NOT NULL,
		basis TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS retest_generations (
		run_id TEXT PRIMARY KEY,
		generation INTEGER NOT NULL,
		affected_json TEXT NOT NULL,
		coverage_json TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS reviews (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		reviewer TEXT NOT NULL,
		qualified INTEGER NOT NULL,
		digest TEXT NOT NULL,
		UNIQUE (run_id, reviewer)
	)`,
	`CREATE TABLE IF NOT EXISTS final_verdicts (
		run_id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		credential TEXT NOT NULL,
		event_seq INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS idempotency (
		key TEXT PRIMARY KEY,
		request_digest TEXT NOT NULL,
		status INTEGER NOT NULL,
		response BLOB,
		event_seq INTEGER NOT NULL
	)`,
}
