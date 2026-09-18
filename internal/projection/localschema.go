package projection

import (
	"fmt"
	"strconv"
)

// localSchemaVersion 3: WRIT-275 dropped the read_state table with
// Store.ReadState (Mark/Clear/Unread), whose only reader that deletion
// removed.
const localSchemaVersion = 3

var localTables = []string{
	"meta",
	"sync_cursors",
}

var localTableQueries = map[string]string{
	"meta":         "SELECT * FROM meta ORDER BY key ASC",
	"sync_cursors": "SELECT * FROM sync_cursors ORDER BY remote ASC, ref_name ASC",
}

const localSchemaSQL = `
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_cursors (
    remote TEXT NOT NULL,
    ref_name TEXT NOT NULL,
    tip TEXT NOT NULL,
    last_synced_at INTEGER NOT NULL,
    PRIMARY KEY (remote, ref_name)
);
`

func (d *DB) ensureLocalSchema() error {
	var versionStr string
	err := d.localDB.QueryRow("SELECT value FROM meta WHERE key = 'local_schema_version'").Scan(&versionStr)
	if err != nil || versionStr != strconv.Itoa(localSchemaVersion) {
		if err := d.resetLocalSchema(); err != nil {
			return fmt.Errorf("projection: reset local schema: %w", err)
		}
	}
	return nil
}

func (d *DB) resetLocalSchema() error {
	// 1. Drop all existing user tables and views from localDB
	rows, err := d.localDB.Query("SELECT name, type FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return fmt.Errorf("projection: query local sqlite_master: %w", err)
	}

	var drops []string
	for rows.Next() {
		var name, objType string
		if err := rows.Scan(&name, &objType); err != nil {
			_ = rows.Close()
			return err
		}
		if objType == "view" {
			drops = append(drops, fmt.Sprintf("DROP VIEW IF EXISTS %s", name))
		} else {
			drops = append(drops, fmt.Sprintf("DROP TABLE IF EXISTS %s", name))
		}
	}
	_ = rows.Close()

	for _, dropStmt := range drops {
		if _, err := d.localDB.Exec(dropStmt); err != nil {
			return fmt.Errorf("projection: exec %s on local: %w", dropStmt, err)
		}
	}

	// 2. Execute localSchemaSQL
	if _, err := d.localDB.Exec(localSchemaSQL); err != nil {
		return fmt.Errorf("projection: exec localSchemaSQL: %w", err)
	}

	// 3. Set local_schema_version in meta
	if _, err := d.localDB.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES ('local_schema_version', ?)", strconv.Itoa(localSchemaVersion)); err != nil {
		return fmt.Errorf("projection: record local_schema_version: %w", err)
	}

	return nil
}

// LocalSchemaVersion returns the current expected local schema version integer.
func LocalSchemaVersion() int {
	return localSchemaVersion
}
