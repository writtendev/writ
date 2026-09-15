// Package sqlitedriver holds a throwaway benchmark comparing mattn/go-sqlite3
// (cgo) against modernc.org/sqlite (pure Go) on a schema shaped like Writ's
// projection: items with an indexed set of entries, bulk-loaded the way a
// from-scratch refold would, then read back by the index a live query would
// use.
package sqlitedriver

const schema = `
CREATE TABLE items (
	id     TEXT PRIMARY KEY,
	status TEXT NOT NULL,
	base   TEXT NOT NULL,
	head   TEXT NOT NULL
);
CREATE TABLE entries (
	id         TEXT PRIMARY KEY,
	item_id    TEXT NOT NULL,
	author     TEXT NOT NULL,
	body       TEXT NOT NULL,
	blob_hash  TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	resolved   INTEGER NOT NULL
);
CREATE INDEX idx_entries_item_id ON entries(item_id);
`

// Sizes chosen to sit in the neighborhood of an imported history for an
// active repo: a few thousand items, tens of thousands of entries.
const (
	numItems       = 5000
	entriesPerItem = 20
	numEntries     = numItems * entriesPerItem
)
