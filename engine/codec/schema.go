package codec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
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
// declared field-rule set for one (op_type, op_version). A field whose
// rule declares a value_type must hold a value conforming to it
// (spec/value-types.md); a field with no declared rule at all is skipped
// when strict is false (the bootstrap tier, where the shipped JSON Schema
// already bounds which fields are known and validateValueTypes'
// rules are only the value-typed subset) and rejected when strict is true
// (the log-sourced tier, where nothing else bounds "known fields" —
// validateAgainstLogVocabulary's rules are every declared field).
func validateFieldsAgainstRules(rules []spec.FieldRule, body map[string]any, strict bool) error {
	byField := make(map[string]spec.FieldRule, len(rules))
	for _, r := range rules {
		byField[r.Field] = r
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
			if strict {
				return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q has no declared rule in the schema", field)}
			}
			continue
		}
		if val == nil || r.ValueType == "" {
			continue
		}
		params := value.Params{Enum: r.Enum, MaxLength: r.MaxLength}
		// set-union/set-observed-remove type the elements, not the array; a
		// bare scalar (e.g. the scalar-shaped add-issue/remove-issue vector,
		// spec/testdata/fold/merge/set-observed-remove-scalar.json) is
		// validated as a single element, matching the accumulators' own
		// flexibility (engine/internal/fold/strategy.go).
		if r.Strategy == "set-union" || r.Strategy == "set-observed-remove" {
			if items, ok := val.([]any); ok {
				for _, item := range items {
					if err := value.Validate(r.ValueType, params, item); err != nil {
						return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: %w", field, err)}
					}
				}
				continue
			}
		}
		if err := value.Validate(r.ValueType, params, val); err != nil {
			return &RejectError{Reason: RejectSchemaViolation, Err: fmt.Errorf("field %q: %w", field, err)}
		}
	}
	return nil
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
