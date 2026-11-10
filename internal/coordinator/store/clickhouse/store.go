// Package clickhouse stores and queries Kurwall network events.
package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/kurwall/kurwall/migrations"
)

// Store owns ClickHouse connections.
type Store struct {
	conn    driver.Conn
	timeout time.Duration
}

// Open applies the idempotent schema and returns a store bound to database.
func Open(ctx context.Context, addr, database, username, password string, dialTimeout time.Duration) (*Store, error) {
	admin, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{addr},
		DialTimeout: dialTimeout,
		Auth:        clickhouse.Auth{Username: username, Password: password},
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionZSTD},
	})
	if err != nil {
		return nil, fmt.Errorf("connect clickhouse: %w", err)
	}
	if err := admin.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping clickhouse: %w", err)
	}
	if err := migrate(ctx, admin, database); err != nil {
		_ = admin.Close()
		return nil, err
	}
	_ = admin.Close()

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:            []string{addr},
		DialTimeout:     dialTimeout,
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: time.Hour,
		Auth:            clickhouse.Auth{Database: database, Username: username, Password: password},
		Compression:     &clickhouse.Compression{Method: clickhouse.CompressionZSTD},
		Settings:        clickhouse.Settings{"max_execution_time": 30},
	})
	if err != nil {
		return nil, fmt.Errorf("connect clickhouse database: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping clickhouse database: %w", err)
	}
	return &Store{conn: conn, timeout: 20 * time.Second}, nil
}

func migrate(ctx context.Context, conn driver.Conn, database string) error {
	raw, err := migrations.FS.ReadFile("clickhouse/00001_schema.sql")
	if err != nil {
		return fmt.Errorf("read clickhouse schema: %w", err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		lines = append(lines, line)
	}
	script := strings.ReplaceAll(strings.Join(lines, "\n"), "kurwall.", database+".")
	for _, stmt := range strings.Split(script, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("apply clickhouse schema: %w", err)
		}
	}
	return nil
}

// Close closes the ClickHouse connection.
func (s *Store) Close() error { return s.conn.Close() }

// Health verifies ClickHouse connectivity.
func (s *Store) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.conn.Ping(ctx)
}
