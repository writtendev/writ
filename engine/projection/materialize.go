package projection

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/resolve"
	"github.com/writtendev/writ/engine/state"
)

// materializeObject folds ops for a single collaborative object against
// desc's rule index and writes the generic, schema-generated tables inside
// tx. It never switches on a Go object-type name: every table and column it
// writes into comes from desc, which is itself built purely from the rule
// index (ddl.go) — the same generic path an object of a type nobody has
// heard of yet, freshly declared through writ.schema, takes on its very
// first materialize.
func materializeObject(tx *sql.Tx, desc *schemaDescriptor, objectID string, ops []codec.Op) error {
	if err := deleteObjectState(tx, desc, objectID); err != nil {
		return err
	}

	if len(ops) == 0 {
		return nil
	}

	orderedOps, err := dag.Order(ops)
	if err != nil {
		return fmt.Errorf("projection: order ops for object %s: %w", objectID, err)
	}

	firstOp := orderedOps[0]
	lastOp := orderedOps[len(orderedOps)-1]
	authorName := firstOp.Author.Name
	authorEmail := firstOp.Author.Email
	createdAt := firstOp.Author.When.UTC().Unix()
	updatedAt := lastOp.Author.When.UTC().Unix()
	lastOpID := lastOp.ID
	objectType := determineObjectType(orderedOps)

	if _, err := tx.Exec(
		"INSERT INTO objects (object_id, object_type, op_count, last_op_id, author_name, author_email, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		objectID, objectType, len(ops), lastOpID, authorName, authorEmail, createdAt, updatedAt,
	); err != nil {
		return fmt.Errorf("projection: insert object %s: %w", objectID, err)
	}

	var td *typeDescriptor
	if desc != nil {
		td = desc.types[objectType]
	}

	if td == nil {
		// Undeclared type (no schema, built-in or log-declared, installs any
		// rules for it), or a type withheld by ddl.go for an invalid target
		// or keyed-lww key component: every op is preserved verbatim in
		// unknown_ops, exactly as the absent-schema path always has.
		return insertUnknownOps(tx, objectID, ops)
	}

	rules := desc.rulesByType[objectType]
	folded, err := state.Fold(ops, rules)
	if err != nil {
		return fmt.Errorf("projection: fold %s %s: %w", objectType, objectID, err)
	}

	unknownFields := computeUnknownFields(orderedOps, rules, folded.UnknownOps, td.WithheldTargets)
	if err := writeTypeRow(tx, td, objectID, folded.State, rules, orderedOps, unknownFields, folded.UnknownOps); err != nil {
		return err
	}

	for i, u := range folded.UnknownOps {
		if _, err := tx.Exec(
			"INSERT OR REPLACE INTO unknown_ops (object_id, op_id, object_type, op_type, op_version, op_index) VALUES (?, ?, ?, ?, ?, ?)",
			objectID, u.Commit, u.ObjectType, u.OpType, u.OpVersion, i,
		); err != nil {
			return fmt.Errorf("projection: insert unknown op %s: %w", u.Commit, err)
		}
	}

	return nil
}

func insertUnknownOps(tx *sql.Tx, objectID string, ops []codec.Op) error {
	for i, op := range ops {
		if _, err := tx.Exec(
			"INSERT OR REPLACE INTO unknown_ops (object_id, op_id, object_type, op_type, op_version, op_index) VALUES (?, ?, ?, ?, ?, ?)",
			objectID, op.ID, op.ObjectType, op.OpType, op.OpVersion, i,
		); err != nil {
			return fmt.Errorf("projection: insert unreduced op %s: %w", op.ID, err)
		}
	}
	return nil
}

// writeTypeRow inserts td's type-table row (scalar and position-companion
// columns, plus unknown_fields), every child-table row a collection or
// keyed-lww target folded to, and every append-group row (unknownOps is the
// object's quarantined ops, needed only to skip them when writing those).
func writeTypeRow(tx *sql.Tx, td *typeDescriptor, objectID string, folded map[string]any, rules []state.Rule, orderedOps []codec.Op, unknownFields string, unknownOps []state.UnknownOp) error {
	cols := []string{"object_id"}
	vals := []any{objectID}

	// group.keyed-lww targets by their shared group table so every group
	// row (one per distinct key tuple) is built once, from every member
	// target's own folded entries, rather than once per target.
	type groupBuild struct {
		table      *ddlTable
		keyColumns []string
		rows       map[string]map[string]any // key-tuple string -> {column: value}
		keyValues  map[string][]string
	}
	groups := make(map[string]*groupBuild)

	for _, tk := range sortedTargetKeys(td) {
		plan := td.Targets[tk]
		val, has := folded[tk]

		switch {
		case plan.Column != "":
			cols = append(cols, plan.Column)
			if has {
				vals = append(vals, columnValue(plan.ValueType, val))
			} else {
				vals = append(vals, nil)
			}
			if plan.PosOpIDColumn != "" {
				cols = append(cols, plan.PosOpIDColumn)
				if has {
					vals = append(vals, positionOpID(orderedOps, td.ObjectType, tk, rules))
				} else {
					vals = append(vals, nil)
				}
			}
			if plan.MembersTable != "" && has {
				if err := writeMembersRows(tx, plan.MembersTable, objectID, val); err != nil {
					return err
				}
			}

		case plan.ChildTable != "":
			if !has {
				continue
			}
			if err := writeChildRows(tx, plan, objectID, val); err != nil {
				return err
			}

		case plan.GroupTable != "":
			if !has {
				continue
			}
			g, ok := groups[plan.GroupTable]
			if !ok {
				var table *ddlTable
				for i := range td.Children {
					if td.Children[i].Name == plan.GroupTable {
						table = &td.Children[i]
						break
					}
				}
				g = &groupBuild{table: table, keyColumns: plan.KeyColumns, rows: make(map[string]map[string]any), keyValues: make(map[string][]string)}
				groups[plan.GroupTable] = g
			}
			entries, _ := val.([]any)
			for _, e := range entries {
				m, ok := e.(map[string]any)
				if !ok {
					continue
				}
				keyArr, _ := m["key"].([]string)
				ks := strings.Join(keyArr, "\x00")
				if g.rows[ks] == nil {
					g.rows[ks] = make(map[string]any)
					g.keyValues[ks] = keyArr
				}
				g.rows[ks][plan.ValueColumn] = columnValue(plan.ValueType, m["value"])
			}
		}
	}

	cols = append(cols, "unknown_fields")
	if unknownFields == "" {
		vals = append(vals, nil)
	} else {
		vals = append(vals, unknownFields)
	}

	qmarks := strings.Repeat("?, ", len(cols)-1) + "?"
	insertSQL := "INSERT INTO " + td.Table.Name + " (" + strings.Join(cols, ", ") + ") VALUES (" + qmarks + ")"
	if _, err := tx.Exec(insertSQL, vals...); err != nil {
		return fmt.Errorf("projection: insert %s row %s: %w", td.Table.Name, objectID, err)
	}

	var groupNames []string
	for name := range groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, name := range groupNames {
		g := groups[name]
		if g.table == nil {
			continue
		}
		var keyTuples []string
		for ks := range g.rows {
			keyTuples = append(keyTuples, ks)
		}
		sort.Strings(keyTuples)
		for _, ks := range keyTuples {
			row := g.rows[ks]
			keyVals := g.keyValues[ks]
			gCols := []string{"object_id"}
			gVals := []any{objectID}
			for i, kc := range g.keyColumns {
				gCols = append(gCols, kc)
				if i < len(keyVals) {
					gVals = append(gVals, keyVals[i])
				} else {
					gVals = append(gVals, "")
				}
			}
			var memberCols []string
			for _, c := range g.table.Columns {
				if strings.HasPrefix(c.Name, "f_") {
					memberCols = append(memberCols, c.Name)
				}
			}
			for _, c := range memberCols {
				gCols = append(gCols, c)
				gVals = append(gVals, row[c])
			}
			ph := strings.Repeat("?, ", len(gCols)-1) + "?"
			insertSQL := "INSERT INTO " + g.table.Name + " (" + strings.Join(gCols, ", ") + ") VALUES (" + ph + ")"
			if _, err := tx.Exec(insertSQL, gVals...); err != nil {
				return fmt.Errorf("projection: insert %s row: %w", g.table.Name, err)
			}
		}
	}

	if len(td.AppendGroups) > 0 {
		skip := make(map[string]bool, len(unknownOps))
		for _, u := range unknownOps {
			skip[u.Commit] = true
		}
		for _, ag := range td.AppendGroups {
			if err := writeAppendGroupRows(tx, ag, objectID, orderedOps, skip); err != nil {
				return err
			}
		}
	}

	return nil
}

func sortedTargetKeys(td *typeDescriptor) []string {
	keys := make([]string, 0, len(td.Targets))
	for k := range td.Targets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writeChildRows writes one row per element of a set-union,
// set-observed-remove, or multi-value target's folded value into its child
// table. multi-value folds to an ordered list ([]any); set-union and
// set-observed-remove fold to a sorted []string. multi-value's folded value
// is a bare string, not a list, when settled (one maximal write) — the
// reader derives `conflicted` as row-count > 1 and the settled body as the
// sole row, so a settled write still needs exactly one row here. An append
// target carries no plan at all (see appendGroupPlan and
// writeAppendGroupRows below) and so never reaches this function.
func writeChildRows(tx *sql.Tx, plan *targetPlan, objectID string, val any) error {
	var items []any
	switch v := val.(type) {
	case []string:
		for _, s := range v {
			items = append(items, s)
		}
	case []any:
		items = v
	case string:
		items = []any{v}
	default:
		return nil
	}

	for i, it := range items {
		converted := columnValue(plan.ValueType, it)
		var err error
		if plan.ChildKind == "idx" {
			_, err = tx.Exec("INSERT INTO "+plan.ChildTable+" (object_id, idx, value) VALUES (?, ?, ?)", objectID, i, converted)
		} else {
			_, err = tx.Exec("INSERT INTO "+plan.ChildTable+" (object_id, item) VALUES (?, ?)", objectID, converted)
		}
		if err != nil {
			return fmt.Errorf("projection: insert %s row for %s: %w", plan.ChildTable, objectID, err)
		}
	}
	return nil
}

// writeAppendGroupRows writes one row per op that matches any of an append
// group's envelopes and isn't already quarantined into unknown_ops, in the
// object's total order — the same op-level filter state.Fold and the typed
// review reducer both apply via fold.Uninterpretable against the same
// rules, so a wholly rejected op contributes no row here either. Each row
// carries one column per grouped target, NULL where that particular op's
// body omitted the field: the row is the pairing, fixed once at write time,
// rather than something a reader reconstructs by zipping
// independently-ordered per-field lists back together (WRIT-189 round 2
// MAJOR-1).
//
// A target declared under more than one envelope (a version bump, or a
// second op_type agreeing on every merge attribute, spec/schema-ops.md §8)
// has to accept rows from every one of them, not just one representative,
// to match what state.Fold's own target-keyed accumulator accumulates
// (WRIT-189 round 3 MAJOR-1) — ag.Envelopes (built purely from the rule
// index in ddl.go, via buildAppendGroups) is the full set.
//
// This never inspects td, rules, or any field name: ag.Envelopes and
// ag.Members are its only inputs besides the raw ops, so it materializes an
// envelope declared by an arbitrary log schema exactly as it does review's
// built-in revision push.
func writeAppendGroupRows(tx *sql.Tx, ag appendGroupPlan, objectID string, orderedOps []codec.Op, skip map[string]bool) error {
	idx := 0
	for _, op := range orderedOps {
		if skip[op.ID] {
			continue
		}
		if !appendGroupEnvelopeMatches(ag.Envelopes, op) {
			continue
		}

		var body map[string]any
		if len(op.Body) > 0 {
			if err := json.Unmarshal(op.Body, &body); err != nil {
				return fmt.Errorf("projection: unmarshal op %s body for %s: %w", op.ID, ag.Table, err)
			}
		}

		cols := make([]string, 0, len(ag.Members)+2)
		vals := make([]any, 0, len(ag.Members)+2)
		cols = append(cols, "object_id", "idx")
		vals = append(vals, objectID, idx)
		for _, m := range ag.Members {
			cols = append(cols, m.Column)
			// A member's body key is per-envelope, not the target key
			// itself: two envelopes can reach the same target through two
			// different field names (a version bump, or a second op_type
			// agreeing on every merge attribute, spec/schema-ops.md §8), and
			// reading body[m.Key] — the target key — silently NULLs every
			// row whenever a rule declares target(...) distinct from field,
			// with the value landing in neither unknown_fields nor
			// unknown_ops (WRIT-189 round 4 MAJOR-1). fieldForOp resolves
			// the field this op's own envelope actually uses, mirroring
			// state.Fold's per-op rule dispatch (engine/internal/fold).
			field, ok := fieldForOp(m.Fields, op)
			if !ok {
				vals = append(vals, nil)
				continue
			}
			// Presence alone gates a write, matching the fold (spec/fold.md
			// §5.1's empty-scalar contract, mirrored by every accumulator
			// including append's): an explicit JSON null is a written nil,
			// not the same as the field never appearing in the body. Both
			// still land as SQL NULL here — columnValue itself already
			// treats a nil value as NULL for every value_type — but the
			// gate is "was it present", not "was it present and non-null"
			// (WRIT-189 round 3 MINOR-5).
			if v, ok := body[field]; ok {
				vals = append(vals, columnValue(m.ValueType, v))
			} else {
				vals = append(vals, nil)
			}
		}

		ph := strings.Repeat("?, ", len(cols)-1) + "?"
		if _, err := tx.Exec("INSERT INTO "+ag.Table+" ("+strings.Join(cols, ", ")+") VALUES ("+ph+")", vals...); err != nil {
			return fmt.Errorf("projection: insert %s row for %s: %w", ag.Table, objectID, err)
		}
		idx++
	}
	return nil
}

// appendGroupEnvelopeMatches reports whether op matches at least one of
// envelopes — the same op_type/op_version wildcard semantics
// opMatchesRuleLite applies for a single rule, applied here across every
// envelope an append group's members are declared under (WRIT-189 round 3
// MAJOR-1: a target declared under two envelopes must accept ops from
// either, not just whichever one buildTypeDescriptor picked as its
// representative rule).
func appendGroupEnvelopeMatches(envelopes []appendGroupEnvelope, op codec.Op) bool {
	for _, e := range envelopes {
		if e.OpType != op.OpType {
			continue
		}
		if e.OpVersion != 0 && op.OpVersion != 0 && op.OpVersion != e.OpVersion {
			continue
		}
		return true
	}
	return false
}

// fieldForOp returns the body key op's own envelope reads a member's value
// from: the Field of the one appendGroupFieldSource among fields whose
// (op_type, op_version) matches op. false means no rule declaring this
// member was declared under an envelope op matches, so it takes no value
// from op at all — distinct from the field being merely absent from op's
// body, which still yields a written NULL below.
//
// This is a plain first-match scan, and first-match is only safe because
// fields cannot contain two entries sharing one exact (op_type, op_version)
// envelope with two different Field values: ddl.go detects that shape while
// building fields (the loop building appendGroupMember.Fields) and withholds
// that one target alone — no column, recorded in WithheldTargets with its
// fields routed to unknown_fields — before a typeDescriptor carrying it is
// ever produced (WRIT-189 round 5 MAJOR-1, rescoped from a whole-type
// withhold by WRIT-201, then rescoped again from a whole-group withhold to
// this single-target scope by WRIT-201 review round 2 MEDIUM-1: state.Fold
// appends both fields' entries, which one row per op cannot hold, but a
// group-mate reached by one field per envelope is unaffected and keeps its
// column). An earlier version of this comment claimed
// first-match "reproduces" state.Fold's own per-op rule dispatch
// (engine/internal/fold) — that was false independent of ordering: fold's
// matchedRulesByField admits a rule only if some op in the object's history
// actually writes that rule's Field, while fields here is unfiltered, so
// first-match could select a field the fold would never have considered.
// Withholding the ambiguous shape at the source, rather than trying to
// mirror fold's admission logic here, is what makes fields safe to scan in
// order at all: with the ambiguous shape gone, at most one entry can ever
// match a given op's exact envelope, so which one is "first" no longer
// matters.
func fieldForOp(fields []appendGroupFieldSource, op codec.Op) (string, bool) {
	for _, f := range fields {
		if f.OpType != op.OpType {
			continue
		}
		if f.OpVersion != 0 && op.OpVersion != 0 && f.OpVersion != op.OpVersion {
			continue
		}
		return f.Field, true
	}
	return "", false
}

// writeMembersRows populates a target's generic members table from its
// folded value, when that value decodes to a JSON object: one row per
// top-level key, so a reader can filter two members of the same object
// together via two indexed EXISTS lookups instead of an unindexed scan.
// A value that is not object-shaped (or fails to decode) writes nothing —
// the members table stays a pure performance index, never a second source
// of truth for the scalar column, which already holds the value verbatim.
func writeMembersRows(tx *sql.Tx, table, objectID string, raw any) error {
	var obj map[string]any
	switch v := raw.(type) {
	case json.RawMessage:
		if err := json.Unmarshal(v, &obj); err != nil {
			return nil
		}
	case []byte:
		if err := json.Unmarshal(v, &obj); err != nil {
			return nil
		}
	case map[string]any:
		obj = v
	default:
		return nil
	}
	if len(obj) == 0 {
		return nil
	}

	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		val := toText(obj[k])
		if _, err := tx.Exec("INSERT INTO "+table+" (object_id, member, value) VALUES (?, ?, ?)", objectID, k, val); err != nil {
			return fmt.Errorf("projection: insert %s row for %s: %w", table, objectID, err)
		}
	}
	return nil
}

// columnValue converts a folded Go value into the form its declared
// value_type's SQL column expects: int and number to their numeric SQLite
// affinities, bool to 0/1, and everything else — including an untyped
// value, which for create-once arrives as raw JSON bytes and for anything
// else falls back to a defensive re-marshal — to text.
//
// create-once always hands back raw JSON bytes verbatim regardless of
// value_type (byte-exact preservation for every non-normalizing value,
// spec/value-types.md), so a create-once string, int, bool, or any other
// typed field arrives here as json.RawMessage holding e.g. `"c-reply-1"` —
// quotes included — not the Go string "c-reply-1". Every value_type except
// the truly untyped one (value_type == "", where the raw bytes are the
// point: an arbitrary JSON object like comment.subject, preserved verbatim
// including unknown members and key order) decodes those bytes back into a
// native Go value before the switch below runs, so a create-once column
// reads back exactly as an lww column of the same value_type would.
func columnValue(valueType string, v any) any {
	if v == nil {
		return nil
	}
	if raw, ok := rawJSONBytes(v); ok {
		if valueType == "" {
			return toText(v)
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			v = decoded
		}
	}
	switch valueType {
	case "int":
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int64:
			return n
		case int:
			return int64(n)
		}
		return nil
	case "number":
		switch n := v.(type) {
		case float64:
			return n
		case int64:
			return float64(n)
		case int:
			return float64(n)
		}
		return nil
	case "bool":
		if b, ok := v.(bool); ok {
			if b {
				return 1
			}
			return 0
		}
		return nil
	default:
		return toText(v)
	}
}

// rawJSONBytes reports whether v is the raw-bytes shape create-once's
// accumulator produces (json.RawMessage, or a bare []byte for defensive
// measure), returning the bytes.
func rawJSONBytes(v any) ([]byte, bool) {
	switch t := v.(type) {
	case json.RawMessage:
		return []byte(t), true
	case []byte:
		return t, true
	}
	return nil, false
}

// toText renders v as the string a TEXT column stores. create-once's raw
// bytes (json.RawMessage, for an untyped target such as comment.subject)
// pass through verbatim — the exact bytes a producer wrote, unknown members
// and key order included — rather than being decoded and re-marshaled.
func toText(v any) any {
	switch t := v.(type) {
	case string:
		return t
	case json.RawMessage:
		return string(t)
	case []byte:
		return string(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(b)
	}
}

// opMatchesRuleLite mirrors engine/internal/fold's unexported opMatchesRule:
// object_type, op_type and op_version filters, empty meaning "matches
// anything" on either side. Position op-id derivation and unknown-field
// detection both need this and neither can reach the internal fold package
// (it is not on the projection's import allowlist), so it is reproduced
// here rather than exported solely for this.
func opMatchesRuleLite(op codec.Op, r state.Rule) bool {
	if r.OpType != "" && r.OpType != op.OpType {
		return false
	}
	if r.OpVersion != 0 && op.OpVersion != 0 && r.OpVersion != op.OpVersion {
		return false
	}
	if r.ObjectType != "" && op.ObjectType != "" && r.ObjectType != op.ObjectType {
		return false
	}
	return true
}

// positionOpID finds, among ops matching a rule bound to targetKey, the id
// of the last op in the total order that carried a string at that rule's
// field — the id ORDER BY position ASC, ..._op_id ASC tiebreaks on. One
// generic helper replaces the three near-identical per-type ones
// (issuePositionOpID, sectionPositionOpID, workflowStatePositionOpID) that
// existed before every position target got this companion column
// mechanically. This is a projection-side derivation over ops the fold
// already ordered: no fold change, no I/O.
func positionOpID(orderedOps []codec.Op, objectType, targetKey string, rules []state.Rule) string {
	var posOpID string
	for _, op := range orderedOps {
		for _, r := range rules {
			if r.TargetKey() != targetKey || r.ValueType != "position" {
				continue
			}
			if !opMatchesRuleLite(op, r) {
				continue
			}
			var body map[string]any
			if len(op.Body) > 0 {
				if err := json.Unmarshal(op.Body, &body); err != nil {
					continue
				}
			}
			if _, ok := body[r.Field].(string); ok {
				posOpID = op.ID
			}
			break
		}
	}
	return posOpID
}

// computeUnknownFields scans every op whose (op_type, op_version) matched at
// least one installed rule — an op already fully quarantined in unknownOps
// contributes nothing here, it is tracked there instead — for body keys no
// rule bound to that op's (op_type, op_version) names as its Field,
// last-write-wins per key over the total order. Before WRIT-195 this was
// the generic expression of the (now-deleted) typed settings reducer's
// unknown_keys collection — the same semantics, computed once for every
// declared type instead of one type's hand-written case; that hand-written
// case is gone, so this is now simply the one implementation.
//
// withheldTargets are target keys the descriptor declined to give a column
// (ddl.go's append-group loop, WRIT-201): a rule bound to one of them still
// matched, so the op is not quarantined, but its field has nowhere to land
// in SQL. It counts as unknown here rather than being dropped, which is what
// spec/forward-compatibility.md §Targets a projection declines requires.
//
// That routing stops where this map does, and deliberately: a withheld append
// target is an accumulator, but result is a per-key register, so a target
// written by several ops keeps only the latest write per body field. The
// entries the fold accumulated are complete only through the fold, never
// through this column, and spec/forward-compatibility.md §Targets a
// projection declines says exactly that rather than promising more.
// Accumulating here instead would give one JSON blob two different
// semantics — a register for a genuinely unknown field, a list for a
// withheld target — that no consumer can tell apart without the schema, and
// it still could not reproduce the fold's value, whose entries interleave
// across the targets' fields in canonical rule order. The shape that does
// hold them is the row-per-entry table redesign the append-group loop
// defers, not a second meaning for this column.
func computeUnknownFields(orderedOps []codec.Op, rules []state.Rule, unknownOps []state.UnknownOp, withheldTargets map[string]bool) string {
	skip := make(map[string]bool, len(unknownOps))
	for _, u := range unknownOps {
		skip[u.Commit] = true
	}

	result := make(map[string]any)
	for _, op := range orderedOps {
		if skip[op.ID] {
			continue
		}
		var matchedAny bool
		knownFields := make(map[string]bool)
		for _, r := range rules {
			if !opMatchesRuleLite(op, r) {
				continue
			}
			matchedAny = true
			if withheldTargets[r.TargetKey()] {
				continue
			}
			knownFields[r.Field] = true
		}
		if !matchedAny || len(op.Body) == 0 {
			continue
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(op.Body, &body); err != nil {
			continue
		}
		for k, raw := range body {
			if knownFields[k] {
				continue
			}
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				continue
			}
			result[k] = v
		}
	}

	if len(result) == 0 {
		return ""
	}
	b, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return string(b)
}

// determineObjectType determines the object type from an ops slice,
// prioritizing create ops with non-empty ObjectType, then the first
// non-empty ObjectType, then ops[0].ObjectType if non-empty, else "".
func determineObjectType(ops []codec.Op) string {
	for _, op := range ops {
		if op.OpType == "create" && op.ObjectType != "" {
			return op.ObjectType
		}
	}
	for _, op := range ops {
		if op.ObjectType != "" {
			return op.ObjectType
		}
	}
	if len(ops) > 0 {
		return ops[0].ObjectType
	}
	return ""
}

// deleteObjectState removes objectID's row from substrate (objects,
// unknown_ops, anchor_resolutions) and, if a prior materialization recorded
// a type for it that desc still declares, from that type's own generated
// tables. An object's type is not expected to change across
// re-materializations; if the prior type's tables no longer exist in desc
// (the type was dropped from the schema), TestSchemaShrinkMovesOpsToUnknownOps
// covers that path — ApplySchema itself already dropped those tables, so
// there is nothing here left to clean.
func deleteObjectState(tx *sql.Tx, desc *schemaDescriptor, objectID string) error {
	var priorType sql.NullString
	_ = tx.QueryRow("SELECT object_type FROM objects WHERE object_id = ?", objectID).Scan(&priorType)

	if priorType.Valid && desc != nil {
		if td, ok := desc.types[priorType.String]; ok {
			if _, err := tx.Exec("DELETE FROM "+td.Table.Name+" WHERE object_id = ?", objectID); err != nil {
				return fmt.Errorf("projection: delete %s row (%s): %w", td.Table.Name, objectID, err)
			}
			for _, child := range td.Children {
				if _, err := tx.Exec("DELETE FROM "+child.Name+" WHERE object_id = ?", objectID); err != nil {
					return fmt.Errorf("projection: delete %s rows (%s): %w", child.Name, objectID, err)
				}
			}
		}
	}

	if _, err := tx.Exec("DELETE FROM objects WHERE object_id = ?", objectID); err != nil {
		return fmt.Errorf("projection: delete object state (objects, %s): %w", objectID, err)
	}
	if _, err := tx.Exec("DELETE FROM unknown_ops WHERE object_id = ?", objectID); err != nil {
		return fmt.Errorf("projection: delete object state (unknown_ops, %s): %w", objectID, err)
	}
	if _, err := tx.Exec("DELETE FROM anchor_resolutions WHERE object_id = ?", objectID); err != nil {
		return fmt.Errorf("projection: delete object state (anchor_resolutions, %s): %w", objectID, err)
	}
	return nil
}

type commentToResolve struct {
	objectID   string
	target     string
	anchorJSON string
}

// materializeAnchors resolves comment anchors against current target commits
// in code_tips. Which columns to read anchors from is driven by
// desc.anchorColumns — every scalar target whose declared value_type is
// "anchor" — rather than a hard-coded read of comments.anchor: comment's
// create/anchor is simply the one case in the shipped vocabulary that
// declares value_type "anchor" today.
func materializeAnchors(tx *sql.Tx, desc *schemaDescriptor, s storage.Storer) (int, error) {
	if _, err := tx.Exec("DELETE FROM anchor_resolutions WHERE target_commit NOT IN (SELECT tip FROM code_tips)"); err != nil {
		return 0, fmt.Errorf("projection: prune stale anchor resolutions: %w", err)
	}

	rows, err := tx.Query("SELECT DISTINCT tip FROM code_tips WHERE tip != ''")
	if err != nil {
		return 0, fmt.Errorf("projection: query code_tips: %w", err)
	}
	var targetCommits []string
	for rows.Next() {
		var tip string
		if err := rows.Scan(&tip); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("projection: scan code_tips: %w", err)
		}
		targetCommits = append(targetCommits, tip)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("projection: iterate code_tips: %w", err)
	}
	if len(targetCommits) == 0 || desc == nil {
		return 0, nil
	}

	var comments []commentToResolve
	for _, ref := range desc.anchorColumns {
		cRows, err := tx.Query("SELECT object_id, " + ref.Column + " FROM " + ref.Table + " WHERE " + ref.Column + " IS NOT NULL AND " + ref.Column + " != '' AND " + ref.Column + " != 'null'")
		if err != nil {
			return 0, fmt.Errorf("projection: query anchors from %s.%s: %w", ref.Table, ref.Column, err)
		}
		for cRows.Next() {
			var c commentToResolve
			if err := cRows.Scan(&c.objectID, &c.anchorJSON); err != nil {
				_ = cRows.Close()
				return 0, fmt.Errorf("projection: scan anchor from %s.%s: %w", ref.Table, ref.Column, err)
			}
			c.target = ref.Target
			comments = append(comments, c)
		}
		_ = cRows.Close()
		if err := cRows.Err(); err != nil {
			return 0, fmt.Errorf("projection: iterate anchors from %s.%s: %w", ref.Table, ref.Column, err)
		}
	}
	if len(comments) == 0 {
		return 0, nil
	}

	treeCache := make(map[string]*resolve.Tree)
	resolvedCount := 0

	for _, targetCommit := range targetCommits {
		resRows, err := tx.Query("SELECT DISTINCT object_id FROM anchor_resolutions WHERE target_commit = ?", targetCommit)
		if err != nil {
			return resolvedCount, fmt.Errorf("projection: query existing resolutions: %w", err)
		}
		existing := make(map[string]bool)
		for resRows.Next() {
			var objID string
			if err := resRows.Scan(&objID); err != nil {
				_ = resRows.Close()
				return resolvedCount, fmt.Errorf("projection: scan existing resolution: %w", err)
			}
			existing[objID] = true
		}
		if err := resRows.Err(); err != nil {
			return resolvedCount, fmt.Errorf("projection: iterate existing resolutions: %w", err)
		}

		for _, comm := range comments {
			if existing[comm.objectID] {
				continue
			}

			targetTree, ok := treeCache[targetCommit]
			if !ok {
				treeFiles, err := materializeCommitTree(s, targetCommit)
				if err != nil {
					return resolvedCount, fmt.Errorf("projection: materialize tree %s: %w", targetCommit, err)
				}
				algo := resolve.SHA1
				if len(targetCommit) == 64 {
					algo = resolve.SHA256
				}
				targetTree = resolve.NewTree(treeFiles, algo)
				treeCache[targetCommit] = targetTree
			}

			anchor, err := resolve.ParseAnchor([]byte(comm.anchorJSON))
			if err != nil {
				return resolvedCount, fmt.Errorf("projection: parse anchor for comment %s: %w", comm.objectID, err)
			}

			res := resolve.Resolve(anchor, targetTree)

			if res.Old != nil {
				startLine, endLine := 0, 0
				if res.Old.Range != nil {
					startLine = res.Old.Range.Start
					endLine = res.Old.Range.End
				}
				_, err = tx.Exec(
					"INSERT OR REPLACE INTO anchor_resolutions (object_id, target, target_commit, side, outcome, match, path, start_line, end_line, reason) VALUES (?, ?, ?, 'old', ?, ?, ?, ?, ?, ?)",
					comm.objectID, comm.target, targetCommit, res.Old.Outcome, res.Old.Match, res.Old.Path, startLine, endLine, res.Old.Reason,
				)
				if err != nil {
					return resolvedCount, fmt.Errorf("projection: insert anchor resolution old (%s, %s): %w", comm.objectID, targetCommit, err)
				}
				resolvedCount++
			}

			if res.New != nil {
				startLine, endLine := 0, 0
				if res.New.Range != nil {
					startLine = res.New.Range.Start
					endLine = res.New.Range.End
				}
				_, err = tx.Exec(
					"INSERT OR REPLACE INTO anchor_resolutions (object_id, target, target_commit, side, outcome, match, path, start_line, end_line, reason) VALUES (?, ?, ?, 'new', ?, ?, ?, ?, ?, ?)",
					comm.objectID, comm.target, targetCommit, res.New.Outcome, res.New.Match, res.New.Path, startLine, endLine, res.New.Reason,
				)
				if err != nil {
					return resolvedCount, fmt.Errorf("projection: insert anchor resolution new (%s, %s): %w", comm.objectID, targetCommit, err)
				}
				resolvedCount++
			}
		}
	}

	return resolvedCount, nil
}

func materializeCommitTree(s storage.Storer, commitHash string) (map[string][]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("nil storer")
	}
	commit, err := object.GetCommit(s, plumbing.NewHash(commitHash))
	if err != nil {
		return nil, fmt.Errorf("lookup commit %s: %w", commitHash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("lookup tree for commit %s: %w", commitHash, err)
	}

	files := make(map[string][]byte)
	err = tree.Files().ForEach(func(f *object.File) error {
		contents, err := f.Contents()
		if err != nil {
			return err
		}
		files[f.Name] = []byte(contents)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read tree files for commit %s: %w", commitHash, err)
	}
	return files, nil
}
