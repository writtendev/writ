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
// spec/schemas/schema-ops.schema.json and the resolver
// (spec.ValidateFieldRule, WRIT-203) both gate target and key components
// against this exact pattern before a rule ever reaches RulesFromSchemas, so
// this check is now a backstop, not the only guard: the generator should not
// trust its input regardless of what has already validated it upstream, and
// this is also what a built-in rule table's target or key component — never
// itself run through ValidateFieldRule — is held to. A type with a target or
// key component failing this grammar gets no tables at all: its objects fall
// to unknown_ops, the same no-winner idiom RulesFromSchemas already uses for
// a contested object_type. TestIdentPatternMatchesWireGrammar ties this copy
// to the wire pattern so the two cannot drift silently.
var identPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const identMaxLength = 64

func validIdent(s string) bool {
	return s != "" && len(s) <= identMaxLength && identPattern.MatchString(s)
}

// quoteIdent double-quotes name for embedding as a SQL identifier
// (doubling any embedded '"', the standard SQL escape, though none of
// this package's generated names ever contain one). Every generated
// table name is built from "o_" + strings.ReplaceAll(objectType, "-",
// "_") (buildTypeDescriptor) with further "__"-joined suffixes, all drawn
// from identifiers validIdent already restricts to [a-z][a-z0-9_]* — so
// the object type's own "-" is the only character that ever maps to "_",
// and "." is the only punctuation that can appear in a generated name at
// all, surviving verbatim from a WRIT-217 qualified object_type such as
// "acme.standup". Left unquoted, "o_acme.standup" parses in SQLite as
// table "standup" in schema "o_acme" rather than one table literally
// named "o_acme.standup" (hazard B, WRIT-217) — quoting at every point a
// generated name is spliced into SQL text is what keeps it meaning what
// it says regardless of what an object type is made of. This is why
// validIdent itself is never extended to admit '.': the fix is quoting
// the identifier, not widening what an unquoted one may contain.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
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
	return "CREATE TABLE " + quoteIdent(t.Name) + " (\n" + strings.Join(lines, ",\n") + "\n);"
}

func (t ddlTable) indexStatements() []string {
	var out []string
	for _, cols := range t.Indexes {
		name := "idx_" + t.Name + "_" + strings.Join(cols, "_")
		out = append(out, "CREATE INDEX "+quoteIdent(name)+" ON "+quoteIdent(t.Name)+"("+strings.Join(cols, ", ")+");")
	}
	return out
}

func (t ddlTable) orderByQuery() string {
	var parts []string
	for _, c := range t.PrimaryKey {
		parts = append(parts, c+" ASC")
	}
	return "SELECT * FROM " + quoteIdent(t.Name) + " ORDER BY " + strings.Join(parts, ", ")
}

// targetPlan describes how one installed target key (state.Rule.TargetKey())
// is materialized: a scalar column on the type table, a child table keyed by
// (object_id, item) or (object_id, idx), a column inside a keyed-lww group
// table shared with the other targets keyed on the same tuple, or — for an
// append-strategy target — a row-per-entry child table of its own (see
// AppendTable/AppendRules and writeAppendRows), mirroring the multi-value
// shape rather than sharing a table with any other target (WRIT-212).
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

	// Append. AppendRules is every rule bound to this target — never one
	// representative — pre-sorted into canonical rule order (spec/fold.md
	// §5: ascending (op_type, op_version, field)), which is the order
	// writeAppendRows applies them in when more than one matches a single
	// op. See writeAppendRows for the row shape.
	AppendTable string
	AppendRules []state.Rule
}

// canonicalRuleLess orders two rules the way spec/fold.md §5 requires rules
// sharing a target to contribute in when one operation matches more than one
// of them: ascending op_type, then op_version, then field. This is a local
// copy of engine/internal/fold's unexported ruleOrderLess rather than a call
// into that package — it is outside this package's import allowlist
// (TestImportsAllowlist), and the comparison itself is three struct fields,
// not logic worth crossing the boundary for (see opMatchesRuleLite in
// materialize.go for the same tradeoff on the match predicate).
func canonicalRuleLess(a, b state.Rule) bool {
	if a.OpType != b.OpType {
		return a.OpType < b.OpType
	}
	if a.OpVersion != b.OpVersion {
		return a.OpVersion < b.OpVersion
	}
	return a.Field < b.Field
}

// anchorColumnRef names one scalar column whose declared value_type is
// "anchor" — anchor_resolutions is driven off these, generically, rather
// than a hard-coded read of one type's own anchor column. Target is the bare
// target key (e.g. "anchor"), recorded verbatim into anchor_resolutions.target
// so a second anchor-valued target sharing the same table would still resolve
// into distinguishable rows. ObjectType is the declared type the column
// belongs to, carried through so a reader can name it (e.g. in an error).
type anchorColumnRef struct {
	Table      string
	Column     string
	Target     string
	ObjectType string
}

// typeDescriptor is everything the materializer and the generic readers need
// to know about one declared object type's generated tables.
type typeDescriptor struct {
	ObjectType string
	Table      ddlTable
	Children   []ddlTable
	Targets    map[string]*targetPlan
	// WithheldTargets are this type's target keys that got no column
	// because the projection's row shape cannot represent them — today
	// only a keyed-lww target whose bound rules disagree on Key
	// (buildTypeDescriptor's keyed-lww case, WRIT-205; unreachable from a
	// schema resolved out of the log since WRIT-234 closed the carve-out
	// that let Key disagree, but still reachable from a caller-supplied
	// rule set — see that case's own comment); an append target was the
	// other reason before WRIT-212 gave append its own row-per-entry
	// table, which has nothing left to withhold. The unit is
	// the single target and nothing wider: the type materializes normally,
	// so do the targets that merely share a table with a withheld one, and
	// so do ops that never write it. Every body field bound to one of
	// these targets lands in unknown_fields instead of being dropped —
	// the latest write per field, which is what that column's per-key
	// register can hold (spec/forward-compatibility.md §Targets a
	// projection declines).
	WithheldTargets map[string]bool
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
	// materialization plan (Children, Targets, and the rest a
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
// schemas itself, so the caller passes in an index already validated —
// every object type a consumer's schema declares, since writ hard-codes no
// object type but `schema`.
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
// identPattern, or when the type's generated table or column names collide
// with an identifier an earlier-processed type (built-in or log-declared)
// already registered. Either withholds the whole type, and buildDescriptor
// never fails the whole schema build over it. A colliding type is data
// someone else wrote — a legal object type name under op-envelope's
// grammar, such as "widget--base", can still generate a table name
// ("o_widget__base") another type already owns — and WRIT-188 round 3's
// ruling applies here just as much as there: data another writer wrote must
// never brick the repository. A withheld type's objects fall to unknown_ops
// through the same absent-typeDescriptor path an invalid target already
// takes (materializeObject).
//
// One shape withholds less than the whole type: a keyed-lww target two
// rules bind to two different Key tuples (a legal version bump,
// spec/schema-ops.md §8) — a group table's "k_"-prefixed columns are fixed
// at generation time from one key tuple, so a second, differently-shaped
// tuple has nowhere to go (WRIT-205). That target alone is withheld — no
// column — and recorded in WithheldTargets, its body fields landing in
// unknown_fields instead (spec/forward-compatibility.md §Targets a
// projection declines). Its group-mates keep their columns, and the group's
// table, built from whichever members survive, still forms as long as one
// does. The type itself, and every other target on it, materializes
// normally: declining one target must not cost a consumer every table and
// every row for the type, and it must not push ops that have nothing to do
// with that target — a create carrying only a title, say — into
// unknown_ops.
//
// An append target used to have a second, analogous unrepresentable shape —
// two rules binding one target to two different Fields under one exact
// (op_type, op_version) envelope, which the old one-row-per-op/one-column-
// per-target append table could not hold both entries of (WRIT-201). WRIT-212
// gave every append target its own row-per-entry table instead (see
// targetPlan's AppendTable/AppendRules and writeAppendRows), so that shape
// is materialized rather than withheld: there is no longer an append case
// here at all.
func buildTypeDescriptor(objectType string, rules []state.Rule, used map[string]bool) (*typeDescriptor, []anchorColumnRef, bool, error) {
	tableName := "o_" + strings.ReplaceAll(objectType, "-", "_")

	// The unit is the target key: one column or one child-table
	// participation per distinct target key, regardless of how many rules
	// (op types) address it. spec.CheckTargetAgreement guarantees every
	// rule sharing a target key agrees on strategy, lattice, and (WRIT-234)
	// key and key_types — always — but not on every other merge attribute:
	// spec/fieldrules.go's carve-out and spec/schema-ops.md §8 both let a
	// version bump of the same (op_type, field) freely change value_type
	// (also enum, max_length). WRIT-205: picking one bound rule as a "representative"
	// and taking every attribute off it (the old single-rule pick) is
	// order-dependent — whichever rule a slice happens to present first
	// decided the generated column's SQL type, and so whether another
	// version's values survived columnValue's type coercion. Every
	// attribute below is instead resolved from ALL of a target's bound
	// rules, never from one of them:
	//
	//   - Strategy: any bound rule's — spec.CheckTargetAgreement already
	//     guarantees agreement here, unconditionally, so this attribute was
	//     never actually order-dependent.
	//   - ValueType: the single declared value type when every bound rule
	//     agrees, otherwise "" (untyped). A singleton set is unchanged from
	//     today. Untyped is not a gap being papered over: it is the
	//     projection's existing first-class shape for a target with no
	//     single declared type — the same TEXT column, with a members
	//     child table under lww/create-once, that an undeclared value_type
	//     already gets (sqlType/columnValue's TEXT/toText path). Two other
	//     shapes were considered and are closed by settled documents, not
	//     by judgment here: declining the target outright is closed by
	//     spec/forward-compatibility.md §"Targets a projection declines",
	//     whose permission is conditioned on storage that "cannot express
	//     what the fold blesses" — untyped storage can, so the condition
	//     for declining is not met; a column per (target, op_version) is
	//     closed by spec/schema-ops.md §8, since Fold groups matched rules
	//     by target key alone, never by op_version, so folded state holds
	//     exactly one value per target and there is no per-version
	//     partition in it to project.
	//   - Key (keyed-lww only): the single declared key tuple when every
	//     bound keyed-lww rule agrees, otherwise the target is genuinely
	//     unrepresentable — a fixed set of "k_"-prefixed columns cannot
	//     hold two different key tuples for one target (spec/fold.md §5
	//     #8 keys each op on its own rule's key list) — and is declined
	//     through the WithheldTargets path below, which is the only
	//     decline this function makes: under the row-per-entry append
	//     table an append target has nothing left to withhold (WRIT-212).
	//     WRIT-234 closed the version-bump carve-out for Key and KeyTypes,
	//     so spec.CheckTargetAgreement now withholds a disagreeing target
	//     before RulesFromSchemas ever emits its rules — rules is this
	//     function's caller's own resolved shape, so a schema resolved out
	//     of the log can no longer reach this branch at all. It stays live
	//     for a caller that builds rules directly, bypassing that resolver
	//     (projection.WithSchema's own contract, exercised by
	//     version_bump_test.go's
	//     TestKeyedLWWVersionBumpKeyDisagreementDeclines).
	//
	// No new spec text implements any of this: value_type's mapping to a
	// SQL type and a keyed-lww target's column shape are both
	// engine-internal DDL decisions nowhere stated in spec/, so widening to
	// the already-defined "untyped" and declining an unrepresentable key
	// both apply existing rules rather than add new ones.
	type resolvedTarget struct {
		Strategy    string
		ValueType   string   // "" when bound rules disagree: untyped
		Key         []string // keyed-lww only; nil when bound rules disagree
		KeyDisagree bool     // keyed-lww only: bound rules declare different key tuples
	}
	rulesByTarget := make(map[string][]state.Rule)
	var targetKeys []string
	for _, r := range rules {
		tk := r.TargetKey()
		if _, ok := rulesByTarget[tk]; !ok {
			targetKeys = append(targetKeys, tk)
		}
		rulesByTarget[tk] = append(rulesByTarget[tk], r)
	}
	sort.Strings(targetKeys)

	resolved := make(map[string]resolvedTarget, len(targetKeys))
	for _, tk := range targetKeys {
		trs := rulesByTarget[tk]
		rt := resolvedTarget{Strategy: trs[0].Strategy, ValueType: trs[0].ValueType}
		for _, r := range trs[1:] {
			if r.ValueType != rt.ValueType {
				rt.ValueType = ""
			}
		}
		if rt.Strategy == "keyed-lww" {
			key := trs[0].Key
			keyStr := strings.Join(key, "\x00")
			agree := true
			for _, r := range trs[1:] {
				if strings.Join(r.Key, "\x00") != keyStr {
					agree = false
					break
				}
			}
			if agree {
				rt.Key = key
			} else {
				rt.KeyDisagree = true
			}
		}
		resolved[tk] = rt
	}

	for _, tk := range targetKeys {
		if !validIdent(tk) {
			return nil, nil, false, nil
		}
		if r := resolved[tk]; r.Strategy == "keyed-lww" {
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
	// once per declaration (a keyed-lww group's table is built once, after
	// every member is gathered, never once per member; an append target's
	// table is written once per target, right here, like multi-value's), so
	// any second write to a name is a genuine collision, never a legitimate
	// merge — collided withholds the whole type for it, exactly like the
	// cross-type collision identCollision already catches.
	// withheldTargets collects target keys this type declines a column for
	// entirely — populated in exactly one place, the keyed-lww case below,
	// for a target whose bound rules disagree on Key (see the
	// resolved-attribute comment above; WRIT-234 made this reachable only
	// from a caller-supplied rule set, never from a schema resolved out of
	// the log). Append has nothing left to withhold under the
	// row-per-entry table (WRIT-212), so the second populator this comment
	// used to point at is gone.
	withheldTargets := make(map[string]bool)

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
		r := resolved[tk]
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
				anchorRefs = append(anchorRefs, anchorColumnRef{Table: tableName, Column: col, Target: tk, ObjectType: objectType})
			}
			// An untyped lww or create-once target's folded value can be an
			// arbitrary JSON object — any lww or create-once target that
			// resolved to untyped (r.ValueType == "", because not every
			// bound rule declared the same non-empty value_type) — so its
			// column carries no index and a filter on it can't use
			// whole-blob equality — create-once preserves raw bytes
			// including unknown members and key order.
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
			// One row-per-entry child table of its own, mirroring
			// multi-value above rather than sharing a table with any other
			// target (WRIT-212 — before this, every append target's rule
			// set fed a separate group-formation pass below, buildAppendGroups,
			// so two targets sharing an envelope shared one table and one
			// row per op; that shared row was what made two entries for one
			// target from a single op unrepresentable, and is gone now that
			// each target has its own table and each folded list entry its
			// own row).
			//
			// AppendRules is every rule bound to tk — never resolved[tk]'s
			// single blended ValueType alone — because a version bump or a
			// second op_type agreeing on every merge attribute
			// (spec/schema-ops.md §8) can declare tk under a different field
			// name, and writeAppendRows needs the field each op's own
			// matching rule actually uses, applied via opMatchesRuleLite
			// rather than a bespoke envelope-matching predicate. Sorted into
			// canonical rule order (spec/fold.md §5: ascending (op_type,
			// op_version, field)) once here rather than at materialize time,
			// since it depends only on the rule set, not on any op.
			trs := append([]state.Rule(nil), rulesByTarget[tk]...)
			sort.Slice(trs, func(i, j int) bool { return canonicalRuleLess(trs[i], trs[j]) })
			childName := tableName + "__" + tk
			targets[tk] = &targetPlan{
				Strategy: r.Strategy, ValueType: r.ValueType,
				AppendTable: childName, AppendRules: trs,
			}
			insertChild(childName, ddlTable{
				Name: childName,
				Columns: []ddlColumn{
					{Name: "object_id", SQLType: "TEXT"},
					{Name: "op_seq", SQLType: "INTEGER"},
					{Name: "entry_idx", SQLType: "INTEGER"},
					{Name: "value", SQLType: sqlType(r.ValueType)},
				},
				PrimaryKey: []string{"object_id", "op_seq", "entry_idx"},
			})

		case "keyed-lww":
			if r.KeyDisagree {
				// Two rules bind tk under different key tuples. A keyed-lww
				// group table's "k_"-prefixed columns are fixed at
				// generation time from one key tuple: a second,
				// differently-shaped tuple has nowhere to go, and
				// spec/fold.md §5 #8 keys each op on its own rule's key
				// list, so a v2 op would mis-key into v1's columns rather
				// than merely mis-type them. Genuinely unrepresentable, so
				// declined: no column, no group-table participation, its
				// body fields land in unknown_fields instead
				// (spec/forward-compatibility.md §"Targets a projection
				// declines"). It is the only shape this function declines
				// — the append case above declines nothing (WRIT-212). Its
				// group-mates and the type itself are unaffected.
				//
				// WRIT-234 closed spec/schema-ops.md §8's version-bump
				// carve-out for Key and KeyTypes: spec.CheckTargetAgreement
				// now refuses this disagreement wholesale, so a schema
				// resolved out of the log withholds the whole target before
				// RulesFromSchemas ever emits rules for it, and this branch
				// can no longer be reached that way. It stays reachable —
				// and stays a decline, not a panic — for a caller that
				// builds rules directly and hands them to
				// projection.WithSchema without going through the resolver
				// (version_bump_test.go's
				// TestKeyedLWWVersionBumpKeyDisagreementDeclines).
				withheldTargets[tk] = true
				continue
			}
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
			r := resolved[tk]
			col := "f_" + tk
			cols = append(cols, ddlColumn{Name: col, SQLType: sqlType(r.ValueType), Indexed: r.ValueType != ""})
			if r.ValueType == "anchor" {
				anchorRefs = append(anchorRefs, anchorColumnRef{Table: g.table, Column: col, Target: tk, ObjectType: objectType})
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
		ObjectType:      objectType,
		Table:           typeTable,
		Children:        childList,
		Targets:         targets,
		WithheldTargets: withheldTargets,
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
