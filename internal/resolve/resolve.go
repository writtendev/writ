package resolve

import (
	"encoding/json"
	"errors"

	"github.com/writtendev/writ/internal/anchorshape"
)

// errNullValue signals that a JSON `null` occupied a slot the Structural
// Pre-Check (spec/resolution.md) requires to be a concrete value: a string,
// an integer, or an array of strings. Go's encoding/json silently no-ops a
// `null` unmarshaled into a non-pointer string/int/slice-element, which is
// exactly the looseness a hostile anchor exploited (round-1 review of this
// PR): every decode helper below unmarshals through a pointer first so a
// `null` is caught here rather than swallowed as a zero value.
var errNullValue = errors.New("resolve: null where a value was required")

// Range is a 1-based inclusive line range [Start, End].
type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Context holds the captured line content and surrounding collar.
type Context struct {
	Before  []string `json:"before"`
	Lines   []string `json:"lines"`
	Omitted int      `json:"omitted,omitempty"`
	After   []string `json:"after"`
}

// MarshalJSON serializes Context with before, lines and after always present as
// JSON arrays.
//
// spec/schemas/anchor.schema.json requires all three as arrays, and a nil Go
// slice marshals to null, which is not one. The absent collar is ordinary, not
// exceptional: a comment on the first line of a file has nothing before it and
// a comment on the last line has nothing after it, and both are built with the
// zero value for those fields. Normalizing here rather than at each of the
// callers is the domain-layer transform the producer check depends on — the
// codec judges the bytes, it cannot fix them.
func (c Context) MarshalJSON() ([]byte, error) {
	type alias Context
	a := alias(c)
	if a.Before == nil {
		a.Before = []string{}
	}
	if a.Lines == nil {
		a.Lines = []string{}
	}
	if a.After == nil {
		a.After = []string{}
	}
	return json.Marshal(a)
}

// SideAnchor describes the position in a specific commit's tree (old or new side).
type SideAnchor struct {
	Commit  string                     `json:"commit"`
	Path    string                     `json:"path"`
	Blob    string                     `json:"blob"`
	Range   *Range                     `json:"range,omitempty"`
	Context *Context                   `json:"context,omitempty"`
	Unknown map[string]json.RawMessage `json:"-"`
}

// Anchor is a content-based position in code (v1), a value type any
// schema-declared object can carry (spec/anchors.md).
type Anchor struct {
	Version int                        `json:"version"`
	Old     *SideAnchor                `json:"old,omitempty"`
	New     *SideAnchor                `json:"new,omitempty"`
	Unknown map[string]json.RawMessage `json:"-"`
	Raw     []byte                     `json:"-"`
}

// MarshalJSON serializes Anchor. If Raw is populated, Raw is returned directly
// to preserve unknown fields and exact bytes.
func (a Anchor) MarshalJSON() ([]byte, error) {
	if len(a.Raw) > 0 {
		return a.Raw, nil
	}
	type Alias Anchor
	return json.Marshal((*Alias)(&a))
}

// ParseAnchor parses raw JSON bytes into an Anchor, retaining the original bytes
// in Raw and preserving unknown fields.
func ParseAnchor(raw []byte) (Anchor, error) {
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(raw, &topLevel); err != nil {
		return Anchor{}, err
	}

	var a Anchor
	a.Raw = raw

	if v, ok := topLevel["version"]; ok {
		version, err := decodeNonNullInt(v)
		if err != nil {
			return Anchor{}, err
		}
		a.Version = version
		delete(topLevel, "version")
	}
	if v, ok := topLevel["old"]; ok {
		side, err := parseSideAnchor(v)
		if err != nil {
			return Anchor{}, err
		}
		a.Old = side
		delete(topLevel, "old")
	}
	if v, ok := topLevel["new"]; ok {
		side, err := parseSideAnchor(v)
		if err != nil {
			return Anchor{}, err
		}
		a.New = side
		delete(topLevel, "new")
	}

	if len(topLevel) > 0 {
		a.Unknown = topLevel
	}

	return a, nil
}

// ResolveRaw is the total read-side entry point (spec/resolution.md
// §Structural Pre-Check): given raw anchor bytes and a target tree, it never
// returns an error and never panics, unlike ParseAnchor followed by Resolve.
//
// Order of checks, matching the spec's structural pre-check before the
// version pre-check before the ladder:
//   - If the bytes don't decode as a JSON object, or neither "old" nor "new"
//     is present, there is no side to orphan: the result carries no Old or
//     New (spec is deliberately silent on this shape; see the ticket's open
//     question).
//   - A missing or non-integer "version" orphans every present side
//     "malformed".
//   - An integer "version" != 1 orphans every present side
//     "unsupported-version", even if a side itself fails to decode: the
//     version pre-check wins.
//   - For version 1, each present side that fails to decode as a SideAnchor
//     orphans "malformed" individually; a side that decodes runs the normal
//     ladder via resolveSide, which applies its own arithmetic pre-check.
//
// The returned Resolution's Anchor always carries Raw, so the orphan is
// byte-preserved (Anchor.MarshalJSON returns Raw directly).
func ResolveRaw(raw []byte, t *Tree) Resolution {
	res := Resolution{Anchor: Anchor{Raw: raw}}

	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(raw, &topLevel); err != nil {
		return res
	}

	oldRaw, hasOld := topLevel["old"]
	newRaw, hasNew := topLevel["new"]
	if !hasOld && !hasNew {
		return res
	}

	vRaw, hasVersion := topLevel["version"]
	version := 0
	versionIsInt := false
	if hasVersion {
		if n, err := decodeNonNullInt(vRaw); err == nil {
			version, versionIsInt = n, true
		}
	}

	if !versionIsInt {
		if hasOld {
			res.Old = &SideResult{Outcome: OutcomeOrphaned, Reason: ReasonMalformed}
		}
		if hasNew {
			res.New = &SideResult{Outcome: OutcomeOrphaned, Reason: ReasonMalformed}
		}
		return res
	}

	if version != 1 {
		if hasOld {
			res.Old = &SideResult{Outcome: OutcomeOrphaned, Reason: ReasonUnsupportedVersion}
		}
		if hasNew {
			res.New = &SideResult{Outcome: OutcomeOrphaned, Reason: ReasonUnsupportedVersion}
		}
		return res
	}

	if hasOld {
		if side, err := parseSideAnchor(oldRaw); err != nil {
			res.Old = &SideResult{Outcome: OutcomeOrphaned, Reason: ReasonMalformed}
		} else {
			res.Old = resolveSide(version, side, t)
		}
	}
	if hasNew {
		if side, err := parseSideAnchor(newRaw); err != nil {
			res.New = &SideResult{Outcome: OutcomeOrphaned, Reason: ReasonMalformed}
		} else {
			res.New = resolveSide(version, side, t)
		}
	}

	return res
}

// errMalformedSide reports that a side failed anchorshape.SideWellFormed
// (spec/resolution.md §Structural Pre-Check steps 2-3): it does not decode
// as a v1 side anchor, or its range/context/omitted arithmetic is
// inconsistent. parseSideAnchor's caller only needs to know that a side
// must orphan "malformed", never why, so one sentinel covers every way
// SideWellFormed can say no.
var errMalformedSide = errors.New("resolve: side does not decode as a well-formed v1 side anchor")

// parseSideAnchor decodes one side of an anchor. It first decodes raw into
// Go's generic JSON representation (map[string]any, []any, string, float64,
// nil) and asks anchorshape.SideWellFormed the exact structural-decode-plus-
// arithmetic question spec/resolution.md's Structural Pre-Check defines —
// the one predicate engine/internal/value's producer check also calls, in
// place of this function's own former hand-written decode. That former
// decode (case-sensitive but not null-aware in every position) is what let
// a null side, a null collar array, and an absent commit/path/blob through
// (round-2 review of this PR). A side that fails is reported as a decode
// error; a side that passes is then extracted by straightforward type
// assertion, which SideWellFormed has already made safe.
func parseSideAnchor(raw json.RawMessage) (*SideAnchor, error) {
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	if !anchorshape.SideWellFormed(generic) {
		return nil, errMalformedSide
	}
	m := generic.(map[string]any)

	s := &SideAnchor{
		Commit: m["commit"].(string),
		Path:   m["path"].(string),
		Blob:   m["blob"].(string),
	}

	if rangeVal, hasRange := m["range"]; hasRange {
		rangeMap := rangeVal.(map[string]any)
		contextMap := m["context"].(map[string]any)
		ctx := &Context{
			Before: toNonNullStrings(contextMap["before"]),
			Lines:  toNonNullStrings(contextMap["lines"]),
			After:  toNonNullStrings(contextMap["after"]),
		}
		if omittedVal, hasOmitted := contextMap["omitted"]; hasOmitted {
			ctx.Omitted = int(omittedVal.(float64))
		}
		s.Range = &Range{
			Start: int(rangeMap["start"].(float64)),
			End:   int(rangeMap["end"].(float64)),
		}
		s.Context = ctx
	}

	// Unknown side-level fields, preserved for forward compatibility (never
	// dropped, per house rules): a second decode into per-key raw bytes,
	// discarding the five keys already extracted above.
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawFields); err == nil {
		for _, known := range [...]string{"commit", "path", "blob", "range", "context"} {
			delete(rawFields, known)
		}
		if len(rawFields) > 0 {
			s.Unknown = rawFields
		}
	}

	return s, nil
}

// toNonNullStrings converts a JSON array already confirmed by
// anchorshape.SideWellFormed to hold only non-null strings into a []string.
func toNonNullStrings(v any) []string {
	arr := v.([]any)
	out := make([]string, len(arr))
	for i, e := range arr {
		out[i] = e.(string)
	}
	return out
}

// errNotJSONInt reports that decodeNonNullInt's value decoded as JSON but
// failed anchorshape.JSONInt's integrality/bound check: it is not a JSON
// number, has a fractional part, or falls outside the ±(2^53-1)
// exact-integer bound spec/value-types.md gives every catalogue int/number.
var errNotJSONInt = errors.New("resolve: value is not a JSON integer within the ±(2^53-1) exact-integer bound")

// decodeNonNullInt decodes raw as a JSON integer under the exact same rule
// anchorshape.JSONInt applies to a side's range.start, range.end, and
// context.omitted, refusing null and refusing anything that isn't an
// integer within ±(2^53-1). Used for the anchor-level "version" field,
// which parseSideAnchor/anchorshape do not otherwise touch — version sits
// outside any side.
//
// Round 4 review of this PR found the previous implementation decoded
// version through Go's own int-literal parsing (json.Unmarshal into *int)
// instead of this shared rule: that accepted an out-of-bound literal like
// 9007199254740992 verbatim as the exact int64 9007199254740992, comparing
// it to 1 and giving "unsupported-version" where the shared rule says the
// number itself is out of bounds and the result must be "malformed" — and
// it rejected 1.0 outright as "malformed", where the same literal in
// range.start decodes to the integer 1 under the shared rule. Version
// disagreed with every side field on the identical numbers for no
// principled reason.
func decodeNonNullInt(raw json.RawMessage) (int, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, err
	}
	if v == nil {
		return 0, errNullValue
	}
	n, ok := anchorshape.JSONInt(v)
	if !ok {
		return 0, errNotJSONInt
	}
	return n, nil
}

// SideResult represents the resolution outcome for one side of an anchor.
type SideResult struct {
	Outcome string `json:"outcome"`
	Match   string `json:"match,omitempty"`
	Path    string `json:"path,omitempty"`
	Range   *Range `json:"range,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Resolution represents the deterministic outcome of resolving an Anchor against a Tree.
type Resolution struct {
	Anchor Anchor      `json:"anchor"`
	Old    *SideResult `json:"old,omitempty"`
	New    *SideResult `json:"new,omitempty"`
}
