package fold

import "github.com/writtendev/writ/engine/codec"

// Uninterpretable reports whether an operation is uninterpretable because a
// field carrying a declared merge rule holds a JSON value that is not the
// shape the field's strategy consumes, per spec/fold.md §7.1.
//
// The unit is the operation, not the field (WRIT-124, WRIT-126). An op is the
// unit of signature and of intent, so half-applying one asserts something
// nobody signed; and op-level is a single rule every implementation can apply
// identically, where a per-field fallback would need specifying once per field
// per strategy — the surface that produced the divergences these rules were
// written to close.
//
// What this does NOT decide, deliberately:
//
//   - Unknown fields and unknown op types. Those keep preserve-and-ignore
//     (spec/forward-compatibility.md) untouched; forward compatibility is a
//     separate rule and this one must not erode it. Only a field with a
//     declared rule is inspected.
//   - The contents of a value the strategy stores verbatim. Fold treats
//     anchors and other structured payloads as opaque data (spec/fold.md §6),
//     so the check reads the value at the declared field and, where the
//     strategy consumes a collection, its immediate elements. It never
//     recurses: an anchor whose context collar is null is well formed.
//   - Whether a value the strategy stores verbatim matches the type its
//     vocabulary schema declares. The fold catalogue knows strategies, not
//     schemas, and teaching it types would make the rules a second source of
//     truth for something the schemas already state. A `lww` field carrying a
//     number folds to that number: deterministic, canonically encodable, and
//     reproducible in any language, which is the property at stake.
func Uninterpretable(op codec.Op, body map[string]any, rules []Rule) bool {
	for _, r := range rules {
		if !opMatchesRule(op, r) {
			continue
		}
		if !ruleAccepts(r, body) {
			return true
		}
	}
	return false
}

// ruleAccepts reports whether body carries a value that rule r's strategy can
// consume. A field the body does not carry is not a write and is accepted.
func ruleAccepts(r Rule, body map[string]any) bool {
	// A keyed-lww key component is consumed as a string whether or not the
	// declared field itself is present in this body: it is what decides which
	// register the write addresses.
	if r.Strategy == "keyed-lww" {
		for _, kf := range r.Key {
			if v, present := body[kf]; present && !isString(v) {
				return false
			}
		}
	}

	if r.Strategy == "set-observed-remove" {
		if r.Field == "add" || r.Field == "remove" {
			sibling := "remove"
			if r.Field == "remove" {
				sibling = "add"
			}
			_, presentField := body[r.Field]
			_, presentSibling := body[sibling]
			if !presentField && !presentSibling {
				return true
			}
			return orSetAccepts(r.Field, body[r.Field], body)
		}
	}

	v, present := body[r.Field]
	if !present {
		return true
	}

	switch r.Strategy {
	case "lww", "create-once", "keyed-lww":
		// The value is stored verbatim, so any JSON value round-trips. null is
		// not a value: it is the absence of one written where a write was
		// claimed, and reducers are known to disagree on it.
		return v != nil
	case "append":
		return appendAccepts(v)
	case "set-union":
		return isStringOrStringSlice(v)
	case "set-observed-remove":
		return orSetAccepts(r.Field, v, body)
	case "tombstone":
		_, ok := v.(bool)
		return ok
	case "lattice":
		return isString(v)
	case "multi-value":
		return isString(v)
	}

	// An undeclared strategy is not this rule's business: NewAccumulator
	// refuses it, which is a structural error rather than a bad op body.
	return true
}

// appendAccepts reports whether v can be appended. Entries are stored
// verbatim, so any JSON value is an entry — but null is not a value, at the
// field or in an array the strategy flattens into the list.
func appendAccepts(v any) bool {
	if v == nil {
		return false
	}
	if slice, ok := v.([]any); ok {
		for _, item := range slice {
			if item == nil {
				return false
			}
		}
	}
	return true
}

// orSetAccepts reports whether v carries OR-set items. Three body shapes reach
// this, and a conforming reader MUST accept all three (spec/fold.md §5.4's
// set-observed-remove body-shapes bullet):
//
//   - nested, where the declared field holds an object with `add` and `remove`
//     members;
//   - flat, where `add` and `remove` are themselves the declared fields;
//   - scalar, where the declared field holds one item and the op type carries
//     which side it lands on.
//
// A side holds a string or an array of strings, exactly as a set-union field
// does. An absent side is not a write and is accepted; a side that is present
// and holds null is not — it is a write claimed with no value in it, which
// §7.1 rejects wherever a strategy consumes a value. Reading it as an absent
// side instead would make `{"add": null}` and `{}` byte-identical in folded
// state, which is the objection §7.1 raises against skipping.
//
// In the flat shape the reducer for one declared field also reads the sibling
// side out of the same body, so this inspects it too. A vocabulary normally
// declares a rule for both `add` and `remove`, which makes the sibling check
// redundant — but only normally, and a predicate that skipped a side the
// reducer goes on to consume is the disagreement this file exists to prevent.
func orSetAccepts(field string, v any, body map[string]any) bool {
	sideOK := func(member any, present bool) bool {
		if !present {
			return true
		}
		if obj, ok := member.(map[string]any); ok {
			for _, side := range []string{"add", "remove"} {
				m, p := obj[side]
				if !p || isStringOrStringSlice(m) {
					continue
				}
				return false
			}
			return true
		}
		return isStringOrStringSlice(member)
	}

	if field == "add" || field == "remove" {
		sibling := "remove"
		if field == "remove" {
			sibling = "add"
		}
		member, present := body[sibling]
		if !sideOK(member, present) {
			return false
		}
		vMember, vPresent := body[field]
		return sideOK(vMember, vPresent)
	}
	return sideOK(v, true)
}

// orSetItems returns the items one side of an OR-set body carries. The side
// holds a string or an array of strings; anything else made the op
// uninterpretable (§7.1) before it reached a reducer, so a non-string here is
// unreachable and skipped rather than rendered.
//
// This is the reducer half of orSetAccepts and MUST consume exactly what that
// accepts. They disagreed once: the predicate took a bare string on a side and
// the reducer read only arrays, so `{"add": "solo"}` folded to the empty set —
// the silent drop that "skip invents an absence" names.
func orSetItems(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		items := make([]string, 0, len(v))
		for _, it := range v {
			if s, ok := it.(string); ok {
				items = append(items, s)
			}
		}
		return items
	case []string:
		return v
	}
	return nil
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

// isStringSlice reports whether v is an array whose every element is a string.
// The []string arm exists for parity with the reducers, which carry one of
// their own: a body decoded from JSON never holds a []string, but a body
// assembled in Go can, and a predicate that rejected what a reducer would have
// consumed is the disagreement this file exists to prevent.
func isStringSlice(v any) bool {
	switch slice := v.(type) {
	case []any:
		for _, item := range slice {
			if !isString(item) {
				return false
			}
		}
		return true
	case []string:
		return true
	}
	return false
}

func isStringOrStringSlice(v any) bool {
	if isString(v) {
		return true
	}
	return isStringSlice(v)
}
