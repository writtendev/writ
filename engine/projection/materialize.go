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
	// orderedOps is already dag.Order's canonical order (above), so its
	// earliest element names the type directly — calling
	// state.DetermineObjectType here would re-run the same Kahn sort a
	// second time over a slice already sorted by it.
	objectType := orderedOps[0].ObjectType

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
// columns, plus unknown_fields), every child-table row a collection,
// keyed-lww or append target folded to (unknownOps is the object's
// quarantined ops, needed only to skip them when writing append rows: see
// writeAppendRows).
func writeTypeRow(tx *sql.Tx, td *typeDescriptor, objectID string, folded map[string]any, rules []state.Rule, orderedOps []codec.Op, unknownFields string, unknownOps []state.UnknownOp) error {
	cols := []string{"object_id"}
	vals := []any{objectID}

	skip := make(map[string]bool, len(unknownOps))
	for _, u := range unknownOps {
		skip[u.Commit] = true
	}

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
		case plan.AppendTable != "":
			if err := writeAppendRows(tx, plan, objectID, orderedOps, skip); err != nil {
				return err
			}

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
	insertSQL := "INSERT INTO " + quoteIdent(td.Table.Name) + " (" + strings.Join(cols, ", ") + ") VALUES (" + qmarks + ")"
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
			insertSQL := "INSERT INTO " + quoteIdent(g.table.Name) + " (" + strings.Join(gCols, ", ") + ") VALUES (" + ph + ")"
			if _, err := tx.Exec(insertSQL, gVals...); err != nil {
				return fmt.Errorf("projection: insert %s row: %w", g.table.Name, err)
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
// target has its own ChildTable-shaped plan too (AppendTable) but is
// written by writeAppendRows instead, straight from orderedOps rather than
// from the folded value, and so never reaches this function.
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
			_, err = tx.Exec("INSERT INTO "+quoteIdent(plan.ChildTable)+" (object_id, idx, value) VALUES (?, ?, ?)", objectID, i, converted)
		} else {
			_, err = tx.Exec("INSERT INTO "+quoteIdent(plan.ChildTable)+" (object_id, item) VALUES (?, ?)", objectID, converted)
		}
		if err != nil {
			return fmt.Errorf("projection: insert %s row for %s: %w", plan.ChildTable, objectID, err)
		}
	}
	return nil
}

// writeAppendRows writes one row per folded list entry for one append
// target into its own table (targetPlan.AppendTable): for every op in
// orderedOps not already quarantined into unknown_ops, for every one of the
// target's rules (plan.AppendRules, pre-sorted into canonical rule order —
// spec/fold.md §5) that matches that op via opMatchesRuleLite, for every
// element the op's body carries at that rule's field, one row keyed
// (object_id, op_seq, entry_idx).
//
// op_seq is the op's own index in orderedOps — the object's total order L —
// not a running counter: a quarantined op is skipped but still occupies its
// position, so op_seq carries gaps rather than compacting around them. That
// is what makes two append targets' tables joinable on a shared op without
// either needing to know which ops the other happened to skip (WRIT-212)
// — cross-target pairing, which the old shared one-row-per-op table used to
// fix by construction, is redefined as "the same op_seq" instead of "the
// same row". entry_idx resets to 0 for each op and counts the entries that
// op alone contributes, across all of plan.AppendRules in canonical order —
// so where two rules bound to this target both match one op (the
// wildcard-op_version shape this ticket folds in, and the two-fields-one-
// envelope shape WRIT-201 made legal), their entries interleave in
// canonical rule order exactly as state.Fold's own accumulator dispatch
// does, rather than in whatever order a caller's rule slice happened to
// list them.
//
// A JSON array at the field is flattened to one row per element, mirroring
// engine/internal/fold's appendAccumulator.Apply exactly (an op writing
// ["a","b"] contributes two entries, not one array-valued entry); an absent
// field or an explicit JSON null contributes no row, also mirroring the
// accumulator, which is a behavior change from the old shared table's
// presence-gated NULL cell (WRIT-212 fixes the divergence the old comment
// here used to claim, wrongly, was already the case).
//
// Iterating every rule in plan.AppendRules, not only the ones fold's own
// matchedRulesByField admits (an op in this object's history actually
// writing that rule's field), is safe without reimplementing that admission
// logic: a rule no op ever writes contributes zero entries at every op, so
// the entry set comes out identical either way.
func writeAppendRows(tx *sql.Tx, plan *targetPlan, objectID string, orderedOps []codec.Op, skip map[string]bool) error {
	for opSeq, op := range orderedOps {
		if skip[op.ID] {
			continue
		}

		var body map[string]any
		bodyLoaded := false
		entryIdx := 0
		for _, r := range plan.AppendRules {
			if !opMatchesRuleLite(op, r) {
				continue
			}
			if !bodyLoaded {
				if len(op.Body) > 0 {
					if err := json.Unmarshal(op.Body, &body); err != nil {
						return fmt.Errorf("projection: unmarshal op %s body for %s: %w", op.ID, plan.AppendTable, err)
					}
				}
				bodyLoaded = true
			}
			raw, ok := body[r.Field]
			if !ok || raw == nil {
				continue
			}
			elems, ok := raw.([]any)
			if !ok {
				elems = []any{raw}
			}
			for _, e := range elems {
				if _, err := tx.Exec(
					"INSERT INTO "+quoteIdent(plan.AppendTable)+" (object_id, op_seq, entry_idx, value) VALUES (?, ?, ?, ?)",
					objectID, opSeq, entryIdx, columnValue(plan.ValueType, e),
				); err != nil {
					return fmt.Errorf("projection: insert %s row for %s: %w", plan.AppendTable, objectID, err)
				}
				entryIdx++
			}
		}
	}
	return nil
}

// writeMembersRows populates a target's generic members table from its
// folded value, when that value decodes to a JSON object: one row per
// top-level key, so a reader can filter two members of the same object
// together via two indexed EXISTS lookups instead of an unindexed scan.
// A value that is not object-shaped (or fails to decode) writes nothing —
// the members table stays a pure performance index, never a second source
// of truth for the scalar column, which already holds every member of the
// value. How faithfully depends on the strategy, and this table is built
// for only two of them (ddl.go: an untyped lww or create-once target, and
// nothing else): a create-once column holds the op's bytes verbatim, an
// lww one the re-marshal toText returns, with <, & and > escaped — see
// columnValue.
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
		if _, err := tx.Exec("INSERT INTO "+quoteIdent(table)+" (object_id, member, value) VALUES (?, ?, ?)", objectID, k, val); err != nil {
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
// typed field arrives here as json.RawMessage holding e.g. `"item-1"` —
// quotes included — not the Go string "item-1". Every value_type except
// the truly untyped one decodes those bytes back into a native Go value
// before the switch below runs, so a create-once column reads back
// exactly as an lww column of the same value_type would.
//
// "Truly untyped" is valueType == "" as ddl.go resolves it for the whole
// target, across every rule bound to it: the resolved value_type is the
// one every bound rule declares, so the target is untyped unless all of
// them declare the same non-empty value_type. That single predicate — not
// any particular combination of rules — is what this path turns on.
// Typechecking meanwhile stays per op: every op is checked against its
// own rule, so a value_type binds only the ops whose rule declares one.
// That rule's own typecheck is all this says anything about — declaring
// a value_type it admits only that type, declaring none it typechecks
// against no type at all and passes an object as readily as a string. It
// is not a claim about what the column can hold: other schema
// constraints, not enumerated here, narrow a field independently of
// value_type, and a value this typecheck admits can still be refused by
// one of them before it reaches the column. However the target got
// there, the raw bytes are the point, because no single declared type is
// there to decode them back into. A top-level null is refused whichever
// rule the op is checked against — by Producer validation rule 6
// (spec/op-envelope.md) unless one of those other constraints refuses it
// first.
//
// Untyped is the one case where that last equivalence does not hold,
// because only create-once's accumulator hands back raw bytes: an untyped
// create-once value reaches the column unchanged, unknown members and key
// order included, while an untyped lww value arrives already decoded and
// falls through to toText, which stores a JSON object re-marshaled (<, >
// and & escaped) and a JSON string bare, without its quotes.
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

// toText renders v as the string a TEXT column stores. Raw create-once
// bytes (json.RawMessage, which columnValue forwards here undecoded for
// any target ddl.go resolved to untyped — any target, that is, whose
// bound rules do not all declare the same non-empty value_type) pass
// through verbatim — the exact bytes the op stores, unknown members and
// key order included. An already-decoded value takes one of the other
// paths instead: a Go string is stored bare, without the JSON
// quotes the raw bytes would have carried, and anything the switch below
// does not special-case is re-marshaled by encoding/json, which escapes
// <, & and > to \u003c, \u0026 and \u003e.
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
// — today only a keyed-lww target whose bound rules disagree on Key
// (ddl.go's keyed-lww case, WRIT-205; an append target was the other
// reason before WRIT-212 gave every append target its own row-per-entry
// table, which has nothing left to withhold): a rule bound to one of them
// still matched, so the op is not quarantined, but its field has nowhere to
// land in SQL. It counts as unknown here rather than being dropped, which is
// what spec/forward-compatibility.md §Targets a projection declines
// requires.
//
// That routing stops where this map does, and deliberately: a withheld
// keyed-lww target is an accumulator (one register per key), but result is
// a per-body-field register with no key at all, so a target written under
// several keys keeps only the latest write per body field, collapsing the
// key dimension entirely. The entries the fold accumulated are complete
// only through the fold, never through this column, and
// spec/forward-compatibility.md §Targets a projection declines says exactly
// that rather than promising more.
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
			if _, err := tx.Exec("DELETE FROM "+quoteIdent(td.Table.Name)+" WHERE object_id = ?", objectID); err != nil {
				return fmt.Errorf("projection: delete %s row (%s): %w", td.Table.Name, objectID, err)
			}
			for _, child := range td.Children {
				if _, err := tx.Exec("DELETE FROM "+quoteIdent(child.Name)+" WHERE object_id = ?", objectID); err != nil {
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
	objectType string
	target     string
	anchorJSON string
}

// materializeAnchors resolves anchors against current target commits in
// code_tips. Which columns to read anchors from is driven by
// desc.anchorColumns — every scalar target, on any schema-declared object
// type, whose declared value_type is "anchor" — rather than a hard-coded
// read of one type's own anchor column: writ hard-codes no object type but
// `schema`, so there is no fixed type to read anchors from any more.
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
		cRows, err := tx.Query("SELECT object_id, " + ref.Column + " FROM " + quoteIdent(ref.Table) + " WHERE " + ref.Column + " IS NOT NULL AND " + ref.Column + " != '' AND " + ref.Column + " != 'null'")
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
			c.objectType = ref.ObjectType
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
				return resolvedCount, fmt.Errorf("projection: parse anchor for %s %s: %w", comm.objectType, comm.objectID, err)
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
