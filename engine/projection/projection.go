// Package projection provides a versioned SQLite cache of collaborative objects
// and ref tips for Writ.
package projection

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/writtendev/writ/engine/state"
	_ "modernc.org/sqlite"
)

// OpenOption configures an Open invocation on the projection database.
type OpenOption func(*openConfig)

type openConfig struct {
	localPath string
}

// WithLocalPath configures an explicit custom filesystem path for the local SQLite database.
func WithLocalPath(path string) OpenOption {
	return func(c *openConfig) {
		c.localPath = path
	}
}

// DB represents a handle to the projection SQLite cache and accompanying local-only database.
type DB struct {
	db        *sql.DB
	path      string
	localDB   *sql.DB
	localPath string

	// descMu guards desc: ApplySchema (called from Refresh/Rebuild, and
	// directly by callers driving the projection package on its own) writes
	// it, materialize/query/DumpTables read it.
	descMu sync.RWMutex
	desc   *schemaDescriptor
}

// Open opens or creates a projection SQLite database at path and an accompanying local-only database,
// validating their respective schema versions. If the projection database is missing or the schema version
// does not match SchemaVersion(), folded tables are dropped and recreated cleanly without migration heroics.
// Local-only state is managed and preserved independently.
func Open(path string, opts ...OpenOption) (*DB, error) {
	cfg := &openConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	localPath := cfg.localPath
	if localPath == "" {
		if path == ":memory:" {
			localPath = ":memory:"
		} else {
			localPath = strings.TrimSuffix(path, ".db") + ".local.db"
		}
	}

	db, err := sql.Open("sqlite", formatDSN(path))
	if err != nil {
		return nil, fmt.Errorf("projection: open sqlite %q: %w", path, err)
	}
	if isMemory(path) {
		db.SetMaxOpenConns(1)
	}

	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA synchronous = NORMAL;",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("projection: exec %q: %w", pragma, err)
		}
	}

	localDB, err := sql.Open("sqlite", formatDSN(localPath))
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("projection: open local sqlite %q: %w", localPath, err)
	}
	if isMemory(localPath) {
		localDB.SetMaxOpenConns(1)
	}

	for _, pragma := range pragmas {
		if _, err := localDB.Exec(pragma); err != nil {
			_ = db.Close()
			_ = localDB.Close()
			return nil, fmt.Errorf("projection: exec %q on local: %w", pragma, err)
		}
	}

	proj := &DB{
		db:        db,
		path:      path,
		localDB:   localDB,
		localPath: localPath,
	}

	if err := proj.ensureSchema(); err != nil {
		_ = proj.Close()
		return nil, err
	}

	if err := proj.ensureLocalSchema(); err != nil {
		_ = proj.Close()
		return nil, err
	}

	// Generated tables, if any exist in the file, were created by a prior
	// ApplySchema call; nothing here recreates them. Reload just enough of
	// that descriptor (table names and primary keys) to answer DumpTables
	// and drive Rebuild's truncate step with no schema argument and no DAG
	// access — a reopened cache is queryable on its own.
	if err := proj.loadPersistedTables(); err != nil {
		_ = proj.Close()
		return nil, err
	}

	return proj, nil
}

// Close closes both the projection and local SQLite database connections.
func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	var errs []error
	if d.db != nil {
		if err := d.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if d.localDB != nil {
		if err := d.localDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DB returns the underlying *sql.DB connection pool for custom queries.
func (d *DB) DB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

// SchemaVersion returns the current expected schema version integer.
func SchemaVersion() int {
	return schemaVersion
}

func (d *DB) ensureSchema() error {
	var versionStr string
	err := d.db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&versionStr)
	if err != nil || versionStr != strconv.Itoa(schemaVersion) {
		// Version mismatch or missing: reset everything
		if err := d.resetSchema(); err != nil {
			return fmt.Errorf("projection: reset schema: %w", err)
		}
	}
	return nil
}

func (d *DB) resetSchema() error {
	// 1. Query and drop all existing user tables and views
	rows, err := d.db.Query("SELECT name, type FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return fmt.Errorf("projection: query sqlite_master: %w", err)
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
		if _, err := d.db.Exec(dropStmt); err != nil {
			return fmt.Errorf("projection: exec %s: %w", dropStmt, err)
		}
	}

	// 2. Execute substrate schema SQL. Generated (per-type) tables are not
	// recreated here: a substrate version bump invalidates everything,
	// including any prior descriptor, exactly like the digest-driven path
	// ApplySchema takes on a schema change — meta (and so schema_digest and
	// schema_tables) was just dropped above, so the next ApplySchema call
	// sees no stored digest and creates fresh generated tables.
	if _, err := d.db.Exec(substrateSQL); err != nil {
		return fmt.Errorf("projection: exec substrateSQL: %w", err)
	}

	// 3. Set schema_version in meta
	if _, err := d.db.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', ?)", strconv.Itoa(schemaVersion)); err != nil {
		return fmt.Errorf("projection: record schema_version: %w", err)
	}

	return nil
}

// loadPersistedTables reloads the DumpTables/Rebuild-truncate view of
// whatever descriptor a prior ApplySchema call in some process left
// recorded in meta, with no rules and no DAG access: a reopened cache is
// queryable on its own. If none was ever recorded, desc starts out with no
// generated tables at all — the same shape a schema-less substrate-only cache
// has always had — until this process's own first ApplySchema call. It also
// reloads "schema_query_shapes" the same way, so Objects' f.Text and
// !IncludeDeleted filters (objectsTextClause, objectsNotDeletedClause in
// query.go) are correct immediately on reopen too, not only after this
// process's own first ApplySchema/Refresh call (WRIT-192 round 2 MAJOR-1).
func (d *DB) loadPersistedTables() error {
	raw, ok := loadMetaString(d.db, "schema_tables")
	if !ok || raw == "" {
		desc, err := descriptorFromPersisted(nil, "")
		if err != nil {
			return err
		}
		d.desc = desc
		return nil
	}
	var tables []persistedTable
	if err := json.Unmarshal([]byte(raw), &tables); err != nil {
		return fmt.Errorf("projection: unmarshal schema_tables: %w", err)
	}
	queryShapesJSON, _ := loadMetaString(d.db, "schema_query_shapes")
	desc, err := descriptorFromPersisted(tables, queryShapesJSON)
	if err != nil {
		return err
	}
	d.desc = desc
	return nil
}

// metaQueryable is satisfied by both *sql.DB and *sql.Tx.
type metaQueryable interface {
	QueryRow(query string, args ...any) *sql.Row
}

func loadMetaString(q metaQueryable, key string) (string, bool) {
	var v string
	if err := q.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

// loadMetaBool reports whether meta's key holds a truthy value ("1"),
// distinguishing "not set" (false, nil error) from a real query failure.
func loadMetaBool(q metaQueryable, key string) (bool, error) {
	var v string
	err := q.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("projection: query meta %s: %w", key, err)
	}
	return v == "1", nil
}

// tableHasRows reports whether table already holds at least one row, run
// inside ApplySchema's transaction so it sees this call's own writes-so-far
// (none, at the point ApplySchema calls it) but not a concurrent writer's.
func tableHasRows(tx *sql.Tx, table string) (bool, error) {
	var n int
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM " + table + ")").Scan(&n); err != nil {
		return false, fmt.Errorf("projection: check %s for existing rows: %w", table, err)
	}
	return n != 0, nil
}

// ApplySchema builds a schemaDescriptor from rules (RulesFromSchemas' shape:
// every object type a consumer's schema declares — writ hard-codes no
// object type but `schema`, so there is no built-in vocabulary to overlay —
// the projection cannot resolve schemas itself, so it consumes an
// already-validated index) and reconciles it against whatever generated
// tables exist on disk.
//
// The meta keys (schema_digest, schema_tables, schema_descriptor,
// schema_query_shapes) are written every call, regardless of whether the
// digest changed. They must not be gated on the digest: the digest
// (buildSnapshot) covers only column name/SQL type/indexed/PK, so a schema
// change that leaves it unchanged — a strategy change alone (lww,
// create-once, lattice and tombstone all emit the same ddlColumn), or a
// value_type change that sqlType collapses to the same SQL type (string,
// text, enum, timestamp, person-ref, object-ref and git-oid all land in
// TEXT) — used to skip this write entirely under the old equal-digest early
// return. schema_query_shapes carries exactly ValueType and Strategy per
// target, so it went stale on precisely the changes the digest cannot see:
// the in-process descriptor was correct (d.desc = newDesc always ran), but
// the persisted copy a warm reopen rehydrates from
// (descriptorFromPersisted) kept answering with the old strategy/value_type
// forever, with nothing to repair it — the same symptom round 2's MAJOR-1
// fixed, reintroduced on a new trigger (WRIT-192 round 3 MAJOR). Writing the
// meta keys unconditionally is the fix; folding value_type/strategy into the
// digest itself was considered and rejected as widening blast radius into
// needs_rebuild's pre-existing gap (see the comment below) — that belongs to
// its own change, if anyone wants it.
//
// Equal digest still means no DDL is needed: generated tables already hold
// correctly-shaped rows, so nothing is dropped or recreated and
// needs_rebuild is not set. A different digest drops every table named in
// the previously recorded descriptor, plus any stray o_-prefixed table not
// in the new one (catches a torn write), emits the new DDL, truncates
// anchor_resolutions (which targets are anchor-valued is a function of the
// schema), and sets needs_rebuild so the next Refresh takes the
// full-rebuild path exactly as a rewound tip does today — the
// droppable-cache answer to a schema change is drop and rebuild, never
// migrate (ARCHITECTURE.md, AGENTS.md).
func (d *DB) ApplySchema(rules map[string][]state.Rule) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("projection: database is closed")
	}

	newDesc, err := buildDescriptor(rules)
	if err != nil {
		return fmt.Errorf("projection: build schema descriptor: %w", err)
	}

	d.descMu.Lock()
	defer d.descMu.Unlock()

	storedDigest, hadPrior := loadMetaString(d.db, "schema_digest")
	sameDigest := storedDigest == newDesc.digest

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("projection: begin apply schema: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	metaWrites := map[string]string{}

	if !sameDigest {
		drop := make(map[string]bool)
		if raw, ok := loadMetaString(tx, "schema_tables"); ok && raw != "" {
			var oldTables []persistedTable
			if err := json.Unmarshal([]byte(raw), &oldTables); err == nil {
				for _, t := range oldTables {
					drop[t.Name] = true
				}
			}
		}

		newNames := make(map[string]bool, len(newDesc.tables))
		for _, t := range newDesc.tables {
			newNames[t.Name] = true
		}

		strayRows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'o\_%' ESCAPE '\'`)
		if err != nil {
			return fmt.Errorf("projection: query generated tables: %w", err)
		}
		var strayNames []string
		for strayRows.Next() {
			var name string
			if err := strayRows.Scan(&name); err != nil {
				_ = strayRows.Close()
				return fmt.Errorf("projection: scan generated table name: %w", err)
			}
			strayNames = append(strayNames, name)
		}
		_ = strayRows.Close()
		if err := strayRows.Err(); err != nil {
			return fmt.Errorf("projection: iterate generated tables: %w", err)
		}
		for _, name := range strayNames {
			if !newNames[name] {
				drop[name] = true
			}
		}

		dropNames := make([]string, 0, len(drop))
		for name := range drop {
			dropNames = append(dropNames, name)
		}
		sort.Strings(dropNames)
		for _, name := range dropNames {
			if _, err := tx.Exec("DROP TABLE IF EXISTS " + name); err != nil {
				return fmt.Errorf("projection: drop table %s: %w", name, err)
			}
		}

		if ddl := newDesc.createSQL(); ddl != "" {
			if _, err := tx.Exec(ddl); err != nil {
				return fmt.Errorf("projection: exec generated schema: %w", err)
			}
		}

		if _, err := tx.Exec("DELETE FROM anchor_resolutions"); err != nil {
			return fmt.Errorf("projection: truncate anchor_resolutions: %w", err)
		}

		// needs_rebuild must be set whenever there is already-materialized
		// data that could be stale under the schema just applied — not only
		// when a prior digest was recorded. hadPrior is the common case: a
		// real schema change invalidates rows folded under the old
		// descriptor. But a first-ever ApplySchema (no prior digest) is not
		// automatically nothing-to-invalidate: a schema-less Refresh may
		// already have run on this cache — every op it touched had no
		// installed rules to fold against, so it folded to unknown_ops
		// rather than the generated tables this ApplySchema is creating for
		// the first time (MEDIUM-4, WRIT-189 round 1). Only an ApplySchema
		// that finds the objects table genuinely empty — nothing yet
		// folded, schema-less or otherwise — can safely leave Refresh on
		// the incremental path it was already taking
		// (TestIncrementalRefoldMatchesColdRebuild): there is nothing for
		// it to have gotten wrong yet.
		hasPriorData, err := tableHasRows(tx, "objects")
		if err != nil {
			return fmt.Errorf("projection: check existing objects: %w", err)
		}
		if hadPrior || hasPriorData {
			metaWrites["needs_rebuild"] = "1"
		}
	}

	tablesJSON, err := json.Marshal(persistedTables(newDesc))
	if err != nil {
		return fmt.Errorf("projection: marshal persisted tables: %w", err)
	}
	queryShapesJSON, err := json.Marshal(persistedQueryShapes(newDesc))
	if err != nil {
		return fmt.Errorf("projection: marshal persisted query shapes: %w", err)
	}
	metaWrites["schema_digest"] = newDesc.digest
	metaWrites["schema_tables"] = string(tablesJSON)
	metaWrites["schema_descriptor"] = string(newDesc.canonicalJSON)
	metaWrites["schema_query_shapes"] = string(queryShapesJSON)

	for key, value := range metaWrites {
		if _, err := tx.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", key, value); err != nil {
			return fmt.Errorf("projection: record %s: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("projection: commit apply schema: %w", err)
	}

	d.desc = newDesc
	return nil
}

// descriptor returns the current in-memory schema descriptor, thread-safely.
func (d *DB) descriptor() *schemaDescriptor {
	d.descMu.RLock()
	defer d.descMu.RUnlock()
	return d.desc
}

// requireMaterializationPlan refuses to fold anything against a name-only
// descriptor: descriptorFromPersisted (loadPersistedTables, the lazy-Open
// path) rebuilds only desc.tables — the DumpTables/Rebuild-truncate view —
// leaving desc.types nil, with no per-type materialization plan at all.
// Refresh/Rebuild without WithSchema on a handle in that state used to
// proceed anyway: materializeObject's `td == nil` branch quietly routed
// every object to unknown_ops (the same path an intentionally schema-less
// caller already takes), while deleteObjectState's own `desc.types[...]`
// lookup found nothing and left that object's previously generated rows in
// place — silent divergence between what unknown_ops now claims and what
// the generated tables still show, not an error (WRIT-189 round 2 MINOR-6).
//
// desc.types == nil alone is not enough to distinguish that hazard from a
// genuinely fresh cache that has never had a schema applied in any process
// (round 1's MEDIUM-4, TestFirstApplySchemaAfterSchemaLessRefreshRebuilds):
// there, desc.tables is empty too, so unknown_ops is exactly correct and
// deleteObjectState has nothing to leave stale — a legitimate, intentional
// schema-less Refresh that must keep working. len(desc.tables) > 0 is what
// tells the two apart: only a cache carrying generated tables from a prior
// ApplySchema (this process's or an earlier one's, persisted in meta) can
// have rows a schema-less materialize would strand.
//
// No shipped writ path hits the real hazard: engine.Store.Refresh/Rebuild
// always pass WithSchema. WithSchema's own doc comment already states the
// only legitimate reason to omit it — a caller that already applied a
// schema on this same *DB directly — and that caller's ApplySchema call
// already left desc.types non-nil by the time Refresh/Rebuild runs, so this
// never refuses a legitimate call.
func (d *DB) requireMaterializationPlan() error {
	desc := d.descriptor()
	if desc != nil && desc.types == nil && len(desc.tables) > 0 {
		return fmt.Errorf("projection: this handle has generated tables from a prior schema but no materialization plan for this process; call Refresh/Rebuild with WithSchema, or ApplySchema directly, before folding any objects")
	}
	return nil
}

// HasGeneratedTables reports whether this cache already has at least one
// generated (per-type) table — either created by an ApplySchema call
// earlier in this process, or reloaded on Open from meta's persisted table
// list left by a prior process's ApplySchema call, no DAG access required
// either way (loadPersistedTables). writ.Open uses this to decide whether it
// needs to resolve the schema from the log at all: a fresh cache has never
// had ApplySchema run and so has no generated tables (only the fixed
// substrate ones), but a reopened cache already has them, correctly shaped
// for whatever schema last applied — a real schema change since is caught
// lazily, the same way any other log change is, the next time something
// actually calls Refresh.
func (d *DB) HasGeneratedTables() bool {
	if d == nil {
		return false
	}
	return len(d.descriptor().allTables()) > 0
}

// DumpTables returns a deterministic dump of all projection tables and their rows.
// Used primarily for asserting byte-for-byte equality across incremental vs cold builds.
func (d *DB) DumpTables() (map[string][]map[string]any, error) {
	dump := make(map[string][]map[string]any)

	dumpOne := func(table, query string) error {
		rows, err := d.db.Query(query)
		if err != nil {
			return fmt.Errorf("query table %s: %w", table, err)
		}

		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("get columns for %s: %w", table, err)
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
				return fmt.Errorf("scan row in %s: %w", table, err)
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
			return fmt.Errorf("iterate %s rows: %w", table, err)
		}

		dump[table] = tableRows
		return nil
	}

	for _, table := range substrateTables {
		if err := dumpOne(table, substrateTableQueries[table]); err != nil {
			return nil, err
		}
	}
	for _, t := range d.descriptor().allTables() {
		if err := dumpOne(t.Name, t.orderByQuery()); err != nil {
			return nil, err
		}
	}

	return dump, nil
}

// String returns a brief representation of the DB connection path.
func (d *DB) String() string {
	if strings.Contains(d.path, ":memory:") {
		return "projection:memory"
	}
	return "projection:" + d.path
}

func formatDSN(path string) string {
	const params = "_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL"
	if strings.Contains(path, "?") {
		if strings.HasSuffix(path, "?") || strings.HasSuffix(path, "&") {
			return path + params
		}
		return path + "&" + params
	}
	return path + "?" + params
}

func isMemory(path string) bool {
	return path == ":memory:" || strings.Contains(path, ":memory:") || strings.Contains(path, "mode=memory")
}
