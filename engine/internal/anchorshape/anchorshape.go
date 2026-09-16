// Package anchorshape implements the one structural well-formedness
// question a v1 anchor side must answer, so that writ's own producer
// (engine/internal/value) and the read-side resolver (engine/resolve) ask
// it the same way instead of each hand-writing a copy that can drift.
//
// WRIT-252 round 2 review found exactly that drift: the producer accepted
// side shapes (non-string context.lines entries, a context missing
// before/after, a null range or context) that the reader orphaned as
// malformed, and the reader let other shapes through (a null side, a null
// collar array, an absent commit/path/blob) that the producer refused.
// Both checks are rewritten here, once, against spec/resolution.md's
// Structural Pre-Check (steps 2-3) and spec/anchors.md's §Context capture
// arithmetic.
//
// SideWellFormed takes a side already decoded through encoding/json into
// Go's generic representation: map[string]any for a JSON object, []any for
// an array, string, float64, bool, or nil. Both callers already have a side
// in this shape when they reach here — the producer validates a
// schema-decoded document tree, and the reader decodes raw anchor bytes
// into it before calling in — and it is exactly the representation the
// Structural Pre-Check needs: map keys are the literal JSON keys (never
// case-folded the way Go's struct-based json.Unmarshal folds field names),
// and a JSON null decodes to a map entry holding a nil interface, distinct
// from the key being absent. Go's struct-based unmarshal gives neither for
// free, which is what let both hand-written copies drift.
//
// This package is stdlib-only: engine/internal/value (a pure leaf: person
// and the standard library only) and engine/resolve (a pure leaf: no I/O)
// both import it, and neither package's own fence allows anything more.
package anchorshape

// SideWellFormed reports whether side decodes as a structurally sound v1
// side anchor per spec/resolution.md's Structural Pre-Check step 2
// (commit, path and blob present as non-null JSON strings; range and
// context present together or absent together; context present as
// exactly before/lines/after string arrays plus an optional integer
// omitted) and step 3 (the range/context/omitted arithmetic
// spec/anchors.md §Context capture defines).
//
// Decoding is exact, not lenient, matching the spec text: an object's
// members are matched by exact case (map keys already are, since nothing
// here does Go's struct-field case folding), and a JSON null satisfies
// none of the required types — a null commit/path/blob is not a string, a
// null range/context is not an object, and a null collar entry is not a
// string, so each fails the corresponding type assertion below rather than
// silently becoming a zero value.
//
// SideWellFormed does not check OID format, path segment rules, or line
// content/length: spec/resolution.md's Structural Pre-Check is
// deliberately decode-and-arithmetic only, leaving format constraints to
// whatever separately validates against anchor.schema.json.
func SideWellFormed(side any) bool {
	m, ok := side.(map[string]any)
	if !ok {
		return false
	}

	for _, field := range [...]string{"commit", "path", "blob"} {
		if _, ok := m[field].(string); !ok {
			return false
		}
	}

	rangeVal, hasRange := m["range"]
	contextVal, hasContext := m["context"]
	if hasRange != hasContext {
		return false
	}
	if !hasRange {
		return true
	}

	rangeMap, ok := rangeVal.(map[string]any)
	if !ok {
		return false
	}
	start, ok := jsonInt(rangeMap["start"])
	if !ok {
		return false
	}
	end, ok := jsonInt(rangeMap["end"])
	if !ok {
		return false
	}

	contextMap, ok := contextVal.(map[string]any)
	if !ok {
		return false
	}
	if _, ok := stringArray(contextMap["before"]); !ok {
		return false
	}
	lines, ok := stringArray(contextMap["lines"])
	if !ok {
		return false
	}
	if _, ok := stringArray(contextMap["after"]); !ok {
		return false
	}

	omitted := 0
	hasOmitted := false
	if omittedVal, present := contextMap["omitted"]; present {
		hasOmitted = true
		omitted, ok = jsonInt(omittedVal)
		if !ok {
			return false
		}
	}

	return RangeContextWellFormed(start, end, len(lines), omitted, hasOmitted)
}

// RangeContextWellFormed reports whether a side's already-decoded range and
// context values satisfy spec/anchors.md §Context capture's cross-field
// arithmetic: start >= 1; end >= start; lines non-empty; and, for the
// elided/non-elided cases, the omitted/lines/range relationship. linesLen is
// len(context.lines); omitted and hasOmitted give context.omitted's value
// and whether the key was present at all — omitted's presence is judged by
// key existence, never by its value, so a present-but-zero omitted must be
// passed as hasOmitted=true, omitted=0 rather than folded into "absent".
//
// This is the one arithmetic implementation both the generic-JSON path
// above (via SideWellFormed) and engine/resolve/ladder.go's typed,
// Go-constructed-anchor path reduce to, so the numbers cannot drift between
// them the way the two hand-written copies this replaces did.
func RangeContextWellFormed(start, end, linesLen, omitted int, hasOmitted bool) bool {
	if start < 1 || end < start {
		return false
	}
	if linesLen == 0 {
		return false
	}

	size := end - start + 1
	if !hasOmitted {
		return size <= 64 && linesLen == size
	}
	if omitted < 1 {
		return false
	}
	return linesLen == 64 && omitted == size-64
}

// jsonInt decodes v (a value from Go's generic JSON representation) as an
// integer, refusing anything that isn't a JSON number with zero fractional
// part — encoding/json always decodes a JSON number into float64 when the
// target is interface{}, so an integer-valued float is the only signal that
// v was a JSON integer rather than "1.5" or a non-numeric type (including
// null, which fails the initial type assertion outright).
func jsonInt(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	i := int(f)
	if float64(i) != f {
		return 0, false
	}
	return i, true
}

// stringArray decodes v as a JSON array of non-null strings, refusing a
// non-array v (including null, an absent key represented as a nil
// interface, and any scalar) and refusing any element that isn't itself a
// JSON string (including a null element).
func stringArray(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(arr))
	for i, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}
