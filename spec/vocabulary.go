package spec

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// vocabularyObjectTypes maps a field-rules.json directory
// (spec.FieldRule.Vocabulary, the testdata/ basename) to the object type
// whose ops it declares rules for. Three of the eleven directories differ
// from the object type name they cover — "review-ops" declares "review",
// "issue-ops" declares "issue", "comments" declares "comment" — so this map,
// not string equality, is the one place that association is recorded.
// spec.FieldRules derives each rule's ObjectType from it, and
// engine/codec/schema.go's fieldRuleVocabularies (object type -> directory,
// the inverse direction, keyed the way a producer looks things up) derives
// from it too, via the VocabularyObjectTypes accessor below, rather than
// keeping a second, independently hand-maintained copy that could drift
// from this one. Unexported so nothing outside this package can mutate the
// table a process-wide fold matching layer depends on: a package-level
// exported map is shared, mutable state with every importer, and there is
// no reason another package needs a live handle on it rather than a
// snapshot.
var vocabularyObjectTypes = map[string]string{
	"review-ops":     "review",
	"comments":       "comment",
	"issue-ops":      "issue",
	"project":        "project",
	"cycle":          "cycle",
	"workflow-state": "workflow-state",
	"label":          "label",
	"document":       "document",
	"section":        "section",
	"settings":       "settings",
	"schema-ops":     "schema",
}

// VocabularyObjectTypes returns a copy of the field-rules.json directory ->
// object type association (see vocabularyObjectTypes): safe for a caller to
// range over or index without holding a handle on writ's own process-wide
// table, and without any caller being able to mutate it out from under the
// fold matching layer.
func VocabularyObjectTypes() map[string]string {
	out := make(map[string]string, len(vocabularyObjectTypes))
	for dir, objectType := range vocabularyObjectTypes {
		out[dir] = objectType
	}
	return out
}

// fieldRulesOnce caches spec.FieldRules() for the vocabulary accessors below,
// which are called repeatedly (cmd/writ flag help, shell completion,
// validation) and must not re-walk and re-parse the embedded field-rules.json
// corpus on every call.
var fieldRulesOnce = sync.OnceValue(func() []FieldRule {
	rules, err := FieldRules()
	if err != nil {
		panic(fmt.Errorf("spec: loading field rules for vocabulary: %w", err))
	}
	return rules
})

// EnumValues returns the declared enum member list for the rule matching
// (vocabulary, opType, field) — the directory under spec/testdata/, the
// op_type, and the field name a field-rules.json entry declares. It panics if
// no such rule exists or the rule is not value_type "enum": both are
// programming errors, the same contract parseSchemaEnum held before this
// scraped schemas directly (WRIT-185 moved the single source of truth for
// these vocabularies from the per-type JSON schemas to the rule tables).
func EnumValues(vocabulary, opType, field string) []string {
	for _, r := range fieldRulesOnce() {
		if r.Vocabulary == vocabulary && r.OpType == opType && r.Field == field {
			if r.ValueType != "enum" {
				panic(fmt.Errorf("spec: rule (%s, %s, %s) is value_type %q, not enum", vocabulary, opType, field, r.ValueType))
			}
			return slices.Clone(r.Enum)
		}
	}
	panic(fmt.Errorf("spec: no field rule (%s, %s, %s)", vocabulary, opType, field))
}

// ReviewStatuses returns the accepted review status enum values, declared on
// review-ops's set-status.status rule.
func ReviewStatuses() []string {
	return EnumValues("review-ops", "set-status", "status")
}

// ApprovalVerdicts returns the accepted approval verdict enum values,
// declared on review-ops's approval.verdict rule.
func ApprovalVerdicts() []string {
	return EnumValues("review-ops", "approval", "verdict")
}

// CIStatusStates returns the accepted CI status state enum values, declared
// on review-ops's ci-status.state rule.
func CIStatusStates() []string {
	return EnumValues("review-ops", "ci-status", "state")
}

// LinkRelations returns the accepted link relation enum values, declared on
// issue-ops's link.relation rule (review-ops's agrees; document's link.relation
// is an unconstrained string, not this enum — see spec/value-types.md).
func LinkRelations() []string {
	return EnumValues("issue-ops", "link", "relation")
}

// ProjectStatuses returns the accepted project status enum values, declared
// on project's set-status.status rule.
func ProjectStatuses() []string {
	return EnumValues("project", "set-status", "status")
}

// WorkflowStateTypes returns the accepted workflow state type enum values,
// declared on workflow-state's create.type rule.
func WorkflowStateTypes() []string {
	return EnumValues("workflow-state", "create", "type")
}

// EstimateScales returns the accepted estimate scale enum values, declared on
// settings's set.estimate_scale rule.
func EstimateScales() []string {
	return EnumValues("settings", "set", "estimate_scale")
}

// FormatOptions formats a slice of enum options into a human-readable list,
// e.g. "open or closed", "approve, request-changes, or none",
// "draft, open, closed, or merged".
func FormatOptions(options []string) string {
	switch len(options) {
	case 0:
		return ""
	case 1:
		return options[0]
	case 2:
		return options[0] + " or " + options[1]
	default:
		return strings.Join(options[:len(options)-1], ", ") + ", or " + options[len(options)-1]
	}
}

// IssuePriorityNames returns the closed priority vocabulary names in index order:
// 0: "none", 1: "urgent", 2: "high", 3: "medium", 4: "low".
func IssuePriorityNames() []string {
	return []string{"none", "urgent", "high", "medium", "low"}
}

// ParseIssuePriority parses a priority string, accepting symbolic names
// ("none", "urgent", "high", "medium", "low") or numeric digits ("0"-"4").
func ParseIssuePriority(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "0":
		return 0, nil
	case "urgent", "1":
		return 1, nil
	case "high", "2":
		return 2, nil
	case "medium", "3":
		return 3, nil
	case "low", "4":
		return 4, nil
	default:
		return 0, fmt.Errorf("spec: invalid issue priority %q (expected none, urgent, high, medium, low, or 0-4)", s)
	}
}

// FormatIssuePriority returns the canonical lowercase symbolic name for a priority level.
func FormatIssuePriority(p int) string {
	switch p {
	case 1:
		return "urgent"
	case 2:
		return "high"
	case 3:
		return "medium"
	case 4:
		return "low"
	default:
		return "none"
	}
}
