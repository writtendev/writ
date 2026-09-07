package projection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/writtendev/writ/engine/state"
)

// identPattern is the grammar every target and keyed-lww key component must
// match before it becomes part of a generated SQL identifier.
// schema-ops.schema.json does not constrain target (minLength 1 only) or key
// components (bare "type": "string"), so an arbitrary log-declared string
// would otherwise become an arbitrary SQL identifier. A type with a target or
// key component failing this grammar gets no tables at all: its objects fall
// to unknown_ops, the same no-winner idiom RulesFromSchemas already uses for
// a contested object_type.
var identPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const identMaxLength = 64

func validIdent(s string) bool {
	return s != "" && len(s) <= identMaxLength && identPattern.MatchString(s)
}

// ddlColumn is one generated column.
type ddlColumn struct {
	Name    string
	SQLType string
	Indexed bool
}

// ddlTable is one generated table: a type table (one per declared object
// type) or a child table (one per collection-valued target, or one per
// keyed-lww key-tuple group).
type ddlTable struct {
	Name       string
	Columns    []ddlColumn
	PrimaryKey []string
	Indexes    [][]string
}

func (t ddlTable) createStatement() string {
	var lines []string
	for _, c := range t.Columns {
		lines = append(lines, "    "+c.Name+" "+c.SQLType)
	}
	lines = append(lines, "    PRIMARY KEY ("+strings.Join(t.PrimaryKey, ", ")+")")
	return "CREATE TABLE " + t.Name + " (\n" + strings.Join(lines, ",\n") + "\n);"
}

func (t ddlTable) indexStatements() []string {
	var out []string
	for _, cols := range t.Indexes {
		name := "idx_" + t.Name + "_" + strings.Join(cols, "_")
		out = append(out, "CREATE INDEX "+name+" ON "+t.Name+"("+strings.Join(cols, ", ")+");")
	}
	return out
}

func (t ddlTable) orderByQuery() string {
	var parts []string
	for _, c := range t.PrimaryKey {
		parts = append(parts, c+" ASC")
	}
	return "SELECT * FROM " + t.Name + " ORDER BY " + strings.Join(parts, ", ")
}

// targetPlan describes how one installed target key (state.Rule.TargetKey())
// is materialized: a scalar column on the type table, a child table keyed by
// (object_id, item) or (object_id, idx), or a column inside a keyed-lww
// group table shared with the other targets keyed on the same tuple. An
// append-strategy target carries no plan of its own — it is materialized
// through the type descriptor's AppendGroups instead (see appendGroupPlan).
type targetPlan struct {
	Strategy  string
	ValueType string

	// Scalar (lww, create-once, lattice, tombstone).
	Column        string
	PosOpIDColumn string // set only when ValueType == "position"
	MembersTable  string // set only for an untyped (ValueType == "") lww/create-once target

	// Collection (set-union, set-observed-remove, multi-value).
	ChildTable string
	ChildKind  string // "item" or "idx"

	// Keyed-lww.
	GroupTable  string
	KeyColumns  []string // "k_"-prefixed, in rule.Key order
	ValueColumn string
}

// appendGroupFieldSource is one rule contributing values to an
// appendGroupMember's target: the (op_type, op_version) envelope it is
// declared under, and the body key that envelope's ops carry the value in.
// A member's Fields lists one of these per declaring rule, in the same
// order those rules appear in the type's own rule slice (the slice
// buildTypeDescriptor received and state.Fold itself folds against) — see
// fieldForOp.
type appendGroupFieldSource struct {
	OpType    string
	OpVersion int64
	Field     string
}

// appendGroupMember is one append-strategy target sharing an
// appendGroupPlan's table. A single target key can be declared by more than
// one rule — a version bump, or a second op_type agreeing on every merge
// attribute (spec/schema-ops.md §8) — and those rules are not required to
// share a field name, so Column alone (keyed by the target) is not enough
// to read a value back out of an op's body: Fields carries the field name
// each declaring rule actually uses, keyed by the envelope that rule
// applies to (WRIT-189 round 4 MAJOR-1).
type appendGroupMember struct {
	Key       string // state.Rule.TargetKey()
	Column    string // "f_" + Key
	ValueType string
	Fields    []appendGroupFieldSource // one per declaring rule, in rule-slice order
}

// appendGroupEnvelope is one (op_type, op_version) envelope an
// appendGroupPlan's table accepts rows from. A target's rule set is not
// always one envelope: a version bump keeping the same target, or a second
// op_type agreeing on every merge attribute (spec/schema-ops.md §8), both
// legally declare the same append target under two different envelopes,
// and state.Fold accumulates both into the one target-keyed list regardless
// (it groups matched rules by target key alone, not by op_type/op_version —
// spec/fold.md §5, spec/schema-ops.md §8). A group's table has to accept
// rows from every one of those envelopes to match, not just one — see
// buildAppendGroups.
type appendGroupEnvelope struct {
	OpType    string
	OpVersion int64
}

// appendGroupPlan describes one shared child table for a connected group of
// append-strategy targets — every target sharing a rule's envelope with
// another lands in the same group (round 2's same-op-body pairing below),
// and a target declared under more than one envelope bridges those
// envelopes' whole membership into one group (round 3's cross-envelope
// fix) — one row per op the materializer walks that matches any of the
// group's envelopes and isn't already quarantined to unknown_ops, one
// column per member, NULL where that particular op's body omitted the
// field.
//
// Two independent per-field child tables, zipped back together by a reader
// matching local list positions, cannot express "these two values came from
// the same op": an op writing only one of two fields shifts every later
// pairing (WRIT-189 round 2 MAJOR-1). A shared table sidesteps that
// entirely — the row itself is the pairing, fixed at write time, nothing
// for a reader to reconstruct. A target with no sibling sharing its
// envelope still goes through this path as a group of one: same shape, same
// code, no special case for the common single-field append target.
//
// Keying a group off one representative rule's envelope instead (WRIT-189
// round 2's shape) silently dropped every op belonging to a target's other
// envelope, and which envelope survived depended on the rule slice's order
// — the same non-determinism class as WRIT-186's map iteration and
// WRIT-198's fieldRules[0] (WRIT-189 round 3 MAJOR-1). Envelopes is every
// envelope any member target is declared under, not one.
type appendGroupPlan struct {
	Table     string
	Envelopes []appendGroupEnvelope // every envelope any member is declared under, sorted
	Members   []appendGroupMember   // sorted by Key — table column order
}

// appendGroupInfo is buildAppendGroups' output: one connected component of
// append-strategy target keys (sorted) plus the sorted, deduped set of
// envelopes any of them are declared under.
type appendGroupInfo struct {
	members   []string
	envelopes []appendGroupEnvelope
}

// buildAppendGroups partitions every append-strategy target declared in
// rules into the shared child-table groups appendGroupPlan describes:
// union-find over target keys, with an edge between every pair of targets
// an envelope co-declares (round 2's pairing) and a target bridging every
// envelope its own rules declare (round 3's fix, MAJOR-1) — a target
// declared under two envelopes pulls both envelopes' whole membership into
// one component, so the resulting table accepts rows from either rather
// than silently losing whichever envelope buildTypeDescriptor didn't
// happen to pick as tk's representative.
//
// This consults every rule sharing a target key, never a single
// representative, and the result does not depend on the order rules are
// visited in: union-find's final partition is determined only by which
// pairs are ever unioned, not by the order those unions happen in, and the
// component and envelope lists are sorted before being handed back. A
// shuffled rules slice therefore produces an identical grouping —
// TestAppendGroupContentIsDeterministic pins this at the materialized-row
// level, not only DDL.
func buildAppendGroups(rules []state.Rule) []*appendGroupInfo {
	parent := make(map[string]string)
	var find func(string) string
	find = func(x string) string {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	envelopeMembers := make(map[appendGroupEnvelope][]string)
	targetEnvelopes := make(map[string][]appendGroupEnvelope)
	for _, r := range rules {
		if r.Strategy != "append" {
			continue
		}
		tk := r.TargetKey()
		find(tk) // register even a target with only one envelope
		ek := appendGroupEnvelope{OpType: r.OpType, OpVersion: r.OpVersion}
		envelopeMembers[ek] = append(envelopeMembers[ek], tk)
		targetEnvelopes[tk] = append(targetEnvelopes[tk], ek)
	}
	for _, members := range envelopeMembers {
		for i := 1; i < len(members); i++ {
			union(members[0], members[i])
		}
	}

	rootMembers := make(map[string][]string)
	for tk := range targetEnvelopes {
		root := find(tk)
		rootMembers[root] = append(rootMembers[root], tk)
	}

	var groups []*appendGroupInfo
	for _, members := range rootMembers {
		sort.Strings(members)
		envSet := make(map[appendGroupEnvelope]bool)
		for _, tk := range members {
			for _, ek := range targetEnvelopes[tk] {
				envSet[ek] = true
			}
		}
		var envs []appendGroupEnvelope
		for ek := range envSet {
			envs = append(envs, ek)
		}
		sort.Slice(envs, func(i, j int) bool {
			if envs[i].OpType != envs[j].OpType {
				return envs[i].OpType < envs[j].OpType
			}
			return envs[i].OpVersion < envs[j].OpVersion
		})
		groups = append(groups, &appendGroupInfo{members: members, envelopes: envs})
	}
	sort.Slice(groups, func(i, j int) bool {
		return strings.Join(groups[i].members, "\x00") < strings.Join(groups[j].members, "\x00")
	})
	return groups
}

// anchorColumnRef names one scalar column whose declared value_type is
// "anchor" — anchor_resolutions is driven off these, generically, rather
// than a hard-coded read of comments.anchor. Target is the bare target key
// (e.g. "anchor"), recorded verbatim into anchor_resolutions.target so a
// second anchor-valued target sharing the same table would still resolve
// into distinguishable rows.
type anchorColumnRef struct {
	Table  string
	Column string
	Target string
}

// typeDescriptor is everything the materializer and the generic readers need
// to know about one declared object type's generated tables.
type typeDescriptor struct {
	ObjectType   string
	Table        ddlTable
	Children     []ddlTable
	Targets      map[string]*targetPlan
	AppendGroups []appendGroupPlan
}

// schemaDescriptor is the generator's output: every declared object type's
// tables, keyed for the materializer, plus the canonical JSON and digest
// ApplySchema compares to decide whether to drop and rebuild.
type schemaDescriptor struct {
	types         map[string]*typeDescriptor
	order         []string // object types with installed tables, sorted
	rulesByType   map[string][]state.Rule
	anchorColumns []anchorColumnRef
	canonicalJSON []byte
	digest        string

	// tables is every generated table, sorted by name. It is always
	// populated — either derived from types (a live buildDescriptor call)
	// or reloaded from meta's persisted table list (Open, with no rules and
	// so no types) — so DumpTables and Rebuild's truncate step work
	// identically whichever way desc came to exist.
	tables []ddlTable

	// queryShapes and queryOrder are objectsTextClause/objectsNotDeletedClause's
	// own view of the descriptor (query.go): table name plus scalar target
	// columns (Column, ValueType, Strategy), for every declared type with
	// installed tables. Deliberately kept apart from types/order rather than
	// read from them directly, so that a name-only reopen (descriptorFromPersisted)
	// can populate this — from meta, with no DAG walk — while leaving
	// types nil: requireMaterializationPlan's `desc.types == nil` check
	// (WRIT-189 round 2 MINOR-6) depends on types staying nil until a real
	// materialization plan (Children, AppendGroups, and the rest a
	// materializeObject call needs) exists for this process, and populating
	// types with the query-only subset would silently satisfy that guard
	// with a plan incapable of materializing anything (WRIT-192 round 2
	// MAJOR-1). A live (buildDescriptor) descriptor populates both this and
	// types/order from the same typeDescriptor, so the two clause builders
	// see identical answers either way.
	queryShapes map[string]objectQueryShape
	queryOrder  []string
}

// objectQueryTarget is the minimal shape objectsTextClause and
// objectsNotDeletedClause need for one declared type's scalar target: which
// column, what value type (a string/text column is a text-search
// candidate), what merge strategy (a tombstone column is a soft-delete
// candidate).
type objectQueryTarget struct {
	Column    string `json:"column"`
	ValueType string `json:"value_type"`
	Strategy  string `json:"strategy"`
}

// objectQueryShape is one declared type's table name plus its scalar
// targets — everything objectsTextClause/objectsNotDeletedClause consume.
// Collection, keyed-lww, and append targets carry no scalar Column (their
// data lives in a child table) and so contribute nothing here.
type objectQueryShape struct {
	Table   string              `json:"table"`
	Targets []objectQueryTarget `json:"targets,omitempty"`
}

// objectQueryShapeFromType derives one type's query shape from its full
// typeDescriptor — used both when buildDescriptor has one freshly built and
// when descriptorFromPersisted rehydrates a lighter-weight equivalent from
// meta.
func objectQueryShapeFromType(td *typeDescriptor) objectQueryShape {
	var targets []objectQueryTarget
	for _, plan := range td.Targets {
		if plan.Column == "" {
			continue
		}
		targets = append(targets, objectQueryTarget{Column: plan.Column, ValueType: plan.ValueType, Strategy: plan.Strategy})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Column < targets[j].Column })
	return objectQueryShape{Table: td.Table.Name, Targets: targets}
}

func sqlType(valueType string) string {
	switch valueType {
	case "int":
		return "INTEGER"
	case "number":
		return "REAL"
	case "bool":
		return "INTEGER"
	default:
		// string, text, enum, timestamp, person-ref, object-ref, git-oid,
		// position, anchor, and untyped (value_type == "") all land in TEXT.
		return "TEXT"
	}
}

// allTables returns every generated table, sorted by name — the unit
// DumpTables, ApplySchema's drop step, and Rebuild's truncate step all
// iterate over.
func (d *schemaDescriptor) allTables() []ddlTable {
	if d == nil {
		return nil
	}
	return d.tables
}

// createSQL renders the full CREATE TABLE + CREATE INDEX text for every
// generated table, in a stable order derived purely from sorted object types
// and sorted target/child names — so a shuffled rule index produces
// byte-identical DDL (TestGeneratedDDLIsDeterministic).
func (d *schemaDescriptor) createSQL() string {
	var b strings.Builder
	for _, t := range d.allTables() {
		b.WriteString(t.createStatement())
		b.WriteString("\n")
		for _, idx := range t.indexStatements() {
			b.WriteString(idx)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// buildDescriptor generates a schemaDescriptor from a validated rule index
// (RulesFromSchemas' shape, exactly: map[object_type][]Rule). It performs no
// I/O and consults nothing beyond rules: the projection cannot resolve
// schemas itself, so the caller passes in an index already merged and
// validated (built-in vocabulary overlaid by whatever the log declares,
// log-over-built-in per type).
func buildDescriptor(rules map[string][]state.Rule) (*schemaDescriptor, error) {
	objectTypes := make([]string, 0, len(rules))
	for t := range rules {
		objectTypes = append(objectTypes, t)
	}
	sort.Strings(objectTypes)

	desc := &schemaDescriptor{
		types:       make(map[string]*typeDescriptor),
		rulesByType: make(map[string][]state.Rule),
	}
	used := make(map[string]bool)

	for _, objectType := range objectTypes {
		typeRules := rules[objectType]
		if len(typeRules) == 0 {
			continue
		}
		td, anchors, ok, err := buildTypeDescriptor(objectType, typeRules, used)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		desc.types[objectType] = td
		desc.rulesByType[objectType] = typeRules
		desc.order = append(desc.order, objectType)
		desc.anchorColumns = append(desc.anchorColumns, anchors...)
	}

	var tables []ddlTable
	desc.queryShapes = make(map[string]objectQueryShape, len(desc.order))
	for _, objectType := range desc.order {
		td := desc.types[objectType]
		tables = append(tables, td.Table)
		tables = append(tables, td.Children...)
		desc.queryShapes[objectType] = objectQueryShapeFromType(td)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	desc.tables = tables
	desc.queryOrder = desc.order

	snapshot := buildSnapshot(desc)
	js, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("projection: marshal schema descriptor: %w", err)
	}
	desc.canonicalJSON = js
	sum := sha256.Sum256(js)
	desc.digest = hex.EncodeToString(sum[:])

	return desc, nil
}

// descriptorFromPersisted rebuilds the DumpTables/Rebuild-truncate-only view
// of a schemaDescriptor from the minimal form Open persists, with no rules
// and so no per-type materialization plan: a reopened cache that has not
// yet seen an ApplySchema call in this process can still be dumped and
// truncated, but nothing can be freshly materialized into it until
// ApplySchema runs (Store.Open calls it immediately after opening the
// projection, so this path exists only for callers driving the projection
// package directly without going through package writ). types/order stay
// nil deliberately (requireMaterializationPlan's guard, see schemaDescriptor's
// own doc comment on queryShapes) — but queryShapes/queryOrder are rehydrated
// from queryShapesJSON (meta key "schema_query_shapes") so
// objectsTextClause/objectsNotDeletedClause still answer correctly on this
// path, instead of silently emitting no clause at all (WRIT-192 round 2
// MAJOR-1). queryShapesJSON is empty exactly when persisted is: a cache that
// has never had ApplySchema run in any process, where queryShapes/queryOrder
// staying nil is correct because tables stays empty too. persisted non-empty
// with queryShapesJSON empty is a hazard, not a legitimate state — ApplySchema
// always writes schema_tables and schema_query_shapes together — and left
// unchecked it would answer objectsTextClause/objectsNotDeletedClause wrong
// forever with nothing to repair it. requireMaterializationPlan treats the
// structurally identical types-vs-tables shape as an error; this matches it
// rather than silently degrading.
func descriptorFromPersisted(persisted []persistedTable, queryShapesJSON string) (*schemaDescriptor, error) {
	tables := make([]ddlTable, len(persisted))
	for i, p := range persisted {
		tables[i] = ddlTable{Name: p.Name, PrimaryKey: p.PrimaryKey}
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })

	desc := &schemaDescriptor{tables: tables}
	if queryShapesJSON == "" {
		if len(persisted) > 0 {
			return nil, fmt.Errorf("projection: meta has schema_tables but no schema_query_shapes; the cache is in an inconsistent state and must be rebuilt")
		}
		return desc, nil
	}
	var persistedShapes []persistedQueryShape
	if err := json.Unmarshal([]byte(queryShapesJSON), &persistedShapes); err != nil {
		return nil, fmt.Errorf("projection: unmarshal schema_query_shapes: %w", err)
	}
	desc.queryShapes = make(map[string]objectQueryShape, len(persistedShapes))
	desc.queryOrder = make([]string, 0, len(persistedShapes))
	for _, ps := range persistedShapes {
		desc.queryShapes[ps.ObjectType] = objectQueryShape{Table: ps.Table, Targets: ps.Targets}
		desc.queryOrder = append(desc.queryOrder, ps.ObjectType)
	}
	sort.Strings(desc.queryOrder)
	return desc, nil
}

// persistedQueryShape and its Targets are objectQueryShape's on-disk form:
// what ApplySchema records in meta (key "schema_query_shapes") so a
// reopened cache, before this process's own first ApplySchema/Refresh call,
// can still answer Objects' f.Text and !IncludeDeleted filters correctly
// (WRIT-192 round 2 MAJOR-1) — descriptorFromPersisted's counterpart to
// persistedTable/persistedTables, but carrying the per-target Column,
// ValueType, and Strategy those two clause builders read, which
// persistedTable's bare table name/primary key cannot supply.
type persistedQueryShape struct {
	ObjectType string              `json:"object_type"`
	Table      string              `json:"table"`
	Targets    []objectQueryTarget `json:"targets,omitempty"`
}

// persistedQueryShapes converts desc's live queryShapes/queryOrder into the
// on-disk form ApplySchema writes to meta.
func persistedQueryShapes(desc *schemaDescriptor) []persistedQueryShape {
	out := make([]persistedQueryShape, 0, len(desc.queryOrder))
	for _, objectType := range desc.queryOrder {
		shape := desc.queryShapes[objectType]
		out = append(out, persistedQueryShape{
			ObjectType: objectType,
			Table:      shape.Table,
			Targets:    shape.Targets,
		})
	}
	return out
}

// buildTypeDescriptor generates one object type's tables. ok is false (with
// a nil error) when a target key or a keyed-lww key component fails
// identPattern, when the type's generated table or column names collide
// with an identifier an earlier-processed type (built-in or log-declared)
// already registered, or when two append-strategy rules bind the same
// target key to two different Fields under one exact (op_type, op_version)
// envelope (WRIT-189 round 5 MAJOR-1: state.Fold picks a value deterministic
// regardless of rule order — see fold's matchedRulesByField — but nothing
// short of reimplementing that admission logic here could make the
// projection's own first-match reader agree with it, so the shape is
// withheld instead of guessed at). Any of these withholds the whole type,
// not just the offending target, and buildDescriptor never fails the whole
// schema build over it. A colliding or ambiguous type is data someone else
// wrote — a legal object type name under op-envelope's grammar, such as
// "ticket--base", can still generate a table name ("o_ticket__base")
// another type already owns, and a legal schema can still declare two rules
// that agree on everything but Field — and WRIT-188 round 3's ruling
// applies here just as much as there: data another writer wrote must never
// brick the repository. A withheld type's objects fall to unknown_ops
// through the same absent-typeDescriptor path an invalid target already
// takes (materializeObject).
func buildTypeDescriptor(objectType string, rules []state.Rule, used map[string]bool) (*typeDescriptor, []anchorColumnRef, bool, error) {
	tableName := "o_" + strings.ReplaceAll(objectType, "-", "_")

	// The unit is the target key: one column or one child-table
	// participation per distinct target key, regardless of how many rules
	// (op types) address it. spec.CheckTargetCollision guarantees every
	// rule sharing a target key agrees on strategy — always — but not on
	// every other merge attribute: spec/fieldrules.go's carve-out and
	// spec/schema-ops.md §8 both let a version bump of the same (op_type,
	// field) freely change value_type (also key, key_types, enum,
	// max_length). So a target with such a version bump has two rules that
	// legally disagree on ValueType, and reps[tk] — whichever one appeared
	// first in rules — decides which one wins: the generated column's SQL
	// type, and so whether the other version's values survive
	// columnValue's type coercion, becomes a function of rule-slice order.
	// Not fixed here — choosing which of two legitimately-disagreeing
	// value_types a shared column should take is a design question on its
	// own, filed as WRIT-205.
	reps := make(map[string]state.Rule)
	var targetKeys []string
	for _, r := range rules {
		tk := r.TargetKey()
		if _, ok := reps[tk]; !ok {
			reps[tk] = r
			targetKeys = append(targetKeys, tk)
		}
	}
	sort.Strings(targetKeys)

	for _, tk := range targetKeys {
		if !validIdent(tk) {
			return nil, nil, false, nil
		}
		if r := reps[tk]; r.Strategy == "keyed-lww" {
			for _, k := range r.Key {
				if !validIdent(k) {
					return nil, nil, false, nil
				}
			}
		}
	}

	targets := make(map[string]*targetPlan, len(targetKeys))
	var scalarCols []ddlColumn
	var anchorRefs []anchorColumnRef
	children := make(map[string]ddlTable)

	// insertChild registers name -> table in children, refusing a second
	// write to a name an earlier declaration already claimed within this
	// type. children used to be written to directly as a plain map: two
	// unrelated declarations generating the same table name (WRIT-189 round
	// 2 MAJOR-2 — a set-union target and a keyed-lww key group, two
	// keyed-lww key tuples that join to the same string, or an untyped
	// lww/create-once target's members table and an unrelated target)
	// silently deduped there, so identCollision below — which only ever
	// sees the already-deduped map — never caught it, and whichever
	// declaration wrote last decided the shape every earlier target's plan
	// still pointed at, hard-erroring at materialize and bricking every
	// Refresh from then on. Every legitimate write below happens exactly
	// once per declaration (a keyed-lww or append-envelope group's table is
	// built once, after every member is gathered, never once per member),
	// so any second write to a name is a genuine collision, never a
	// legitimate merge — collided withholds the whole type for it, exactly
	// like the cross-type collision identCollision already catches.
	collided := false
	insertChild := func(name string, table ddlTable) {
		if collided {
			return
		}
		if _, exists := children[name]; exists {
			collided = true
			return
		}
		children[name] = table
	}

	type groupInfo struct {
		table   string
		keyCols []string
		members []string
	}
	groups := make(map[string]*groupInfo)

	for _, tk := range targetKeys {
		r := reps[tk]
		switch r.Strategy {
		case "lww", "create-once", "lattice", "tombstone":
			col := "f_" + tk
			plan := &targetPlan{Strategy: r.Strategy, ValueType: r.ValueType, Column: col}
			scalarCols = append(scalarCols, ddlColumn{Name: col, SQLType: sqlType(r.ValueType), Indexed: r.ValueType != ""})
			if r.ValueType == "position" {
				plan.PosOpIDColumn = col + "__op_id"
				scalarCols = append(scalarCols, ddlColumn{Name: plan.PosOpIDColumn, SQLType: "TEXT"})
			}
			if r.ValueType == "anchor" {
				anchorRefs = append(anchorRefs, anchorColumnRef{Table: tableName, Column: col, Target: tk})
			}
			// An untyped lww or create-once target's folded value can be an
			// arbitrary JSON object (comment.subject is the one case in the
			// shipped vocabulary), so its column carries no index and a
			// filter on it can't use whole-blob equality — create-once
			// preserves raw bytes including unknown members and key order.
			// The generic remedy, independent of what this particular
			// target happens to be: a members child table, one row per
			// object-valued key, indexed on (member, value), so a reader
			// can filter two members of the same object together via two
			// indexed EXISTS lookups instead of an unindexed json_extract
			// scan. Only lww and create-once are considered: tombstone
			// folds to bool and lattice to an enum string, neither of which
			// is ever object-shaped.
			if r.ValueType == "" && (r.Strategy == "lww" || r.Strategy == "create-once") {
				membersName := tableName + "__" + tk + "__members"
				plan.MembersTable = membersName
				insertChild(membersName, ddlTable{
					Name: membersName,
					Columns: []ddlColumn{
						{Name: "object_id", SQLType: "TEXT"},
						{Name: "member", SQLType: "TEXT"},
						{Name: "value", SQLType: "TEXT", Indexed: true},
					},
					PrimaryKey: []string{"object_id", "member"},
					Indexes:    [][]string{{"member", "value"}},
				})
			}
			targets[tk] = plan

		case "set-union", "set-observed-remove":
			childName := tableName + "__" + tk
			targets[tk] = &targetPlan{Strategy: r.Strategy, ValueType: r.ValueType, ChildTable: childName, ChildKind: "item"}
			insertChild(childName, ddlTable{
				Name: childName,
				Columns: []ddlColumn{
					{Name: "object_id", SQLType: "TEXT"},
					{Name: "item", SQLType: sqlType(r.ValueType), Indexed: true},
				},
				PrimaryKey: []string{"object_id", "item"},
				Indexes:    [][]string{{"item"}},
			})

		case "multi-value":
			childName := tableName + "__" + tk
			targets[tk] = &targetPlan{Strategy: r.Strategy, ValueType: r.ValueType, ChildTable: childName, ChildKind: "idx"}
			insertChild(childName, ddlTable{
				Name: childName,
				Columns: []ddlColumn{
					{Name: "object_id", SQLType: "TEXT"},
					{Name: "idx", SQLType: "INTEGER"},
					{Name: "value", SQLType: sqlType(r.ValueType)},
				},
				PrimaryKey: []string{"object_id", "idx"},
			})

		case "append":
			// Deferred entirely to buildAppendGroups below (WRIT-189 round 2
			// MAJOR-1, round 3 MAJOR-1): every append target's own full rule
			// set — every envelope it is declared under, not reps[tk] alone
			// — feeds group formation directly from this type's rules, never
			// from this per-target-key loop (reps here holds only one
			// representative rule per target, which is exactly what round 3
			// found unsafe for a target declared under more than one
			// envelope).

		case "keyed-lww":
			groupKey := strings.Join(r.Key, "\x00")
			g, ok := groups[groupKey]
			if !ok {
				g = &groupInfo{table: tableName + "__k_" + strings.Join(r.Key, "_"), keyCols: r.Key}
				groups[groupKey] = g
			}
			g.members = append(g.members, tk)

		default:
			// spec.KnownCatalogueStrategies already closes the catalogue
			// upstream of RulesFromSchemas; an unrecognized strategy here
			// is a programmer error, not a log-supplied one.
			return nil, nil, false, fmt.Errorf("projection: object type %q target %q has unrecognized strategy %q", objectType, tk, r.Strategy)
		}
	}

	var groupKeys []string
	for gk := range groups {
		groupKeys = append(groupKeys, gk)
	}
	sort.Strings(groupKeys)
	for _, gk := range groupKeys {
		g := groups[gk]
		sort.Strings(g.members)

		cols := []ddlColumn{{Name: "object_id", SQLType: "TEXT"}}
		var keyColNames []string
		for _, k := range g.keyCols {
			kc := "k_" + k
			keyColNames = append(keyColNames, kc)
			cols = append(cols, ddlColumn{Name: kc, SQLType: "TEXT"})
		}
		for _, tk := range g.members {
			r := reps[tk]
			col := "f_" + tk
			cols = append(cols, ddlColumn{Name: col, SQLType: sqlType(r.ValueType), Indexed: r.ValueType != ""})
			if r.ValueType == "anchor" {
				anchorRefs = append(anchorRefs, anchorColumnRef{Table: g.table, Column: col, Target: tk})
			}
			targets[tk] = &targetPlan{
				Strategy: "keyed-lww", ValueType: r.ValueType,
				GroupTable: g.table, KeyColumns: keyColNames, ValueColumn: col,
			}
		}

		pk := append([]string{"object_id"}, keyColNames...)
		var idx [][]string
		if len(keyColNames) > 0 {
			idx = append(idx, keyColNames)
		}
		insertChild(g.table, ddlTable{Name: g.table, Columns: cols, PrimaryKey: pk, Indexes: idx})
	}

	var appendGroupPlans []appendGroupPlan
	for _, g := range buildAppendGroups(rules) {
		table := tableName + "__" + strings.Join(g.members, "_")
		cols := []ddlColumn{
			{Name: "object_id", SQLType: "TEXT"},
			{Name: "idx", SQLType: "INTEGER"},
		}
		members := make([]appendGroupMember, 0, len(g.members))
		for _, tk := range g.members {
			r := reps[tk]
			col := "f_" + tk
			cols = append(cols, ddlColumn{Name: col, SQLType: sqlType(r.ValueType)})
			// Every append rule bound to tk, not just reps[tk]'s one
			// representative: a version bump or a second op_type agreeing
			// on every merge attribute (spec/schema-ops.md §8) can declare
			// tk under a different field name, and writeAppendGroupRows
			// needs the field that specific envelope actually uses (WRIT-189
			// round 4 MAJOR-1).
			//
			// Two of tk's rules can still share one exact (op_type,
			// op_version) envelope while disagreeing on Field — nothing
			// upstream forbids it, and fieldForOp's first match would then
			// pick whichever one happens to come first in rules, which is
			// order-dependent and can diverge from state.Fold (WRIT-189
			// round 5 MAJOR-1: body {"b":"yb"} folds to ["yb"] under either
			// rule order, but first-match projects NULL under one order and
			// "yb" under the other, with the value landing in neither
			// unknown_fields nor unknown_ops). fieldForOp cannot fix this by
			// picking "better" — it has no admission logic of its own to
			// consult, and reimplementing state.Fold's here would duplicate
			// the one place that logic is allowed to live. So this loop
			// detects the ambiguous shape — two distinct Fields declared for
			// the same tk under the same envelope — and withholds the whole
			// type for it below, exactly like identCollision's cross-type
			// collision and the invalid-identifier check above: the type's
			// objects fall to unknown_ops instead of a column silently
			// materializing as NULL. A target declared under two *different*
			// envelopes with two different field names is not this shape —
			// each envelope still resolves to exactly one field — and stays
			// on the ordinary path (round 3 MAJOR-1, round 4 MAJOR-1,
			// verified order-independent again in round 5).
			var fields []appendGroupFieldSource
			envelopeField := make(map[appendGroupEnvelope]string)
			for _, rr := range rules {
				if rr.Strategy != "append" || rr.TargetKey() != tk {
					continue
				}
				ek := appendGroupEnvelope{OpType: rr.OpType, OpVersion: rr.OpVersion}
				if prevField, seen := envelopeField[ek]; seen && prevField != rr.Field {
					collided = true
				} else if !seen {
					envelopeField[ek] = rr.Field
				}
				fields = append(fields, appendGroupFieldSource{OpType: rr.OpType, OpVersion: rr.OpVersion, Field: rr.Field})
			}
			members = append(members, appendGroupMember{Key: tk, Column: col, ValueType: r.ValueType, Fields: fields})
		}
		insertChild(table, ddlTable{Name: table, Columns: cols, PrimaryKey: []string{"object_id", "idx"}})
		appendGroupPlans = append(appendGroupPlans, appendGroupPlan{
			Table: table, Envelopes: g.envelopes, Members: members,
		})
	}

	if collided {
		return nil, nil, false, nil
	}

	// unknown_fields is the generic expression of forward-compatibility on
	// this type table: every body key no installed rule matched for its op's
	// (op_type, op_version), last-write-wins per key over the total order.
	scalarCols = append(scalarCols, ddlColumn{Name: "unknown_fields", SQLType: "TEXT"})
	sort.Slice(scalarCols, func(i, j int) bool { return scalarCols[i].Name < scalarCols[j].Name })

	typeTable := ddlTable{
		Name:       tableName,
		Columns:    append([]ddlColumn{{Name: "object_id", SQLType: "TEXT"}}, scalarCols...),
		PrimaryKey: []string{"object_id"},
	}
	for _, c := range typeTable.Columns {
		if c.Indexed {
			typeTable.Indexes = append(typeTable.Indexes, []string{c.Name})
		}
	}
	for _, tk := range targetKeys {
		if p := targets[tk]; p != nil && p.PosOpIDColumn != "" {
			typeTable.Indexes = append(typeTable.Indexes, []string{p.Column, p.PosOpIDColumn})
		}
	}
	sort.Slice(typeTable.Indexes, func(i, j int) bool {
		return strings.Join(typeTable.Indexes[i], ",") < strings.Join(typeTable.Indexes[j], ",")
	})

	var childNames []string
	for n := range children {
		childNames = append(childNames, n)
	}
	sort.Strings(childNames)
	var childList []ddlTable
	for _, n := range childNames {
		childList = append(childList, children[n])
	}

	if identCollision(used, typeTable, childList) {
		return nil, nil, false, nil
	}
	registerIdents(used, typeTable, childList)

	td := &typeDescriptor{
		ObjectType:   objectType,
		Table:        typeTable,
		Children:     childList,
		Targets:      targets,
		AppendGroups: appendGroupPlans,
	}

	return td, anchorRefs, true, nil
}

// tableIdents lists t's own generated table name and every "table.column"
// identifier it would register — the unit identCollision and registerIdents
// both operate over.
func tableIdents(t ddlTable) []string {
	idents := make([]string, 0, len(t.Columns)+1)
	idents = append(idents, t.Name)
	for _, c := range t.Columns {
		idents = append(idents, t.Name+"."+c.Name)
	}
	return idents
}

// identCollision reports whether typeTable or any of its child tables would
// reuse a table or column identifier already registered by an
// earlier-processed type, or reuse one another's — e.g. two targets in the
// same type whose generated child-table names collide. It never mutates
// used: a caller that finds a collision withholds the whole type rather than
// registering any part of it (registerIdents is the commit step, called only
// once a caller has confirmed there is none).
func identCollision(used map[string]bool, typeTable ddlTable, children []ddlTable) bool {
	seen := make(map[string]bool)
	check := func(t ddlTable) bool {
		for _, id := range tableIdents(t) {
			if used[id] || seen[id] {
				return true
			}
			seen[id] = true
		}
		return false
	}
	if check(typeTable) {
		return true
	}
	for _, ct := range children {
		if check(ct) {
			return true
		}
	}
	return false
}

// registerIdents commits every identifier typeTable and its children
// introduce into used. Call only after identCollision has confirmed none of
// them are already taken.
func registerIdents(used map[string]bool, typeTable ddlTable, children []ddlTable) {
	for _, id := range tableIdents(typeTable) {
		used[id] = true
	}
	for _, ct := range children {
		for _, id := range tableIdents(ct) {
			used[id] = true
		}
	}
}

// tableSnapshot and typeSnapshot are the canonical, digest-bearing
// projections of a schemaDescriptor: map-valued so encoding/json's sorted
// map-key marshaling makes the JSON (and so the digest) independent of the
// order the descriptor happened to build its tables in.
type tableSnapshot struct {
	Columns    map[string]string `json:"columns"`
	Indexed    []string          `json:"indexed,omitempty"`
	PrimaryKey []string          `json:"primary_key"`
}

type typeSnapshot struct {
	Table    tableSnapshot            `json:"table"`
	Children map[string]tableSnapshot `json:"children,omitempty"`
}

func snapshotTable(t ddlTable) tableSnapshot {
	cols := make(map[string]string, len(t.Columns))
	var indexed []string
	for _, c := range t.Columns {
		cols[c.Name] = c.SQLType
		if c.Indexed {
			indexed = append(indexed, c.Name)
		}
	}
	sort.Strings(indexed)
	return tableSnapshot{Columns: cols, Indexed: indexed, PrimaryKey: t.PrimaryKey}
}

func buildSnapshot(desc *schemaDescriptor) map[string]typeSnapshot {
	out := make(map[string]typeSnapshot, len(desc.types))
	for objectType, td := range desc.types {
		out[objectType] = snapshotType(td)
	}
	return out
}

// snapshotType is buildSnapshot's per-type unit: td's table and every child
// table, map-valued so two typeDescriptors built from equivalent but
// differently-ordered rule slices compare equal.
func snapshotType(td *typeDescriptor) typeSnapshot {
	children := make(map[string]tableSnapshot, len(td.Children))
	for _, c := range td.Children {
		children[c.Name] = snapshotTable(c)
	}
	return typeSnapshot{Table: snapshotTable(td.Table), Children: children}
}

// persistedTable is the minimal, rule-independent shape Open persists (as
// meta key "schema_tables") and reads back to rebuild the runtime table map
// for DumpTables and Rebuild's truncate step without resolving any schema:
// a reopened cache is queryable with no DAG access and no schema argument.
type persistedTable struct {
	Name       string   `json:"name"`
	PrimaryKey []string `json:"primary_key"`
}

func persistedTables(desc *schemaDescriptor) []persistedTable {
	tables := desc.allTables()
	out := make([]persistedTable, len(tables))
	for i, t := range tables {
		out[i] = persistedTable{Name: t.Name, PrimaryKey: t.PrimaryKey}
	}
	return out
}
