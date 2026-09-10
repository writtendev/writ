// Package value implements the closed value-type catalogue (spec/value-types.md):
// producer-side validation, and the one normalization behaviour the catalogue
// defines. It is modelled on engine/internal/person and, like it, is a pure
// leaf package — person and the standard library only, no I/O — so the fold,
// which must stay free of I/O, can call it without pulling anything else in.
package value

import (
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/writtendev/writ/engine/internal/person"
)

// Known is the closed set of value-type names from spec/value-types.md.
var Known = map[string]bool{
	"string":     true,
	"text":       true,
	"int":        true,
	"number":     true,
	"bool":       true,
	"timestamp":  true,
	"enum":       true,
	"person-ref": true,
	"object-ref": true,
	"git-oid":    true,
	"position":   true,
	"anchor":     true,
}

// maxSafeInt is the canonical-encoding bound of spec/canonicalization.md:
// integers beyond ±2^53-1 silently lose precision once round-tripped through
// an IEEE-754 double, so int and number are both bounded here.
const maxSafeInt = 1<<53 - 1

// Params carries the parameterisation a value type MAY declare per
// spec/value-types.md: enum's member list, and string/text's max_length
// (counted in Unicode code points, per spec/value-types.md §Length units).
type Params struct {
	Enum      []string
	MaxLength int64
}

var (
	referencePattern = regexp.MustCompile(`^([0-9a-f]{32}#)?[^#\s]+$`)
	gitOIDPattern    = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})$`)
	positionPattern  = regexp.MustCompile(`^[0-9A-Za-z]*[1-9A-Za-z]$`)
)

// referenceMaxLen is spec/identifiers.md §Length bound's reference bound: 32
// (repo-id) + 1 (#) + 256, counted in Unicode code points.
const referenceMaxLen = 289

// Validate reports whether v conforms to valueType, parameterised by params.
// Validation is producer-side and reader-tolerant (spec/op-envelope.md
// §Producer validation): callers on the read path MUST NOT call this to
// decide whether to keep a value it cannot interpret
// (spec/forward-compatibility.md) — surface it through the existing
// UnknownOp channel instead.
func Validate(valueType string, params Params, v any) error {
	switch valueType {
	case "string", "text":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("value: %s value must be a JSON string", valueType)
		}
		if params.MaxLength > 0 && int64(utf8.RuneCountInString(s)) > params.MaxLength {
			return fmt.Errorf("value: %s value exceeds max_length %d code points", valueType, params.MaxLength)
		}
		return nil
	case "int":
		n, ok := jsonNumber(v)
		if !ok {
			return fmt.Errorf("value: int value must be a JSON number")
		}
		if n != float64(int64(n)) {
			return fmt.Errorf("value: int value %v is not an integer", v)
		}
		if n < -maxSafeInt || n > maxSafeInt {
			return fmt.Errorf("value: int value %v exceeds spec/canonicalization.md's ±2^53-1 bound", v)
		}
		return nil
	case "number":
		n, ok := jsonNumber(v)
		if !ok {
			return fmt.Errorf("value: number value must be a JSON number")
		}
		if n < -maxSafeInt || n > maxSafeInt {
			return fmt.Errorf("value: number value %v exceeds spec/canonicalization.md's ±2^53-1 bound", v)
		}
		return nil
	case "bool":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("value: bool value must be a JSON boolean")
		}
		return nil
	case "timestamp":
		s, ok := v.(string)
		if !ok || !timestampPattern.MatchString(s) {
			return fmt.Errorf("value: timestamp value must be an RFC 3339 date-time string")
		}
		return nil
	case "enum":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("value: enum value must be a JSON string")
		}
		for _, e := range params.Enum {
			if e == s {
				return nil
			}
		}
		return fmt.Errorf("value: %q is not a member of the declared enum %v", s, params.Enum)
	case "person-ref":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("value: person-ref value must be a JSON string")
		}
		if p := person.Check(s); p != person.Valid {
			if r, ok := person.FirstForbidden(s); ok {
				return fmt.Errorf("value: person-ref value %q: %s (U+%04X)", s, p, r)
			}
			return fmt.Errorf("value: person-ref value %q: %s", s, p)
		}
		return nil
	case "object-ref":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("value: object-ref value must be a JSON string")
		}
		if s == "" || int64(utf8.RuneCountInString(s)) > referenceMaxLen || !referencePattern.MatchString(s) {
			return fmt.Errorf("value: object-ref value %q is not a conforming reference (spec/identifiers.md)", s)
		}
		return nil
	case "git-oid":
		s, ok := v.(string)
		if !ok || !gitOIDPattern.MatchString(s) {
			return fmt.Errorf("value: git-oid value must be 40 or 64 lowercase hexadecimal characters")
		}
		return nil
	case "position":
		s, ok := v.(string)
		if !ok || !positionPattern.MatchString(s) {
			return fmt.Errorf("value: position value must be a canonical base-62 fractional index (spec/ordering.md)")
		}
		return nil
	case "anchor":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("value: anchor value must be a JSON object")
		}
		if err := validateAnchor(m); err != nil {
			return fmt.Errorf("value: anchor value: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("value: unknown value type %q", valueType)
	}
}

func jsonNumber(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok
}

// validateAnchor checks the cross-field shape spec/anchors.md and
// anchor.schema.json require beyond "is a JSON object": version 1, at least
// one of old/new, and each present side's required fields and its
// range/context pairing. It is not a JSON Schema validator — value stays a
// pure leaf package, person and the standard library only — only the finite,
// known shape of this one catalogue type.
func validateAnchor(m map[string]any) error {
	if v, ok := m["version"]; !ok || v != float64(1) {
		return fmt.Errorf("version must be 1")
	}
	old, hasOld := m["old"]
	newSide, hasNew := m["new"]
	if !hasOld && !hasNew {
		return fmt.Errorf("at least one of old or new is required")
	}
	if hasOld {
		if err := validateAnchorSide(old); err != nil {
			return fmt.Errorf("old: %w", err)
		}
	}
	if hasNew {
		if err := validateAnchorSide(newSide); err != nil {
			return fmt.Errorf("new: %w", err)
		}
	}
	return nil
}

// validateAnchorSide checks one side's required fields (commit, path, blob)
// and the dependentRequired pairing between range and context that
// anchor.schema.json declares.
func validateAnchorSide(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("must be a JSON object")
	}
	for _, field := range []string{"commit", "path", "blob"} {
		s, ok := m[field].(string)
		if !ok || s == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	_, hasRange := m["range"]
	_, hasContext := m["context"]
	if hasRange != hasContext {
		return fmt.Errorf("range and context must be present together")
	}
	return nil
}

// Normalize applies the one normalization behaviour the catalogue defines:
// person-ref values fold through person.NormalizePerson (spec/identifiers.md).
// Every other value type is returned unchanged. Normalization is intrinsic to
// the value type rather than a separate rule attribute (spec/value-types.md).
func Normalize(valueType, s string) string {
	if valueType == "person-ref" {
		return person.NormalizePerson(s)
	}
	return s
}
