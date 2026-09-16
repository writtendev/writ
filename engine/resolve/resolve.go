package resolve

import (
	"encoding/json"
)

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
		if err := json.Unmarshal(v, &a.Version); err != nil {
			return Anchor{}, err
		}
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
	var version int
	versionIsInt := hasVersion
	if hasVersion {
		if err := json.Unmarshal(vRaw, &version); err != nil {
			versionIsInt = false
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

func parseSideAnchor(raw json.RawMessage) (*SideAnchor, error) {
	var sideMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sideMap); err != nil {
		return nil, err
	}

	var s SideAnchor
	if v, ok := sideMap["commit"]; ok {
		if err := json.Unmarshal(v, &s.Commit); err != nil {
			return nil, err
		}
		delete(sideMap, "commit")
	}
	if v, ok := sideMap["path"]; ok {
		if err := json.Unmarshal(v, &s.Path); err != nil {
			return nil, err
		}
		delete(sideMap, "path")
	}
	if v, ok := sideMap["blob"]; ok {
		if err := json.Unmarshal(v, &s.Blob); err != nil {
			return nil, err
		}
		delete(sideMap, "blob")
	}
	if v, ok := sideMap["range"]; ok {
		var r Range
		if err := json.Unmarshal(v, &r); err != nil {
			return nil, err
		}
		s.Range = &r
		delete(sideMap, "range")
	}
	if v, ok := sideMap["context"]; ok {
		var ctx Context
		if err := json.Unmarshal(v, &ctx); err != nil {
			return nil, err
		}
		s.Context = &ctx
		delete(sideMap, "context")
	}

	if len(sideMap) > 0 {
		s.Unknown = sideMap
	}

	return &s, nil
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
