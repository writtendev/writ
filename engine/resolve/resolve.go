package resolve

import (
	"encoding/json"
	"errors"
	"fmt"
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

// parseSideAnchor decodes one side of an anchor per spec/resolution.md
// §Structural Pre-Check step 2: commit/path/blob must each be a JSON string
// (not null), range must be exactly {"start": int, "end": int}, and context
// must be exactly {"before": [string], "lines": [string], "after": [string],
// "omitted"?: int} — every key matched case-sensitively and every scalar
// checked for null explicitly, because Go's struct-based json.Unmarshal does
// neither (case-insensitive field fallback; a null silently no-ops into a
// non-pointer's zero value) and round-1 review found both holes. Decoding
// through map[string]json.RawMessage side-steps struct field matching
// entirely, so a wrongly-cased key is simply absent, not a synonym.
func parseSideAnchor(raw json.RawMessage) (*SideAnchor, error) {
	var sideMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sideMap); err != nil {
		return nil, err
	}

	var s SideAnchor
	if v, ok := sideMap["commit"]; ok {
		str, err := decodeNonNullString(v)
		if err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
		s.Commit = str
		delete(sideMap, "commit")
	}
	if v, ok := sideMap["path"]; ok {
		str, err := decodeNonNullString(v)
		if err != nil {
			return nil, fmt.Errorf("path: %w", err)
		}
		s.Path = str
		delete(sideMap, "path")
	}
	if v, ok := sideMap["blob"]; ok {
		str, err := decodeNonNullString(v)
		if err != nil {
			return nil, fmt.Errorf("blob: %w", err)
		}
		s.Blob = str
		delete(sideMap, "blob")
	}
	if v, ok := sideMap["range"]; ok {
		r, err := decodeRange(v)
		if err != nil {
			return nil, fmt.Errorf("range: %w", err)
		}
		s.Range = r
		delete(sideMap, "range")
	}
	if v, ok := sideMap["context"]; ok {
		ctx, err := decodeContext(v)
		if err != nil {
			return nil, fmt.Errorf("context: %w", err)
		}
		s.Context = ctx
		delete(sideMap, "context")
	}

	if len(sideMap) > 0 {
		s.Unknown = sideMap
	}

	return &s, nil
}

// decodeRange decodes {"start": int, "end": int}, exact-case, rejecting a
// missing key or a null/non-integer value rather than defaulting to 0 the
// way json.Unmarshal into a Range struct would (Go's case-insensitive
// struct-field fallback also let "START"/"End" through as synonyms — a map
// lookup by exact key has no such fallback).
func decodeRange(raw json.RawMessage) (*Range, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	startRaw, ok := m["start"]
	if !ok {
		return nil, errors.New("start is required")
	}
	start, err := decodeNonNullInt(startRaw)
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	endRaw, ok := m["end"]
	if !ok {
		return nil, errors.New("end is required")
	}
	end, err := decodeNonNullInt(endRaw)
	if err != nil {
		return nil, fmt.Errorf("end: %w", err)
	}
	return &Range{Start: start, End: end}, nil
}

// decodeContext decodes {"before": [string], "lines": [string], "after":
// [string], "omitted"?: int}, exact-case, with the same null- and
// case-intolerance as decodeRange. "omitted", when present, must decode as
// a non-null integer >= 1: spec/resolution.md's arithmetic step requires
// that regardless of what the range/lines arithmetic below it says, so
// rejecting it here (rather than only in sideWellFormed's numeric check)
// means an omitted value of 0 or null can never be confused with omitted
// being absent — the two are indistinguishable once collapsed to the int 0
// Context.Omitted uses everywhere else.
func decodeContext(raw json.RawMessage) (*Context, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	before, err := decodeNonNullStringSlice(m, "before")
	if err != nil {
		return nil, err
	}
	lines, err := decodeNonNullStringSlice(m, "lines")
	if err != nil {
		return nil, err
	}
	after, err := decodeNonNullStringSlice(m, "after")
	if err != nil {
		return nil, err
	}
	ctx := &Context{Before: before, Lines: lines, After: after}
	if omittedRaw, ok := m["omitted"]; ok {
		omitted, err := decodeNonNullInt(omittedRaw)
		if err != nil {
			return nil, fmt.Errorf("omitted: %w", err)
		}
		if omitted < 1 {
			return nil, errors.New("omitted: must be >= 1 when present")
		}
		ctx.Omitted = omitted
	}
	return ctx, nil
}

// decodeNonNullString decodes raw as a JSON string, refusing null. Unmarshal
// into a plain string silently no-ops on null, leaving the caller unable to
// tell "absent" from "present and null" — going through *string first makes
// that distinction visible.
func decodeNonNullString(raw json.RawMessage) (string, error) {
	var v *string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	if v == nil {
		return "", errNullValue
	}
	return *v, nil
}

// decodeNonNullInt decodes raw as a JSON integer, refusing null and refusing
// a non-integer number (json.Unmarshal into *int already errors on "1.5" or
// `"1"`; only null needed the pointer indirection to catch).
func decodeNonNullInt(raw json.RawMessage) (int, error) {
	var v *int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, err
	}
	if v == nil {
		return 0, errNullValue
	}
	return *v, nil
}

// decodeNonNullStringSlice requires key to be present in m as a JSON array
// of non-null strings. Decoding through []*string catches a null array
// element the same way decodeNonNullString catches a null scalar.
func decodeNonNullStringSlice(m map[string]json.RawMessage, key string) ([]string, error) {
	raw, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("%s is required", key)
	}
	var ptrs []*string
	if err := json.Unmarshal(raw, &ptrs); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	out := make([]string, len(ptrs))
	for i, p := range ptrs {
		if p == nil {
			return nil, fmt.Errorf("%s[%d]: %w", key, i, errNullValue)
		}
		out[i] = *p
	}
	return out, nil
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
