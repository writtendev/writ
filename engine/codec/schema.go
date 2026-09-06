package codec

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
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
}

// vocabularySchemaFiles maps an object type to the vocabulary schema that
// governs ops on it. It is the producer's registry: every object type writ
// can emit MUST appear here, or ops of that type are signed against nothing.
//
// The map is exhaustive over the vocabularies shipped in spec/schemas/, and
// TestEveryShippedVocabularyIsValidated fails when a schema file is added
// without an entry — so "writ ships a schema for this type but never wired it
// up" cannot recur silently. That is what lets a lookup miss below mean one
// thing only: an object type this implementation has never heard of.
var vocabularySchemaFiles = map[string]string{
	"review":         "review-ops.schema.json",
	"comment":        "comment.schema.json",
	"issue":          "issue-ops.schema.json",
	"project":        "project-ops.schema.json",
	"cycle":          "cycle-ops.schema.json",
	"workflow-state": "workflow-state-ops.schema.json",
	"label":          "label-ops.schema.json",
	"document":       "document-ops.schema.json",
	"section":        "document-ops.schema.json",
	"settings":       "settings-ops.schema.json",
}

// vocabularyOpTypes maps an object type to the op types this build defines for
// it. It is the second half of the producer's registry, and it is what rule 4
// of spec/op-envelope.md §Producer validation is enforced from.
//
// It cannot be read out of the vocabulary schemas, because the schemas
// deliberately do not say it: all six gate their body rules on op_version 1, so
// an op carrying an unknown op_type or a future op_version is a valid instance
// of them. That is what a reader needs (spec/forward-compatibility.md) and it
// is exactly why schema validation alone cannot catch a producer's typo — an
// op_type of "aproval" passes review-ops.schema.json with its body unexamined.
// TestProducerOpTypesMatchShippedVocabularies keeps this table in agreement
// with the op_type branches the schemas do carry.
//
// An object type registered in vocabularySchemaFiles but missing here refuses
// every op of that type. That is deliberate: a producer that cannot say which
// op types it defines has not satisfied rule 4 for any of them, and failing
// closed on the write path costs an error message
// (spec/op-envelope.md §Producer validation).
var vocabularyOpTypes = map[string][]string{
	"review":         {"approval", "assign", "ci-status", "create", "label", "link", "revision", "set-status", "update"},
	"comment":        {"create", "delete", "edit", "resolve"},
	"issue":          {"assign", "create", "label", "link", "set-state", "update"},
	"project":        {"add-issue", "create", "remove-issue", "set-status", "update"},
	"cycle":          {"add-issue", "create", "remove-issue", "set-dates", "update"},
	"workflow-state": {"create", "update"},
	"label":          {"create", "update"},
	"document":       {"create", "label", "link", "update"},
	"section":        {"create", "delete", "edit", "move", "update"},
	"settings":       {"set"},
}

// vocabularyOpVersion is the op version this build defines for every object
// type: all six shipped vocabularies are at v1 and gate their body rules on it.
// When one of them ships a v2, this becomes a per-object-type set;
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

// ValidateBody checks an envelope against the vocabulary registered for its
// object type: its op_type and op_version are ones this build defines (rule 4)
// and its payload satisfies the vocabulary schema (rule 3), both from
// spec/op-envelope.md §Producer validation. BuildCommit calls it, so no op writ
// appends is signed without passing through here.
//
// An object type with no registered vocabulary passes. That is forward
// compatibility, not a hole: a type this implementation has never heard of is
// something a reader must tolerate (spec/forward-compatibility.md) and
// something writ's own producer never emits — every type it does emit is in
// vocabularySchemaFiles, which is tested exhaustive over spec/schemas/.
//
// The rules bind producers only. Nothing on the read path calls this: an op
// fetched from the log with an op type writ does not define is projected and
// preserved, never refused (spec/op-envelope.md, and see
// TestEncodePayloadDoesNotValidateBody).
func ValidateBody(env Envelope) error {
	if _, ok := schemasOnce().vocab[env.ObjectType]; !ok {
		return nil
	}
	raw := env.Raw
	if len(raw) == 0 {
		var err error
		raw, err = EncodePayload(env)
		if err != nil {
			return err
		}
	}
	return validateProducerOp(env, raw)
}

// validateProducerOp validates an envelope whose payload bytes are already
// encoded, so the append path does not canonicalize the same envelope twice.
func validateProducerOp(env Envelope, raw []byte) error {
	sch, ok := schemasOnce().vocab[env.ObjectType]
	if !ok {
		return nil
	}
	if err := validateOpTypeAndVersion(env); err != nil {
		return err
	}
	return validateAgainst(sch, raw)
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
