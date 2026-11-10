// Package store owns the agent's embedded SQLite database: the durable event
// outbox and the last applied policy snapshot.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS outbox (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id        TEXT NOT NULL UNIQUE,
    payload         BLOB NOT NULL,
    event_count     INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS outbox_ready ON outbox (next_attempt_at, id);
CREATE TABLE IF NOT EXISTS policy_cache (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    version    INTEGER NOT NULL,
    payload    BLOB NOT NULL,
    updated_at INTEGER NOT NULL
);
`

// Open creates the database directory, opens SQLite in WAL mode and applies
// the idempotent schema.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	dsn := path + "?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_foreign_keys=on"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	for _, stmt := range splitStatements(schema) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply sqlite schema: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func splitStatements(script string) []string {
	var out []string
	start := 0
	for i := 0; i < len(script); i++ {
		if script[i] != ';' {
			continue
		}
		stmt := script[start : i+1]
		if len(stmt) > 1 {
			out = append(out, stmt)
		}
		start = i + 1
	}
	return out
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SavePolicy stores the last applied policy snapshot.
func (s *Store) SavePolicy(version int64, payload []byte) error {
	_, err := s.db.Exec(`INSERT INTO policy_cache (id, version, payload, updated_at) VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET version = excluded.version, payload = excluded.payload, updated_at = excluded.updated_at`,
		version, payload, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("save policy cache: %w", err)
	}
	return nil
}

// LoadPolicy returns the cached snapshot.
func (s *Store) LoadPolicy() (int64, []byte, bool, error) {
	var version int64
	var payload []byte
	err := s.db.QueryRow(`SELECT version, payload FROM policy_cache WHERE id = 1`).Scan(&version, &payload)
	if err == sql.ErrNoRows {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, fmt.Errorf("load policy cache: %w", err)
	}
	return version, payload, true, nil
}

// Batch is one queued event batch.
type Batch struct {
	ID          int64
	BatchID     string
	Payload     []byte
	EventCount  int
	Attempts    int
	NextAttempt time.Time
	CreatedAt   time.Time
}

// EnqueueBatch appends a batch to the durable outbox.
func (s *Store) EnqueueBatch(ctx context.Context, batchID string, payload []byte, eventCount int, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO outbox (batch_id, payload, event_count, created_at, next_attempt_at)
		VALUES (?, ?, ?, ?, 0)`, batchID, payload, eventCount, now.Unix())
	if err != nil {
		return fmt.Errorf("enqueue event batch: %w", err)
	}
	return nil
}

// PendingBatches returns the oldest ready batches.
func (s *Store) PendingBatches(ctx context.Context, now time.Time, limit int) ([]Batch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, batch_id, payload, event_count, attempts, next_attempt_at, created_at
		FROM outbox WHERE next_attempt_at <= ? ORDER BY id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("select pending batches: %w", err)
	}
	defer rows.Close()
	var out []Batch
	for rows.Next() {
		var b Batch
		var next, created int64
		if err := rows.Scan(&b.ID, &b.BatchID, &b.Payload, &b.EventCount, &b.Attempts, &next, &created); err != nil {
			return nil, fmt.Errorf("scan pending batch: %w", err)
		}
		b.NextAttempt = time.Unix(next, 0)
		b.CreatedAt = time.Unix(created, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBatch removes an acknowledged batch.
func (s *Store) DeleteBatch(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete batch: %w", err)
	}
	return nil
}

// RescheduleBatch records a failed attempt.
func (s *Store) RescheduleBatch(ctx context.Context, id int64, attempts int, next time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE outbox SET attempts = ?, next_attempt_at = ? WHERE id = ?`, attempts, next.Unix(), id); err != nil {
		return fmt.Errorf("reschedule batch: %w", err)
	}
	return nil
}

// DropBatch removes a poison batch and returns its event count.
func (s *Store) DropBatch(ctx context.Context, id int64) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT event_count FROM outbox WHERE id = ?`, id).Scan(&count); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("read dropped batch: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, id); err != nil {
		return count, fmt.Errorf("drop poison batch: %w", err)
	}
	return count, nil
}

// Counts returns queued batch and event totals.
func (s *Store) Counts(ctx context.Context) (batches, events int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(event_count), 0) FROM outbox`).Scan(&batches, &events)
	if err != nil {
		return 0, 0, fmt.Errorf("count outbox: %w", err)
	}
	return batches, events, nil
}

// Prune keeps the outbox under maxBatches by deleting the oldest batches. It
// returns the number of dropped batches and events.
func (s *Store) Prune(ctx context.Context, maxBatches int) (droppedBatches, droppedEvents int64, err error) {
	batches, _, err := s.Counts(ctx)
	if err != nil {
		return 0, 0, err
	}
	excess := int(batches) - maxBatches
	if excess <= 0 {
		return 0, 0, nil
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(event_count),0) FROM (
		SELECT event_count FROM outbox ORDER BY id LIMIT ?)`, excess).Scan(&droppedEvents); err != nil {
		return 0, 0, fmt.Errorf("sum pruned events: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE id IN (SELECT id FROM outbox ORDER BY id LIMIT ?)`, excess); err != nil {
		return 0, 0, fmt.Errorf("prune outbox: %w", err)
	}
	return int64(excess), droppedEvents, nil
}
