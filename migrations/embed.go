// Package migrations embeds SQL migrations so binaries stay self-contained.
package migrations

import "embed"

// FS contains PostgreSQL and ClickHouse migrations.
//
//go:embed postgres/*.sql clickhouse/*.sql
var FS embed.FS
