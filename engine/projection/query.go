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
	LastOpID   string    `json:"last_op_id"`
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

	var parts []string
	for _, objectType := range desc.queryOrder {
		if allow != nil && !allow[objectType] {
			continue
		}
		table, columns := objectTextColumns(desc.queryShapes[objectType])
		if len(columns) == 0 {
			continue
		}

		var colParts []string
		for _, col := range columns {
			colParts = append(colParts, "x."+col+" LIKE ? ESCAPE '\\'")
		}
		parts = append(parts, "EXISTS (SELECT 1 FROM "+table+" x WHERE x.object_id = o.object_id AND ("+strings.Join(colParts, " OR ")+"))")
		params += len(columns)
	}
	if len(parts) == 0 {
		return "", 0
	}
	return "(" + strings.Join(parts, " OR ") + ")", params
}

// objectsNotDeletedClause builds Objects' default !IncludeDeleted filter
// directly from the installed schema descriptor: "no tombstone-strategy
// target folded true", over every declared type that has one (restricted to
// restrictTypes when non-empty, the same restriction objectsTextClause
// applies — the types f.Type itself already narrows the query to, so a
// clause for any other type would be dead weight), replacing the single
// built-in literal (o_comment.f_deleted) WRIT-189 round 3 MAJOR-2
// hard-coded here (WRIT-192). A type with more than one tombstone-strategy
// target — the schema DSL does not forbid one — is excluded when any one
// of its tombstone targets folded true (the generated clause is an AND of
// "not deleted" per column, so a single deleted-true column fails it).
//
// Reads desc.queryShapes/queryOrder for the same reason objectsTextClause
// does — see its doc comment (WRIT-192 round 2 MAJOR-1).
func objectsNotDeletedClause(desc *schemaDescriptor, restrictTypes []string) string {
	if desc == nil {
		return ""
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
		parts = append(parts, "(o.object_type != '"+objectType+"' OR EXISTS (SELECT 1 FROM "+shape.Table+" x WHERE x.object_id = o.object_id AND "+strings.Join(notDeleted, " AND ")+"))")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " AND ")
}

// Objects executes a cross-type summary query over collaborative objects.
func (d *DB) Objects(f ObjectFilter) ([]ObjectResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}

	desc := d.descriptor()

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT o.object_id, o.object_type, o.op_count, o.last_op_id, ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at ")
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
		if clause := objectsNotDeletedClause(desc, f.Type); clause != "" {
			sb.WriteString(" AND " + clause)
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
			&or.ObjectID, &or.ObjectType, &or.OpCount, &or.LastOpID,
			&or.Author.Name, &or.Author.Email, &createdAt, &updatedAt,
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
		SELECT object_id, object_type, op_count, last_op_id, author_name, author_email, created_at, updated_at
		FROM objects
		WHERE object_id = ?
	`, objectID).Scan(
		&res.ObjectID,
		&res.ObjectType,
		&res.OpCount,
		&res.LastOpID,
		&res.Author.Name,
		&res.Author.Email,
		&createdAtSec,
		&updatedAtSec,
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
