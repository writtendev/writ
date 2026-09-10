package codec

import "fmt"

// Test hooks for the producer registry and the vocabulary schemas.
//
// They exist so the two halves this package deliberately keeps apart can be
// pinned apart: what a vocabulary schema accepts is reader safety, and what
// BuildCommit writes is producer rules 3, 4, 5, and 6 (spec/op-envelope.md
// §Producer validation). Going through ValidateBody could not tell them
// apart, because ValidateBody enforces both.

// VocabularyOpTypes is the producer's op-type registry, keyed by object type.
var VocabularyOpTypes = vocabularyOpTypes

// VocabularyOpVersion is the op version the producer writes.
const VocabularyOpVersion = vocabularyOpVersion

// VocabularySchemaFiles is the producer's vocabulary-schema registry, keyed by
// object type. Exported so TestFieldRuleVocabulariesIsExhaustive can check
// fieldRuleVocabularies against the same key set without a second, drifting
// copy of it.
var VocabularySchemaFiles = vocabularySchemaFiles

// FieldRuleVocabularies is fieldRuleVocabularies, exported for
// TestFieldRuleVocabulariesIsExhaustive.
var FieldRuleVocabularies = fieldRuleVocabularies

// ValidateAgainstVocabularySchema validates payload bytes against the
// vocabulary schema registered for objectType and nothing else — no producer
// rule is applied. It is what a third-party reader validating an op against a
// published schema does.
func ValidateAgainstVocabularySchema(objectType string, raw []byte) error {
	sch, ok := schemasOnce().vocab[objectType]
	if !ok {
		return fmt.Errorf("codec: no vocabulary schema registered for object type %q", objectType)
	}
	return validateAgainst(sch, raw)
}
