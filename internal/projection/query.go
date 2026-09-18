package projection

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned when an object is not found in the projection.
var ErrNotFound = errors.New("writ: object not found")

// Author holds the author display name and email address derived from an object's operations.
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ObjectResult represents summary metadata for any collaborative object cross-type.
type ObjectResult struct {
	ObjectID   string    `json:"object_id"`
	ObjectType string    `json:"object_type"`
	Author     Author    `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	OpCount    int       `json:"op_count"`
	// Verification is the worst signature-verification outcome (WRIT-251
	// ruling 3's ordering) among the object's contributing ops, cached in
	// the objects table's own column by the materializer at Refresh/Rebuild
	// time — an envelope-level fact like Author and CreatedAt, not
	// something a caller needs to ask the DAG for separately. It is
	// reported, not enforced: an object whose ops did not verify still
	// appears in results, unfiltered by that outcome.
	Verification string `json:"verification"`
}

// objectTextColumns returns shape's generated table name and the sorted
// "f_"-prefixed columns of its scalar targets whose declared value_type is
// "string" or "text" — every column Objects' full-text search below can
// usefully LIKE against. Only lww/create-once/lattice/tombstone targets
// carry a Column at all (a collection or keyed-lww target materializes into
// a child table instead), so shape.Targets already holds exactly the scalar
// ones (objectQueryShapeFromType, ddl.go).
func objectTextColumns(shape objectQueryShape) (table string, columns []string) {
	for _, target := range shape.Targets {
		if target.ValueType != "string" && target.ValueType != "text" {
			continue
		}
		columns = append(columns, target.Column)
	}
	sort.Strings(columns)
	return shape.Table, columns
}

// joinBalanced joins parts with op as a balanced binary tree rather than a
// flat left-deep chain, so the generated expression's height is O(log n) in
// the number of parts instead of O(n). SQLite's SQLITE_MAX_EXPR_DEPTH
// (1000 by default) counts every term of a flat OR/AND chain as one level
// of depth, so objectsTextClause and objectsNotDeletedClause's one-term-
// per-column chains used to breach it well short of 1000 columns on a wide
// schema; a balanced tree of the same terms is height O(log n) instead
// (WRIT-285). One part is returned as-is; otherwise this recurses on each
// half and parenthesizes the combination. Both callers already return early
// on len(parts) == 0, so this need not handle it.
func joinBalanced(parts []string, op string) string {
	if len(parts) == 1 {
		return parts[0]
	}
	mid := len(parts) / 2
	return "(" + joinBalanced(parts[:mid], op) + " " + op + " " + joinBalanced(parts[mid:], op) + ")"
}

// sqliteMaxVariableNumber mirrors SQLite's default SQLITE_MAX_VARIABLE_NUMBER
// (modernc.org/sqlite, the driver this package runs on, does not raise it).
// wideTextSearchBindThreshold is half of that: objectsTextClause's own LIKE
// binds are only one contributor to a query's total bind count, which also
// includes f.Type, f.Author, and the limit param — all fixed-size, one or
// two binds regardless of schema shape — sharing the same connection-wide
// counter, and the threshold leaves that much headroom for those rather
// than cutting it as close as correctness alone would allow.
//
// It does NOT leave headroom for objectsNotDeletedClause's per-type
// object_type binds: those scale with the same quantity this threshold
// bounds (a schema of many narrow types has a type count roughly equal to
// its contributing text-column count), so at the threshold both clauses'
// binds can sum past sqliteMaxVariableNumber regardless of which branch
// objectsTextClause takes — the wide branch binds one param per
// contributing type too, so switching to it does not help. This is not a
// regression: such a schema already breaches SQLITE_MAX_EXPR_DEPTH at
// roughly 1,000 types, well short of the ~16,383 types this shape needs, so
// no divisor here changes what actually fails first. The /2 buys headroom
// against the fixed-size contributors above; it was never sized to cover
// objectsNotDeletedClause's per-type cost, which is a distinct bind ceiling
// this constant does not address.
const sqliteMaxVariableNumber = 32766
const wideTextSearchBindThreshold = sqliteMaxVariableNumber / 2

// objectsTextClause builds Objects' f.Text filter directly from the
// installed schema descriptor: one EXISTS per declared type (restricted to
// restrictTypes when non-empty, the types f.Type itself already narrows the
// query to) over that type's own string/text scalar columns, replacing the
// five built-in table/column literals WRIT-189 round 3 MAJOR-2 hard-coded
// here (WRIT-192). A type with no string/text scalar column at all — every
// declared type, immediately after ApplySchema installs an empty
// descriptor, or a type whose only text fields are collection- or
// keyed-lww-valued — contributes no clause; Objects falls through to
// "found nothing" for f.Text rather than referencing a table it has no
// column to search.
//
// Each type's own column terms are joined with joinBalanced rather than a
// flat OR chain (WRIT-285: a type with ~1000+ string columns breached
// SQLite's default expression-depth limit) — unconditionally, in both
// branches below, since the depth limit does not care how the LIKE pattern
// is bound.
//
// The LIKE pattern's binding style is conditional, though. Binding it once
// per column (a literal "?" in every column term) keeps the correlated
// EXISTS index-searchable ("SEARCH x EXISTS USING INDEX" in
// EXPLAIN QUERY PLAN) and is what this clause always did before WRIT-285.
// But summed across enough contributing types and columns, one bind per
// column approaches SQLITE_MAX_VARIABLE_NUMBER independently of the depth
// limit above, so past wideTextSearchBindThreshold contributing columns
// this instead binds the pattern once per type: a one-row derived table
// ("SELECT ? AS p") is cross-joined into the EXISTS subquery and every
// column term reads writ_text.p, so params below counts contributing types
// rather than columns in that case. That derived table is not free —
// EXPLAIN QUERY PLAN shows the planner losing the index search for a
// per-outer-row co-routine, a measured ~45% regression on the ordinary
// narrow-schema path (WRIT-285 round 1 finding 2, BenchmarkObjectsListWithFilter) — so it
// is worth paying only once the alternative is a hard SQL error, not on
// every call. Binding the derived table's pattern with a bare "?" rather
// than a numbered placeholder like ?1 keeps it positional: SQLite numbers a
// bare "?" one above the highest index already assigned, and this clause
// sits between the f.Type/f.Author args and the not-deleted/limit args at
// the Objects call site, so a numbered form would silently renumber those.
//
// Reads desc.queryShapes/queryOrder, not desc.types/order: those two stay
// nil on a name-only reopen (requireMaterializationPlan's guard depends on
// it — see schemaDescriptor's doc comment in ddl.go), but queryShapes is
// rehydrated from meta in exactly that state (descriptorFromPersisted), so
// this clause is correct whether desc came from a live buildDescriptor call
// or a warm reopen that has not run ApplySchema in this process yet
// (WRIT-192 round 2 MAJOR-1).
func objectsTextClause(desc *schemaDescriptor, restrictTypes []string) (clause string, params int) {
	if desc == nil {
		return "", 0
	}

	var allow map[string]bool
	if len(restrictTypes) > 0 {
		allow = make(map[string]bool, len(restrictTypes))
		for _, t := range restrictTypes {
			allow[t] = true
		}
	}

	type textContributor struct {
		table   string
		columns []string
	}
	var contributors []textContributor
	totalColumns := 0
	for _, objectType := range desc.queryOrder {
		if allow != nil && !allow[objectType] {
			continue
		}
		table, columns := objectTextColumns(desc.queryShapes[objectType])
		if len(columns) == 0 {
			continue
		}
		contributors = append(contributors, textContributor{table: table, columns: columns})
		totalColumns += len(columns)
	}
	if len(contributors) == 0 {
		return "", 0
	}

	wide := totalColumns > wideTextSearchBindThreshold

	var parts []string
	for _, c := range contributors {
		var colParts []string
		if wide {
			for _, col := range c.columns {
				colParts = append(colParts, "x."+col+" LIKE writ_text.p ESCAPE '\\'")
			}
			parts = append(parts, "EXISTS (SELECT 1 FROM "+quoteIdent(c.table)+" x, (SELECT ? AS p) writ_text WHERE x.object_id = o.object_id AND ("+joinBalanced(colParts, "OR")+"))")
			params++
		} else {
			for _, col := range c.columns {
				colParts = append(colParts, "x."+col+" LIKE ? ESCAPE '\\'")
			}
			parts = append(parts, "EXISTS (SELECT 1 FROM "+quoteIdent(c.table)+" x WHERE x.object_id = o.object_id AND ("+joinBalanced(colParts, "OR")+"))")
			params += len(c.columns)
		}
	}
	return "(" + joinBalanced(parts, "OR") + ")", params
}

// objectsNotDeletedClause builds Objects' default !IncludeDeleted filter
// directly from the installed schema descriptor: "no tombstone-strategy
// target folded true", over every declared type that has one (restricted to
// restrictTypes when non-empty, the same restriction objectsTextClause
// applies — the types f.Type itself already narrows the query to, so a
// clause for any other type would be dead weight), replacing the single
// built-in literal from earlier revisions (WRIT-192). A type with more than
// one tombstone-strategy target — the schema DSL does not forbid one — is
// excluded when any one of its tombstone targets folded true (the generated
// clause is an AND of "not deleted" per column, so a single deleted-true
// column fails it).
//
// Reads desc.queryShapes/queryOrder for the same reason objectsTextClause
// does — see its doc comment (WRIT-192 round 2 MAJOR-1).
//
// objectType is a schema-declared name, not a literal writ controls, so it
// is bound as a parameter (o.object_type != ?) rather than spliced into the
// clause text — the one schema-derived value that used to reach SQL as a
// literal (WRIT-253). params holds one entry per part, in the same order
// the clauses appear in the returned string, for the caller to append to
// its query args.
func objectsNotDeletedClause(desc *schemaDescriptor, restrictTypes []string) (clause string, params []any) {
	if desc == nil {
		return "", nil
	}

	var allow map[string]bool
	if len(restrictTypes) > 0 {
		allow = make(map[string]bool, len(restrictTypes))
		for _, t := range restrictTypes {
			allow[t] = true
		}
	}

	var parts []string
	for _, objectType := range desc.queryOrder {
		if allow != nil && !allow[objectType] {
			continue
		}
		shape := desc.queryShapes[objectType]
		var cols []string
		for _, target := range shape.Targets {
			if target.Strategy == "tombstone" {
				cols = append(cols, target.Column)
			}
		}
		if len(cols) == 0 {
			continue
		}
		sort.Strings(cols)

		var notDeleted []string
		for _, col := range cols {
			notDeleted = append(notDeleted, "(x."+col+" = 0 OR x."+col+" IS NULL)")
		}
		parts = append(parts, "(o.object_type != ? OR EXISTS (SELECT 1 FROM "+quoteIdent(shape.Table)+" x WHERE x.object_id = o.object_id AND "+joinBalanced(notDeleted, "AND")+"))")
		params = append(params, objectType)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return joinBalanced(parts, "AND"), params
}

// Objects executes a cross-type summary query over collaborative objects.
func (d *DB) Objects(f ObjectFilter) ([]ObjectResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}

	desc := d.descriptor()

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT o.object_id, o.object_type, o.op_count, ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at, o.verification ")
	sb.WriteString("FROM objects o WHERE 1=1")

	if len(f.Type) > 0 {
		sb.WriteString(" AND o.object_type IN (" + placeholders(len(f.Type)) + ")")
		for _, t := range f.Type {
			args = append(args, t)
		}
	}

	if len(f.Author) > 0 {
		sb.WriteString(" AND (o.author_email IN (" + placeholders(len(f.Author)) + ") OR o.author_name IN (" + placeholders(len(f.Author)) + "))")
		for _, a := range f.Author {
			args = append(args, a)
		}
		for _, a := range f.Author {
			args = append(args, a)
		}
	}

	if f.Text != "" {
		clause, params := objectsTextClause(desc, f.Type)
		if clause != "" {
			escaped := "%" + escapeLike(f.Text) + "%"
			sb.WriteString(" AND " + clause)
			for i := 0; i < params; i++ {
				args = append(args, escaped)
			}
		} else {
			// No declared type has a string/text scalar column to search
			// (restricted to f.Type, when set): nothing can match.
			sb.WriteString(" AND 0")
		}
	}

	if !f.IncludeDeleted {
		if clause, params := objectsNotDeletedClause(desc, f.Type); clause != "" {
			sb.WriteString(" AND " + clause)
			args = append(args, params...)
		}
	}

	switch f.OrderBy {
	case OrderByCreatedAtAsc:
		sb.WriteString(" ORDER BY o.created_at ASC, o.object_id ASC")
	case OrderByCreatedAtDesc:
		sb.WriteString(" ORDER BY o.created_at DESC, o.object_id DESC")
	case OrderByUpdatedAtAsc:
		sb.WriteString(" ORDER BY o.updated_at ASC, o.object_id ASC")
	case OrderByUpdatedAtDesc:
		sb.WriteString(" ORDER BY o.updated_at DESC, o.object_id DESC")
	default:
		sb.WriteString(" ORDER BY o.created_at ASC, o.object_id ASC")
	}

	appendLimitOffset(&sb, &args, f.Limit, f.Offset)

	rows, err := d.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query objects: %w", err)
	}
	defer rows.Close()

	var results []ObjectResult
	for rows.Next() {
		var or ObjectResult
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&or.ObjectID, &or.ObjectType, &or.OpCount,
			&or.Author.Name, &or.Author.Email, &createdAt, &updatedAt, &or.Verification,
		); err != nil {
			return nil, fmt.Errorf("projection: scan object: %w", err)
		}
		or.CreatedAt = time.Unix(createdAt, 0).UTC()
		or.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		results = append(results, or)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate objects: %w", err)
	}

	if results == nil {
		return []ObjectResult{}, nil
	}

	return results, nil
}

// Object fetches summary metadata for a single collaborative object by its ID, returning ErrNotFound if not found.
func (d *DB) Object(objectID string) (ObjectResult, error) {
	if d == nil || d.db == nil {
		return ObjectResult{}, fmt.Errorf("projection: database is closed")
	}
	if objectID == "" {
		return ObjectResult{}, ErrNotFound
	}

	var (
		res          ObjectResult
		createdAtSec int64
		updatedAtSec int64
	)

	err := d.db.QueryRow(`
		SELECT object_id, object_type, op_count, author_name, author_email, created_at, updated_at, verification
		FROM objects
		WHERE object_id = ?
	`, objectID).Scan(
		&res.ObjectID,
		&res.ObjectType,
		&res.OpCount,
		&res.Author.Name,
		&res.Author.Email,
		&createdAtSec,
		&updatedAtSec,
		&res.Verification,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return ObjectResult{}, ErrNotFound
		}
		return ObjectResult{}, fmt.Errorf("projection: query object %s: %w", objectID, err)
	}

	res.CreatedAt = time.Unix(createdAtSec, 0).UTC()
	res.UpdatedAt = time.Unix(updatedAtSec, 0).UTC()

	return res, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func appendLimitOffset(sb *strings.Builder, args *[]any, limit, offset int) {
	if limit > 0 && offset > 0 {
		sb.WriteString(" LIMIT ? OFFSET ?")
		*args = append(*args, limit, offset)
	} else if limit > 0 {
		sb.WriteString(" LIMIT ?")
		*args = append(*args, limit)
	} else if offset > 0 {
		sb.WriteString(" LIMIT -1 OFFSET ?")
		*args = append(*args, offset)
	}
}

// Frontier returns the observed frontier of op commits for the given object ID:
// the op commits with no child dependencies within that object.
func (d *DB) Frontier(objectID string) ([]string, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}
	if objectID == "" {
		return nil, nil
	}

	rows, err := d.db.Query("SELECT op_id, parents FROM ops WHERE object_id = ? ORDER BY op_id ASC", objectID)
	if err != nil {
		return nil, fmt.Errorf("projection: query ops for frontier %s: %w", objectID, err)
	}
	defer rows.Close()

	allOps := make(map[string]bool)
	hasChildren := make(map[string]bool)

	for rows.Next() {
		var opID, parentsJSON string
		if err := rows.Scan(&opID, &parentsJSON); err != nil {
			return nil, fmt.Errorf("projection: scan op for frontier: %w", err)
		}
		allOps[opID] = true
		if len(parentsJSON) > 0 {
			var parents []string
			if err := json.Unmarshal([]byte(parentsJSON), &parents); err == nil {
				for _, p := range parents {
					hasChildren[p] = true
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate ops for frontier: %w", err)
	}

	var frontier []string
	for opID := range allOps {
		if !hasChildren[opID] {
			frontier = append(frontier, opID)
		}
	}
	sort.Strings(frontier)

	return frontier, nil
}
