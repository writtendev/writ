package codec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/engine/internal/value"
	"github.com/writtendev/writ/spec"
)

// schemaIDPrefix is the schema identity convention from WRIT-73: every
// schema in spec/schemas/ declares a $id of prefix + filename.
const schemaIDPrefix = "https://writ.dev/spec/"

// envelopeSchemaFile is the payload schema every op satisfies, whatever its
// object type.
const envelopeSchemaFile = "op-envelope.schema.json"

// supportSchemaFiles are compiled as resources only: the vocabularies $ref
// them, but they are not themselves op payload schemas.
var supportSchemaFiles = []string{
	"identifiers.schema.json",
	"anchor.schema.json",
	"ordering.schema.json",
	"value-types.schema.json",
}

// vocabularySchemaFiles maps an object type to the vocabulary schema that
// governs ops on it. It is the producer's registry, and it holds exactly one
// entry: `schema` is the only object type writ hard-codes, and every other
// type is declared by a schema object written into the log
// (spec/schema-ops.md §Bootstrap).
//
// The map is exhaustive over the vocabularies shipped in spec/schemas/, and
// TestEveryShippedVocabularyIsValidated fails when a schema file is added
// without an entry — so "writ ships a schema for this type but never wired it
// up" cannot recur silently. That is what lets a lookup miss below mean one
// thing only: an object type this implementation has never heard of.
var vocabularySchemaFiles = map[string]string{
	"schema": "schema-ops.schema.json",
}

// fieldRuleVocabularies maps an object type to the field-rules.json directory
// (spec.FieldRule.Vocabulary) that declares its value types. Like
// vocabularySchemaFiles above it holds exactly one entry, for the same
// reason: the bootstrap table is the one rule table that never comes from the
// log, and it ships as spec/testdata/schema-ops/field-rules.json.
var fieldRuleVocabularies = map[string]string{
	"schema": "schema-ops",
}

// fieldRuleKey groups the value-typed rules for one (vocabulary, op_type,
// op_version) triple, mirroring how field-rules.json entries are addressed.
type fieldRuleKey struct {
	vocabulary string
	opType     string
	opVersion  int64
}

// valueTypeRulesOnce indexes spec.FieldRules() by fieldRuleKey, keeping only
// rules that declare a value_type: a rule with none is untyped
// (spec/value-types.md) and has nothing for validateValueTypes to check.
var valueTypeRulesOnce = sync.OnceValue(func() map[fieldRuleKey][]spec.FieldRule {
	rules, err := spec.FieldRules()
	if err != nil {
		panic(fmt.Sprintf("codec: loading field rules: %v", err))
	}
	idx := make(map[fieldRuleKey][]spec.FieldRule)
	for _, r := range rules {
		if r.ValueType == "" {
			continue
		}
		k := fieldRuleKey{vocabulary: r.Vocabulary, opType: r.OpType, opVersion: r.OpVersion}
		idx[k] = append(idx[k], r)
	}
	return idx
})

// vocabularyOpTypes maps an object type to the op types this build defines for
// it. It is the second half of the producer's registry, and it is what rule 4
// of spec/op-envelope.md §Producer validation is enforced from.
//
// It cannot be read out of the vocabulary schema, because the schema
// deliberately does not say it: it gates its body rules on op_version 1, so an
// op carrying an unknown op_type or a future op_version is a valid instance of
// it. That is what a reader needs (spec/forward-compatibility.md) and it is
// exactly why schema validation alone cannot catch a producer's typo — an
// op_type of "define-feild" passes schema-ops.schema.json with its body
// unexamined. TestProducerOpTypesMatchShippedVocabularies keeps this table in
// agreement with the op_type branches the schema does carry.
//
// An object type registered in vocabularySchemaFiles but missing here refuses
// every op of that type. That is deliberate: a producer that cannot say which
// op types it defines has not satisfied rule 4 for any of them, and failing
// closed on the write path costs an error message
// (spec/op-envelope.md §Producer validation).
var vocabularyOpTypes = map[string][]string{
	"schema": {"create", "define-field", "define-op", "define-type", "deprecate-field", "deprecate-type"},
}

// vocabularyOpVersion is the op version this build defines for every object
// type it hard-codes: the shipped `schema` vocabulary is at v1 and gates its
// body rules on it. When it ships a v2, this becomes a per-object-type set;
// TestShippedVocabulariesGateOnTheProducedOpVersion fails here first, so the
// change cannot be missed.
const vocabularyOpVersion int64 = 1

type compiledSchemas struct {
	envelope *jsonschema.Schema
	vocab    map[string]*jsonschema.Schema
}

var schemasOnce = sync.OnceValue(func() compiledSchemas {
	c := jsonschema.NewCompiler()

	add := func(file string) {
		raw, err := spec.FS.ReadFile("schemas/" + file)
		if err != nil {
			panic(fmt.Sprintf("codec: read schema %s: %v", file, err))
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			panic(fmt.Sprintf("codec: unmarshal schema %s: %v", file, err))
		}
		if err := c.AddResource(schemaIDPrefix+file, doc); err != nil {
			panic(fmt.Sprintf("codec: add schema resource %s: %v", file, err))
		}
	}
	compile := func(file string) *jsonschema.Schema {
		sch, err := c.Compile(schemaIDPrefix + file)
		if err != nil {
			panic(fmt.Sprintf("codec: compile schema %s: %v", file, err))
		}
		return sch
	}

	added := make(map[string]bool)
	addOnce := func(file string) {
		if !added[file] {
			added[file] = true
			add(file)
		}
	}

	addOnce(envelopeSchemaFile)
	for _, file := range supportSchemaFiles {
		addOnce(file)
	}
	for _, file := range vocabularySchemaFiles {
		addOnce(file)
	}

	vocab := make(map[string]*jsonschema.Schema, len(vocabularySchemaFiles))
	for objectType, file := range vocabularySchemaFiles {
		vocab[objectType] = compile(file)
	}

	return compiledSchemas{
		envelope: compile(envelopeSchemaFile),
		vocab:    vocab,
	}
})

// ValidateEnvelope validates raw JSON payload bytes against the op-envelope schema.
func ValidateEnvelope(raw []byte) error {
	sch := schemasOnce().envelope
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return &RejectError{Reason: RejectSchemaViolation, Err: err}
	}
	if err := sch.Validate(inst); err != nil {
		return &RejectError{Reason: RejectSchemaViolation, Err: err}
	}
	return nil
}

// OpVersionKey identifies one (op_type, op_version) pair within a single
// object type's vocabulary — the log-sourced analogue of fieldRuleKey,
// keyed by object type instead of vocabulary directory because a
// consumer-declared type has no field-rules.json directory.
type OpVersionKey struct {
	OpType    string
	OpVersion int64
}

// Vocabulary is one object type's producer-facing declaration, resolved
// from the schema objects folded from a repo's log
// (writ.VocabulariesFromSchemas). It is what the generic validator checks
// tier 2 of spec/op-envelope.md's four-tier producer precedence against,
// in place of the shipped JSON Schema + vocabularyOpTypes pair tier 1
// uses.
//
// Declared and Contested are never both true: a bare object_type bound by
// two or more schema objects installs no rules at all
// (spec/schema-ops.md §6), and RulesFromSchemas/VocabulariesFromSchemas
// share the collision pass that decides which one applies.
type Vocabulary struct {
	// Declared is true for an object_type at least one schema object
	// binds, uncontested. A type declared with no fields at all
	// (define-type or define-op alone, no define-field) is still
	// Declared: OpTypes/Fields are simply sparse, not the type's whole
	// entry absent.
	Declared bool
	// Contested is true for an object_type two or more schema objects
	// bind: the ruling's carve-out (spec/op-envelope.md §Producer
	// validation) permits the write unvalidated rather than refusing it.
	Contested bool
	// SchemaObjectID is the ObjectID of the schema object that declared
	// this type, set whenever Declared is true, so a rejection can name
	// which schema is responsible (spec/op-envelope.md §Producer
	// validation) rather than naming only the object_type.
	SchemaObjectID string
	// OpTypes is the set of (op_type, op_version) pairs rule 4 accepts:
	// named by a define-op, or by any define-field
	// (spec/schema-ops.md §4.2's generosity — "a type need not be
	// declared by define-type before a define-field or define-op names
	// it" — extends to op_type itself).
	OpTypes map[OpVersionKey]bool
	// Fields indexes every validated field rule declared for the type by
	// (op_type, op_version), regardless of whether it carries a
	// value_type: rule 3 refuses a body key with no declared rule at all
	// — there is no per-vocabulary JSON Schema bounding "known fields"
	// for a log-declared type the way there is for the bootstrap one — and
	// separately validates value_type on the rules that declare one.
	Fields map[OpVersionKey][]spec.FieldRule
}

// Vocabularies maps object type to its resolved Vocabulary. It is the
// log-sourced input BuildCommit and ValidateBody take alongside the
// engine's built-in bootstrap table for "schema"
// (spec/op-envelope.md §Producer validation).
//
// A nil Vocabularies is legal and means "the log declares nothing" — what
// a bare dag.Store, or engine/scenario's test runner, can honestly know
// without resolving anything. Looking up any key in a nil map yields the
// zero Vocabulary and ok == false, so it falls through to tier 3 (the
// contested carve-out) or tier 4 (refusal) exactly as an object type simply
// absent from a non-nil Vocabularies would.
type Vocabularies map[string]Vocabulary

// ValidateBody checks an envelope against the vocabulary that applies to
// its object type under the four-tier precedence validateProducerOp
// implements (spec/op-envelope.md §Producer validation): its op_type and
// op_version are ones that tier defines (rule 4) and its payload satisfies
// that tier's field rules (rule 3). BuildCommit calls it, so no op writ
// appends is signed without passing through here.
//
// vocabularies is the log-sourced declarations resolved once per Append
// (engine/dag's WithProducerVocabularies); nil means the log declares
// nothing, which still lets tier 3 (the contested carve-out) or tier 4
// (refusal) apply.
//
// The rules bind producers only. Nothing on the read path calls this: an op
// fetched from the log with an op type writ does not define is projected and
// preserved, never refused (spec/op-envelope.md, and see
// TestEncodePayloadDoesNotValidateBody).
func ValidateBody(env Envelope, vocabularies Vocabularies) error {
	raw := env.Raw
	if len(raw) == 0 {
		var err error
		raw, err = EncodePayload(env)
		if err != nil {
			return err
		}
	}
	return validateProducerOp(env, raw, vocabularies)
}

// validateProducerOp validates an envelope whose payload bytes are already
// encoded, so the append path does not canonicalize the same envelope
// twice. It implements the four-tier precedence from spec/op-envelope.md
// §Producer validation, exactly one tier of which ever applies to a given
// op:
//
//  1. object_type == "schema" -> the engine's built-in bootstrap table,
//     always, never the log (spec/schema-ops.md §7). This function does
//     not even consult vocabularies for "schema" — the bootstrap
//     exception is unconditional, and RulesFromSchemas/
//     VocabulariesFromSchemas both refuse to let a log schema redefine
//     it regardless.
//  2. Otherwise, vocabularies declares (and does not contest) object_type
//     -> the log-sourced declaration, and only it.
//  3. Otherwise, object_type is contested (vocabularies has an entry with
//     Contested set) -> permit the write, unvalidated. WRIT-199 makes the
//     *accidental* route into this tier unreachable for schema objects —
//     two writers bootstrapping the same namespace offline now converge
//     on one object instead of each minting one that contests the
//     other's type — but does not delete the tier itself: a
//     hand-crafted op with a random object_id and a colliding
//     object_type can still contest one deliberately, and that
//     adversarial path is out of scope for WRIT-199 by design.
//  4. Otherwise -> refuse, naming object_type.
func validateProducerOp(env Envelope, raw []byte, vocabularies Vocabularies) error {
	if env.ObjectType == "schema" {
		return validateAgainstBootstrap(env, raw)
	}

	// Indexing a nil or non-matching map yields the zero Vocabulary, whose
	// Declared and Contested are both false — the same "nothing resolved"
	// state tier 4 already falls through on, so there is no separate
	// "found" bit to track. Declared and Contested are never both true
	// (see Vocabulary's doc comment), so checking each in turn is
	// exhaustive.
	voc := vocabularies[env.ObjectType]
	if voc.Declared {
		return validateAgainstLogVocabulary(env, raw, voc)
	}
	if voc.Contested {
		// Tier 3: the ruling's carve-out (spec/op-envelope.md §Producer
		// validation). Nothing to check here: rules 1 and 2 of the
		// envelope schema and canonical-encoding check already ran in
		// BuildCommit before this was ever reached, and no conforming
		// reader will interpret these ops until the contest resolves —
		// WRIT-199 makes the accidental route to a contested schema type
		// unreachable, but a deliberately hand-crafted contest is still
		// possible and still lands here — but a permanent write outage is
		// the alternative, and that trade is not this function's call to
		// make.
		return nil
	}
	return fmt.Errorf("codec: object_type %q is not declared by any schema in the log: spec/op-envelope.md §Producer validation rule 3/4", env.ObjectType)
}

// validateAgainstBootstrap is tier 1: `schema` ops are validated against
// writ's own built-in tables (vocabularySchemaFiles, vocabularyOpTypes,
// fieldRuleVocabularies), which is the bootstrap the log cannot supply
// because a schema object has to exist before anything else can be typed.
func validateAgainstBootstrap(env Envelope, raw []byte) error {
	sch, ok := schemasOnce().vocab[env.ObjectType]
	if !ok {
		return nil
	}
	if err := validateOpTypeAndVersion(env); err != nil {
		return err
	}
	if err := validateAgainst(sch, raw); err != nil {
		return err
	}
	return validateValueTypes(env, raw)
}

// validateAgainstLogVocabulary is tier 2: rule 4 (op_type/op_version
// declared) and rule 3 (every body field declared, and value-typed ones
// valid) checked against a log-sourced Vocabulary rather than a shipped
// JSON Schema. There is no per-vocabulary schema bounding "known fields"
// for a consumer-declared type, so — unlike validateValueTypes below — an
// undeclared field is itself a rejection, not a silent skip.
func validateAgainstLogVocabulary(env Envelope, raw []byte, voc Vocabulary) error {
	key := OpVersionKey{OpType: env.OpType, OpVersion: env.OpVersion}
	if !voc.OpTypes[key] {
		return fmt.Errorf("codec: op_type %q at op_version %d is not declared by schema object %q for object_type %q: spec/op-envelope.md §Producer validation rule 4",
			env.OpType, env.OpVersion, voc.SchemaObjectID, env.ObjectType)
	}

	var decoded struct {
		Body map[string]any `json:"body"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return &RejectError{Reason: RejectSchemaViolation, Err: err}
	}
	if err := validateFieldsAgainstRules(voc.Fields[key], decoded.Body, true); err != nil {
		return fmt.Errorf("schema object %q: %w", voc.SchemaObjectID, err)
	}
	return nil
}

// validateValueTypes is the second half of producer rule 3
// (spec/op-envelope.md §Producer validation): once the vocabulary schema
// passes, every field with a declared value_type (spec/value-types.md) must
// hold a value conforming to it. A field with no declared value_type is
// untyped and skipped, and an object type or (op_type, op_version) with no
// value-typed rules at all is a no-op — this is additive to what the schema
// already checks, not a replacement for it.
//
// rules below is valueTypeRulesOnce()'s filtered set — only rules declaring
// a value_type — not the full bootstrap rule table, so validateFieldsAgainstRules'
// rule 6 (WRIT-222, its own doc comment) never runs at all here for the four
// untyped members of define-field's own rules: enum, key, key_types, and
// lattice (spec/testdata/schema-ops/field-rules.json). That is not a live
// gap in rule 6 only because spec/schemas/schema-ops.schema.json already
// types those four members "array"/"object", and a JSON Schema type
// constraint never admits null on its own — so a null there is refused by
// the shipped schema before validateValueTypes would ever see the body, and
// producer/reader lockstep holds regardless of whether rule 6 itself runs
// for these names. This is a considered decision, not an oversight to
// widen later: dispatch decided against extending this filter to the full
// bootstrap rule table (that would let rule 6 run for these four names too,
// but the shipped schema already closes the gap, so it would add coverage
// with no behavior change). See
// TestBootstrapDefineFieldRefusesNullForUntypedMembersViaShippedSchema
// (engine/codec/producer_test.go) for the fixture pinning that all four
// names are actually refused by the shipped schema, not merely by
// construction.
//
// Nothing on the read path calls this: ValidateBody's contract ("the rules
// bind producers only") is unchanged.
func validateValueTypes(env Envelope, raw []byte) error {
	vocab, ok := fieldRuleVocabularies[env.ObjectType]
	if !ok {
		return nil
	}
	rules := valueTypeRulesOnce()[fieldRuleKey{vocabulary: vocab, opType: env.OpType, opVersion: env.OpVersion}]
	if len(rules) == 0 {
		return nil
	}

	var decoded struct {
		Body map[string]any `json:"body"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return &RejectError{Reason: RejectSchemaViolation, Err: err}
	}
	return validateFieldsAgainstRules(rules, decoded.Body, false)
}

// validateFieldsAgainstRules checks a decoded op body's fields against a
// declared field-rule set for one (op_type, op_version). A field whose rule
// declares a value_type must hold a value conforming to it
// (spec/value-types.md); a body key naming neither a declared field nor a
// keyed-lww key column of one of rules (see keyed-lww key columns below) is
// skipped when strict is false (the bootstrap tier, where the shipped JSON
// Schema already bounds which fields are known and validateValueTypes'
// rules are only the value-typed subset) and rejected when strict is true
// (the log-sourced tier, where nothing else bounds "known fields" —
// validateAgainstLogVocabulary's rules are every declared field).
//
// Before any of that, a presence pass enforces rule 5
// (spec/op-envelope.md §Producer validation, WRIT-219): a body writing a
// keyed-lww field must also carry every column of that field's declared
// key, or the write is refused outright. See that pass's own comment
// below for why it is keyed directly off each rule rather than byField.
//
// Separately, the per-field loop enforces rule 6 (spec/op-envelope.md
// §Producer validation, WRIT-222): a declared field's own value MUST NOT
// be JSON null, under every merge strategy, for every rule this function
// receives in rules. This is a different check from rule 5's, keyed on a
// different thing -- rule 5 keys on a keyed-lww key column being absent
// from the body, rule 6 keys on a declared field's value being null -- and
// the two stay independent rather than merging into one pass. See the
// val == nil check further down for the rule 6 rejection itself, and
// validateValueTypes's own doc comment for the one caller that hands this
// function a deliberately narrowed rules -- the bootstrap tier's
// value-typed subset, not the full rule table -- and why that narrowing
// does not reopen this rule for the names it excludes.
//
// A keyed-lww field's key columns travel in the body but are not themselves
// declared fields (spec/op-envelope.md §Producer validation rule 3): a body
// key is also declared when it is a member of key on some keyed-lww rule in
// rules for this same (op_type, op_version), and its value is checked
// against that rule's key_types entry the same way a field's value is
// checked against its value_type, plus one stricter requirement — the value
// MUST be a JSON string regardless of key_types, because fold's keyed-lww
// strategy treats a non-string key component as uninterpretable
// (spec/fold.md §5's "Key components are strings", enforced via §7.1;
// engine/internal/fold/reject.go). A JSON null key column is refused here
// the same way a declared field's null value is refused below (WRIT-222,
// rule 6): a key column addresses a register rather than carrying a value
// of its own, and fold's keyed-lww strategy does not tolerate null there
// either (isString(nil) is false, so fold.ruleAccepts rejects it) —
// validateKeyColumnValue runs on every key column value unconditionally,
// null included, independently of the field-value check below.
//
// Where a name is both a declared field and a key column of some rule
// (writ's own "schema" vocabulary's define-field's "field" does this, and so
// does any consumer schema that reuses a field name as another rule's key),
// the field rule governs the value's declared *type* — byField is checked
// first below, so the field's own value_type is what typechecks the value,
// never the key column's key_types entry — but this is a union, not an
// override: fold.ruleAccepts checks every keyed-lww key column present in
// the body regardless of whether that name also carries a field rule, so
// the bare "MUST be a JSON string" floor above still applies on top of the
// field's own check. Skipping that floor here is exactly the hole WRIT-214
// round 3 found: a field typed `int` that doubles as another rule's key
// column would otherwise be accepted by the producer (satisfying its own
// value_type) and quarantined by every reader (failing the key-column
// floor) — the same producer/reader lockstep break the null case above was
// fixed for, on the one path a plain byField-then-key-column dispatch
// cannot see.
//
// The floor and the field's own check can still disagree on what "the
// value" even is: once the floor confirms val is a JSON string, a field
// whose own value_type is int, number, bool, or anchor cannot typecheck
// that string directly (a JSON string is never a conforming JSON integer,
// number, boolean, or object) — the string instead carries the field's
// ordinary encoding as text, the same "content" relationship
// validateKeyColumnValue already applies to a key-column-only name. Round
// 4 found this half of the union left unfixed: the code below decodes that
// content the same way before checking it against r.ValueType, so the two
// checks are jointly satisfiable instead of permanently refusing every
// value.
//
// `tombstone` is the one strategy no decode ever reconciles with the floor,
// which is why it gets its own check ahead of everything else here rather
// than joining the r.ValueType-keyed decode logic: every other
// strategy either stores the body value verbatim without caring about its
// JSON shape (lww, create-once, keyed-lww, append) or already requires a
// JSON string itself (set-union, set-observed-remove, lattice, multi-value)
// — a keyed-lww key column's confirmed-string floor satisfies all of them —
// but fold's tombstone reducer requires the *raw* body value to already be
// a JSON boolean (engine/internal/fold/reject.go's `v.(bool)`), never a
// string carrying "true"/"false" as decoded content. Since the floor and
// tombstone's own requirement are mutually exclusive JSON shapes for the
// very same body value, no value a producer could write ever satisfies
// both, whatever r.ValueType says (tombstone's is "" or "bool", never
// anything decode-eligible) — round 4's decode branch nonetheless decoded
// and accepted `{"tag":"true"}` for a bool-typed tombstone field, signing
// an op every reader's fold.Uninterpretable then quarantines. The fix is to
// refuse every value up front for this combination, the same conclusion
// fold reaches on the read side, instead of letting the decode step
// discover a false positive.
//
// A schema resolved from the log never reaches this branch: round 5 left the
// combination declarable and refused it once per write, which is the same
// declarable-but-unwritable shape WRIT-214 exists to remove, only relocated,
// so spec.CheckKeyColumnAgreement now refuses it where the schema resolves
// (engine/schema.go's resolveSchemaTypes) and withholds both rules, and
// spec.FieldRules refuses it in writ's own bootstrap tables the same way. The
// check stays here because codec.Vocabularies is a public shape a caller can
// build directly, without going through either resolver: the producer must
// never accept what every reader quarantines, whoever assembled the rules.
func validateFieldsAgainstRules(rules []spec.FieldRule, body map[string]any, strict bool) error {
	byField := make(map[string]spec.FieldRule, len(rules))
	keyColumnTypes := make(map[string]string)
	for _, r := range rules {
		byField[r.Field] = r
		if r.Strategy == "keyed-lww" {
			for col, kt := range r.KeyTypes {
				keyColumnTypes[col] = kt
			}
		}
	}

	// Rule 5 (spec/op-envelope.md §Producer validation): a body that writes
	// a keyed-lww field MUST also carry every column of that field's
	// declared key, or the op folds onto the empty key and the field's
	// declared per-key partition silently degenerates to plain lww with no
	// error at any layer (WRIT-219). This pass runs first and separately
	// from the per-field loop below, over rules directly rather than
	// byField, so a body missing a key column is refused for that reason
	// rather than by whichever per-value check happens to fire first.
	//
	// Checked off each rule with Strategy == "keyed-lww" and a non-empty
	// Key, not off a byField lookup: a body's own rule set can carry a
	// second FieldRule for the very same field name with Strategy
	// "keyed-lww" but no Key -- a key-column-only entry synthesized for a
	// dual-role name, the way engine/projection/refresh_test.go's
	// vocabulariesFrom does for its own test fixtures -- and byField's
	// last-rule-wins map would let that vacuous entry win the lookup for
	// such a name, so a check keyed off byField[field].Key would see an
	// empty key list and pass vacuously. Filtering on len(r.Key) > 0 and
	// reading Key straight off r sidesteps the map entirely.
	type missingKeyColumn struct {
		field  string
		column string
	}
	var missing []missingKeyColumn
	for _, r := range rules {
		if r.Strategy != "keyed-lww" || len(r.Key) == 0 {
			continue
		}
		if _, wrote := body[r.Field]; !wrote {
			continue
		}
		for _, col := range r.Key {
			if _, present := body[col]; !present {
				// Only the first missing column per field, in Key's
				// declared order -- the acceptance criterion is refusal
				// naming the field and *a* missing column, not every one.
				missing = append(missing, missingKeyColumn{field: r.Field, column: col})
				break
			}
		}
	}
	if len(missing) > 0 {
		// Sorted by field, the same determinism discipline the per-field
		// loop below applies to body's keys: two conforming
		// implementations -- and two runs of this one -- must name the
		// same field and column first when more than one is missing.
		sort.Slice(missing, func(i, j int) bool { return missing[i].field < missing[j].field })
		m := missing[0]
		return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: keyed-lww requires key %q, which body omits", m.field, m.column)}
	}

	// Sorted rather than ranged directly: body is a JSON-decoded map, whose
	// Go iteration order is randomized, and two conforming implementations
	// (or two runs of the same one) must report the same field first when
	// more than one is invalid.
	fields := make([]string, 0, len(body))
	for field := range body {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	for _, field := range fields {
		val := body[field]
		r, ok := byField[field]
		if !ok {
			if kt, isKeyColumn := keyColumnTypes[field]; isKeyColumn {
				if err := validateKeyColumnValue(kt, val); err != nil {
					return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: %w", field, err)}
				}
				continue
			}
			if strict {
				return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q has no declared rule in the schema", field)}
			}
			continue
		}
		// field also names a keyed-lww key column of some rule: the field
		// rule above governs its declared type, but fold.ruleAccepts checks
		// every key column present in the body regardless of whether the
		// name also carries a field rule, so the bare JSON-string floor
		// applies here too, unconditionally — including when val is nil.
		// The field branch below refuses a null field value too (WRIT-222,
		// rule 6), but for a different, field-scoped reason and with a
		// different message naming the field rather than the key-column
		// floor -- this key-column check runs first, so a name playing both
		// roles is refused here, before that later check ever sees the
		// value.
		_, isKeyColumn := keyColumnTypes[field]
		if isKeyColumn {
			if _, isStr := val.(string); !isStr {
				return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: key column value must be a JSON string (spec/fold.md §5 keyed-lww)", field)}
			}
			// tombstone is the one strategy the floor above and the
			// strategy's own reducer can never jointly satisfy (see this
			// function's doc comment; a resolver refuses the combination
			// outright, so only a directly-built Vocabularies gets here):
			// fold requires a raw JSON boolean,
			// the floor just confirmed val is a JSON string, and those are
			// two different JSON values, not two encodings of the same
			// one. Refusing here, ahead of the val==nil/ValueType=="" skip
			// below, covers a blank ValueType too — tombstone's is legally
			// "" or "bool" (spec.FieldRule's own ValidateFieldRule), and
			// blank would otherwise reach that skip and pass through
			// unchecked.
			if r.Strategy == "tombstone" {
				return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: a tombstone field cannot also be a keyed-lww key column of another rule: tombstone's reducer requires a raw JSON boolean (engine/internal/fold/reject.go), which is never the JSON string a keyed-lww key column's value must be (spec/fold.md §5) -- no value ever satisfies both, so every write to this field is refused", field)}
			}
		}
		// A declared field's value MUST NOT be JSON null, under every merge
		// strategy, no carve-outs (spec/op-envelope.md §Producer validation
		// rule 6, WRIT-222): omitting the field already means "this op
		// asserts nothing about this field", so null would only be a second
		// spelling of the same absence, and engine/internal/fold/reject.go
		// quarantines a null field value for every one of the nine
		// catalogue strategies -- an accept here is a producer/reader
		// lockstep break, not a value a reader can fold. This binds an
		// untyped field too (r.ValueType == ""), which is why it runs ahead
		// of that skip rather than after it: a rule with no declared
		// value_type typechecks nothing, but "untyped" must not mean "null
		// accepted". It does not reach a null nested *inside* a structured
		// value (an anchor's interior, an object written via -field-json)
		// -- that is the value type's own business, checked by
		// value.Validate below, not this field-level presence check.
		if val == nil {
			return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: value must not be JSON null; omit the field instead to write nothing (spec/op-envelope.md §Producer validation rule 6)", field)}
		}
		if r.ValueType == "" {
			continue
		}
		// val is confirmed a JSON string above by the key-column floor. When
		// the field's own value_type is itself string-shaped (including
		// "enum": a field's own enum member is always a JSON string), that
		// string IS the value to typecheck, same as any other field. But
		// when r.ValueType is int/number/bool/anchor — the same four
		// catalogue members validateKeyColumnValue decodes for a
		// key-column-only name — the string instead carries r.ValueType's
		// ordinary encoding as text (spec/op-envelope.md's "or when its
		// value is encoded as the string form the paragraph below
		// describes"), so it must be decoded the same way before
		// value.Validate runs, or the field's own type check and the
		// key-column floor above are mutually unsatisfiable and the field
		// becomes permanently unwritable — WRIT-214 round 4's finding on
		// this exact branch. `timestamp` needs no unwrap step (its ordinary
		// encoding is already a JSON string, same as validateKeyColumnValue
		// treats it) but does need the same canonical-spelling floor a
		// key-column-only timestamp gets — see validateCanonicalTimestamp.
		checkVal := val
		if isKeyColumn && r.ValueType != "enum" {
			switch {
			case r.ValueType == "timestamp":
				if err := validateCanonicalTimestamp(val.(string)); err != nil {
					return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: %w", field, err)}
				}
			case !keyColumnStringEncoded[r.ValueType]:
				decoded, err := canonicalKeyColumnContent(r.ValueType, val.(string))
				if err != nil {
					return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: %w", field, err)}
				}
				checkVal = decoded
			}
		}
		params := value.Params{Enum: r.Enum, MaxLength: r.MaxLength}
		// set-union/set-observed-remove type the elements, not the array; a
		// bare scalar (e.g. the scalar-shaped add-issue/remove-issue vector,
		// spec/testdata/fold/merge/set-observed-remove-scalar.json) is
		// validated as a single element, matching the accumulators' own
		// flexibility (engine/internal/fold/strategy.go).
		if r.Strategy == "set-union" || r.Strategy == "set-observed-remove" {
			if items, ok := checkVal.([]any); ok {
				for _, item := range items {
					if err := value.Validate(r.ValueType, params, item); err != nil {
						return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: %w", field, err)}
					}
				}
				continue
			}
		}
		if err := value.Validate(r.ValueType, params, checkVal); err != nil {
			return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: %w", field, err)}
		}
	}
	return nil
}

// keyColumnStringEncoded is the set of catalogue value types whose ordinary
// wire encoding (spec/value-types.md's "Encoding" column) already is a JSON
// string AND whose own validation already admits exactly one spelling per
// value: value.Validate's cases for these all start with `v.(string)`, so a
// key column's raw body value — already confirmed to be a JSON string below
// — IS the value to typecheck, unchanged, with no further canonical-form
// requirement needed on top.
//
// `timestamp` is deliberately not in this set despite also being
// string-encoded (round 5): RFC 3339 admits more than one spelling of the
// same instant, so it gets validateCanonicalTimestamp's own canonical-form
// check instead — see that function's doc comment.
var keyColumnStringEncoded = map[string]bool{
	"string":     true,
	"text":       true,
	"person-ref": true,
	"object-ref": true,
	"git-oid":    true,
	"position":   true,
}

// validateKeyColumnValue checks one keyed-lww key column's value: it MUST be
// a JSON string, regardless of what valueType (the rule's key_types entry
// for this column) says, because fold's keyed-lww strategy treats a
// non-string key component as uninterpretable (spec/fold.md §5's "Key
// components are strings", enforced via §7.1) — spec/schema-ops.md §3.1's
// op_version-as-decimal-string encoding is this exact rule already applied
// to the bootstrap's own key columns, not a new constraint invented here —
// and its content MUST additionally be a valid encoding of valueType,
// checked the same way a field's value is (spec/value-types.md).
//
// "Content", not "raw value", is the operative word, and it is what §3.1
// already establishes: `op_version`'s catalogue encoding is a JSON integer,
// yet it travels as the decimal string "1", because the JSON-string
// requirement above wins over the type's own ordinary encoding wherever a
// value plays both roles. `int`, `number`, `bool` and `anchor` are exactly
// the catalogue members whose ordinary encoding (JSON integer, number,
// boolean, object) is not itself a JSON string — keyColumnStringEncoded is
// everything else, whose encoding already is a JSON string, so the
// confirmed-string val needs no further decoding to typecheck. For the rest,
// the key column's string content is read as JSON text — "7", "3.5",
// "true", or an anchor object's compact JSON encoding — and the decoded
// value is what value.Validate checks, exactly as it would a field's own
// JSON-typed value. This is not a widening of key_types (WRIT-214 round 3
// declined that): it is §3.1's rule, generalized from `int` to every
// non-string-shaped catalogue member, so a schema declaring
// `key(seq int)` is writable instead of permanently refusing every value
// while never being refused itself.
//
// The content must be canonicaljson's own encoding of itself, not merely
// text that parses (WRIT-214 round 4): "7", "7.0", " 7", "1e3", and "-0"
// all decode to the same JSON number, but fold keys a keyed-lww register on
// the raw string, so accepting every spelling would let semantically equal
// keys address different registers that never converge. See
// canonicalKeyColumnContent.
//
// valueType "enum" is the one catalogue member no amount of decoding fixes:
// value.Validate requires the declared member list (value.Params.Enum) to
// check membership, and key_types (spec/value-types.md, a column name ->
// catalogue type name map) has no slot for one — unlike a field's own
// value_type "enum", which always travels with the rule's own enum
// attribute. There is deliberately no key-column-scoped member list to add
// one (spec/schema-ops.md §11's key_types shape is not being widened for
// this), so an enum-typed key column is held to the JSON-string requirement
// above and nothing more: that is already every check value.Validate would
// otherwise run for "enum" beyond membership, so this is not a narrower
// check than any other key_types entry gets, only one that cannot also
// bound the value to a closed set.
func validateKeyColumnValue(valueType string, val any) error {
	s, ok := val.(string)
	if !ok {
		return fmt.Errorf("key column value must be a JSON string (spec/fold.md §5 keyed-lww)")
	}
	if valueType == "enum" {
		return nil
	}
	if valueType == "timestamp" {
		return validateCanonicalTimestamp(s)
	}
	if keyColumnStringEncoded[valueType] {
		return value.Validate(valueType, value.Params{}, val)
	}
	// valueType's ordinary encoding is not a JSON string (int, number, bool,
	// anchor): the confirmed string above carries that encoding as text, not
	// the value itself, so it is decoded back to JSON before typechecking.
	decoded, err := canonicalKeyColumnContent(valueType, s)
	if err != nil {
		return err
	}
	return value.Validate(valueType, value.Params{}, decoded)
}

// validateCanonicalTimestamp checks a keyed-lww key column's (or a
// dual-role field's) timestamp value: it must conform to value_type
// `timestamp` (spec/value-types.md) AND already be in that instant's one
// canonical spelling (round 5), the same standard canonicalKeyColumnContent
// holds int/number/bool/anchor to, applied here to a value that is already
// a JSON string rather than needing one more decode step first.
//
// Without the second check, RFC 3339 lets "2024-01-01T00:00:00Z",
// "2024-01-01T01:00:00+01:00", and "2024-01-01T00:00:00.000Z" name the same
// instant with three different byte strings, and fold's keyed-lww strategy
// keys a register on the byte string, not the instant it denotes
// (value.Normalize is the identity for timestamp — only person-ref
// normalizes, engine/internal/value/value_test.go's
// TestNormalizeOnlyPersonRef) — so three producers who mean the same key
// would address three registers that can never converge, exactly the
// failure canonicalKeyColumnContent closes for the other non-string-shaped
// catalogue members.
//
// canonicalTimestamp defines the one accepted spelling: UTC (a `Z` offset,
// never a numeric one) with fractional seconds present only when nonzero
// and written with no trailing zero digits. time.Parse is used ahead of
// value.Validate's own regex (spec/value-types.md's
// `$defs/timestamp` pattern) because the regex, like any fixed-width
// pattern, accepts calendar nonsense (month 13, a February 30th) that a
// real calendar parse refuses — a stricter rule for a value this function
// is also about to reformat and byte-compare, not a relaxation of what
// value.Validate already enforces elsewhere.
func validateCanonicalTimestamp(s string) error {
	if err := value.Validate("timestamp", value.Params{}, s); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("key column value %q is not a valid RFC 3339 timestamp: %w", s, err)
	}
	if canon := canonicalTimestamp(t); canon != s {
		return fmt.Errorf("key column value %q is not the canonical timestamp encoding of the same instant (want %q): spec/op-envelope.md §Producer validation", s, canon)
	}
	return nil
}

// canonicalTimestamp is validateCanonicalTimestamp's one accepted spelling
// for an instant: UTC, with time.RFC3339Nano's "9"-run fractional-second
// digits, which Go's time.Format already trims to the shortest spelling
// (dropping the fraction entirely when it is exactly zero) — the same
// "shortest round-tripping spelling" discipline spec/canonicalization.md
// holds JSON numbers to, applied here to an instant instead of a number.
func canonicalTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// canonicalKeyColumnContent decodes a non-string-shaped key column's (or a
// dual-role field's) JSON-string content — confirmed a string by the
// caller's floor check — into the JSON value valueType's own encoding
// checks against.
//
// A bare json.Unmarshal is not enough: spec/op-envelope.md is explicit that
// the content is "the decimal string \"1\"" and "a compact JSON object's
// text", not merely some JSON text that happens to parse to the right
// shape — the same standard spec/schema-ops.md §3.1 already holds
// op_version to. "7", "7.0", " 7", "7 ", "1e3", and "-0" all parse to the
// same number, but they are six different byte strings, and fold's
// keyed-lww accumulator keys a register on the byte string, not the number
// it denotes (engine/internal/fold/strategy.go's value.Normalize is the
// identity for everything but person-ref) — so admitting more than one
// spelling here would let two producers who mean the same key write to two
// registers that can never converge, the opposite of what a keyed-lww
// column exists for. Reusing canonicaljson — the codec's own existing
// standard for "the one true encoding of a JSON value" — rather than
// inventing a second canonicalization concept: content is accepted only
// when it already IS canonicaljson's encoding of itself, byte for byte,
// which also rejects leading/trailing whitespace and (for anchor) a
// reordered or non-compact object, since canonicaljson sorts object
// members and writes no insignificant whitespace.
func canonicalKeyColumnContent(valueType, s string) (any, error) {
	canon, err := canonicaljson.Marshal([]byte(s))
	if err != nil {
		return nil, fmt.Errorf("key column value %q is not a valid JSON encoding of value_type %q: %w", s, valueType, err)
	}
	if string(canon) != s {
		return nil, fmt.Errorf("key column value %q is not the canonical JSON encoding of value_type %q (want %q)", s, valueType, canon)
	}
	var decoded any
	if err := json.Unmarshal(canon, &decoded); err != nil {
		return nil, fmt.Errorf("key column value %q is not a valid JSON encoding of value_type %q: %w", s, valueType, err)
	}
	return decoded, nil
}

// validateOpTypeAndVersion is producer rule 4: an op_type or op_version writ
// does not define for an object type it does define is a typo, and the op it
// would write is one no reader — writ's own included — will ever interpret.
//
// It runs before the body check because a body cannot be meaningfully judged
// against an op type the build has no rules for; that is precisely the case
// where the vocabulary schema examines nothing and passes.
func validateOpTypeAndVersion(env Envelope) error {
	defined := vocabularyOpTypes[env.ObjectType]
	if !slices.Contains(defined, env.OpType) {
		return fmt.Errorf("codec: op_type %q is not one this build defines for object_type %q (defined: %s): spec/op-envelope.md §Producer validation rule 4",
			env.OpType, env.ObjectType, strings.Join(defined, ", "))
	}
	if env.OpVersion != vocabularyOpVersion {
		return fmt.Errorf("codec: op_version %d is not one this build defines for object_type %q (defined: %d): spec/op-envelope.md §Producer validation rule 4",
			env.OpVersion, env.ObjectType, vocabularyOpVersion)
	}
	return nil
}

func validateAgainst(sch *jsonschema.Schema, raw []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return &RejectError{Reason: RejectSchemaViolation, Err: err}
	}
	if err := sch.Validate(inst); err != nil {
		return &RejectError{Reason: RejectSchemaViolation, Err: err}
	}
	return nil
}

// Disposition represents whether an op is interpretable or opaque to a reader.
type Disposition string

const (
	DispositionInterpretable Disposition = "interpretable"
	DispositionOpaque        Disposition = "opaque"
)

// KnownOp defines an (object_type, op_type) pair and its supported versions.
type KnownOp struct {
	ObjectType string  `json:"object_type"`
	OpType     string  `json:"op_type"`
	Versions   []int64 `json:"versions"`
}

// Profile represents a reader capability profile defining known operations.
type Profile struct {
	Name     string    `json:"profile,omitempty"`
	KnownOps []KnownOp `json:"known_ops"`
}

// Classify determines whether an envelope is interpretable or opaque under the profile.
func (p Profile) Classify(env Envelope) Disposition {
	for _, k := range p.KnownOps {
		if k.ObjectType == env.ObjectType && k.OpType == env.OpType {
			for _, v := range k.Versions {
				if v == env.OpVersion {
					return DispositionInterpretable
				}
			}
			return DispositionOpaque
		}
	}
	return DispositionOpaque
}
