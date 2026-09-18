package projection

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SyncCursor represents the recorded tip and timestamp of a synced remote chain ref.
type SyncCursor struct {
	Remote       string    `json:"remote"`
	RefName      string    `json:"ref_name"`
	Tip          string    `json:"tip"`
	LastSyncedAt time.Time `json:"last_synced_at"`
}

// LocalDB returns the underlying *sql.DB connection pool for the local database.
func (d *DB) LocalDB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.localDB
}

// SetSyncCursor records the sync cursor tip and timestamp for a remote and ref name.
func (d *DB) SetSyncCursor(remote, refName, tip string, lastSyncedAt time.Time) error {
	if d == nil || d.localDB == nil {
		return fmt.Errorf("projection: local database is closed")
	}

	if lastSyncedAt.IsZero() {
		lastSyncedAt = time.Now().UTC()
	}

	_, err := d.localDB.Exec(`
		INSERT OR REPLACE INTO sync_cursors (remote, ref_name, tip, last_synced_at)
		VALUES (?, ?, ?, ?)
	`, remote, refName, tip, lastSyncedAt.Unix())
	if err != nil {
		return fmt.Errorf("projection: set sync cursor %s/%s: %w", remote, refName, err)
	}

	return nil
}

// SyncCursors returns the recorded sync cursors for the given remote (or all remotes if remote is empty).
func (d *DB) SyncCursors(remote string) ([]SyncCursor, error) {
	if d == nil || d.localDB == nil {
		return nil, fmt.Errorf("projection: local database is closed")
	}

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT remote, ref_name, tip, last_synced_at FROM sync_cursors")
	if remote != "" {
		sb.WriteString(" WHERE remote = ?")
		args = append(args, remote)
	}
	sb.WriteString(" ORDER BY remote ASC, ref_name ASC")

	rows, err := d.localDB.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query sync cursors: %w", err)
	}
	defer rows.Close()

	var cursors []SyncCursor
	for rows.Next() {
		var (
			sc          SyncCursor
			syncedAtSec int64
		)
		if err := rows.Scan(&sc.Remote, &sc.RefName, &sc.Tip, &syncedAtSec); err != nil {
			return nil, fmt.Errorf("projection: scan sync cursor: %w", err)
		}
		sc.LastSyncedAt = time.Unix(syncedAtSec, 0).UTC()
		cursors = append(cursors, sc)
	}

	return cursors, rows.Err()
}

// DumpLocalTables returns a deterministic dump of all local tables and their rows.
func (d *DB) DumpLocalTables() (map[string][]map[string]any, error) {
	if d == nil || d.localDB == nil {
		return nil, fmt.Errorf("projection: local database is closed")
	}

	dump := make(map[string][]map[string]any)

	for _, table := range localTables {
		query, ok := localTableQueries[table]
		if !ok {
			return nil, fmt.Errorf("missing query for local table %s", table)
		}
		rows, err := d.localDB.Query(query)
		if err != nil {
			return nil, fmt.Errorf("query local table %s: %w", table, err)
		}

		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("get columns for local table %s: %w", table, err)
		}

		var tableRows []map[string]any
		for rows.Next() {
			values := make([]any, len(cols))
			valuePtrs := make([]any, len(cols))
			for i := range values {
				valuePtrs[i] = &values[i]
			}

			if err := rows.Scan(valuePtrs...); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan row in local table %s: %w", table, err)
			}

			rowMap := make(map[string]any, len(cols))
			for i, col := range cols {
				val := values[i]
				if b, ok := val.([]byte); ok {
					rowMap[col] = string(b)
				} else {
					rowMap[col] = val
				}
			}
			tableRows = append(tableRows, rowMap)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate local table %s rows: %w", table, err)
		}

		dump[table] = tableRows
	}

	return dump, nil
}
