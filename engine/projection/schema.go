package projection

// schemaVersion invalidates the cache whenever what is stored in it changes
// meaning, not only when a column does. Projection rows hold *folded* person
// identifiers and Refresh is incremental, so a rule change leaves rows written
// under the old rule sitting beside rows written under the new one, while
// the generic readers normalize their filters with the new rule — and an
// assignee filter then quietly matches nothing. Bumping is how a
// normalization change reaches an existing checkout.
//
// This covers only the substrate tables below (meta, chain_tips, code_tips,
// ops, objects, unknown_ops, anchor_resolutions): a schema change to the
// per-type generated tables invalidates the cache through a different key,
// the descriptor digest ApplySchema computes from the current rule index and
// compares against the one recorded in meta on the previous apply. Two keys,
// two jobs — schemaVersion for substrate shape, the digest for generated
// shape — and projection_test.go's literal keeps its meaning for the first.
//
// 7: WRIT-117 pinned the person-identifier folding algorithm to NFC, Unicode
// default case folding, NFC (spec/identifiers.md). Also covers WRIT-102, which
// 8: WRIT-155 added object_type column to unknown_ops table (spec FC-5).
// 9: WRIT-104 added workflow_states table.
// 10: WRIT-109 added labels table.
// 11: WRIT-105 added documents, document_links, document_labels, sections tables.
// 12: WRIT-106 added priority, estimate, position, position_op_id to issues table.
// 13: WRIT-110 added settings table.
// 14: WRIT-182 removed the repos and repo_remotes tables (repo registry object type deleted).
// 15: WRIT-189 replaced every per-type table with ones generated from the
// schema in the log; anchor_resolutions is generalized and its columns
// unchanged, so it stays here rather than moving with the generated tables.
const schemaVersion = 15

// substrateTables lists the type-agnostic tables created unconditionally at
// Open, before any schema is ever applied: meta, chain_tips, code_tips, ops,
// objects, unknown_ops, and anchor_resolutions (generalized, but neither
// per-type nor dropped on a schema change the way a generated table is —
// ApplySchema truncates it instead, since which targets are anchors is a
// function of the schema even though its own shape is not).
var substrateTables = []string{
	"meta",
	"chain_tips",
	"code_tips",
	"ops",
	"objects",
	"unknown_ops",
	"anchor_resolutions",
}

var substrateTableQueries = map[string]string{
	"meta":               "SELECT * FROM meta ORDER BY key ASC",
	"chain_tips":         "SELECT * FROM chain_tips ORDER BY ref_name ASC",
	"code_tips":          "SELECT * FROM code_tips ORDER BY ref_name ASC",
	"ops":                "SELECT * FROM ops ORDER BY op_id ASC",
	"objects":            "SELECT * FROM objects ORDER BY object_id ASC",
	"unknown_ops":        "SELECT * FROM unknown_ops ORDER BY object_id ASC, op_index ASC",
	"anchor_resolutions": "SELECT * FROM anchor_resolutions ORDER BY object_id ASC, target ASC, target_commit ASC, side ASC",
}

// substrateSQL creates every type-agnostic table. Nothing here mentions an
// SDLC type: `meta`, `chain_tips`, `code_tips`, `ops`, `objects`,
// `unknown_ops` are untouched from before this ticket, and
// `anchor_resolutions` is generalized to a target/value_type-driven shape
// rather than a hard-coded read of a `comments.anchor` column.
const substrateSQL = `
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS chain_tips (
    ref_name TEXT PRIMARY KEY,
    tip TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS code_tips (
    ref_name TEXT PRIMARY KEY,
    tip TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ops (
    op_id TEXT PRIMARY KEY,
    object_id TEXT NOT NULL,
    object_type TEXT NOT NULL,
    op_type TEXT NOT NULL,
    op_version INTEGER NOT NULL,
    parents TEXT NOT NULL,
    author_name TEXT NOT NULL,
    author_email TEXT NOT NULL,
    author_time INTEGER NOT NULL,
    author_tz TEXT NOT NULL,
    committer_name TEXT NOT NULL,
    committer_email TEXT NOT NULL,
    committer_time INTEGER NOT NULL,
    committer_tz TEXT NOT NULL,
    message TEXT NOT NULL,
    signature TEXT,
    payload BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ops_object_id ON ops(object_id);

CREATE TABLE IF NOT EXISTS objects (
    object_id TEXT PRIMARY KEY,
    object_type TEXT NOT NULL,
    op_count INTEGER NOT NULL,
    last_op_id TEXT NOT NULL,
    author_name TEXT NOT NULL,
    author_email TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_objects_author_email ON objects(author_email);
CREATE INDEX IF NOT EXISTS idx_objects_object_type ON objects(object_type);

CREATE TABLE IF NOT EXISTS unknown_ops (
    object_id TEXT NOT NULL,
    op_id TEXT NOT NULL,
    object_type TEXT NOT NULL,
    op_type TEXT NOT NULL,
    op_version INTEGER NOT NULL,
    op_index INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (object_id, op_id)
);
CREATE INDEX IF NOT EXISTS idx_unknown_ops_object_id ON unknown_ops(object_id);

CREATE TABLE IF NOT EXISTS anchor_resolutions (
    object_id TEXT NOT NULL,
    target TEXT NOT NULL,
    target_commit TEXT NOT NULL,
    side TEXT NOT NULL,
    outcome TEXT NOT NULL,
    match TEXT NOT NULL,
    path TEXT NOT NULL,
    start_line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY (object_id, target, target_commit, side)
);
CREATE INDEX IF NOT EXISTS idx_anchor_resolutions_object ON anchor_resolutions(object_id);
CREATE INDEX IF NOT EXISTS idx_anchor_resolutions_target_commit ON anchor_resolutions(target_commit);
`
