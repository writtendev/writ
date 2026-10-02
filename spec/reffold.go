package spec

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/writtendev/writ/internal/person/ucd"
	"github.com/writtendev/writ/internal/textsafe"
)

// personUnicodeVersion is the Unicode version spec/identifiers.md pins the
// person-identifier normalization algorithm to, permanently for format v1. The
// Unicode data is internal/person/ucd's vendored tables, shared with the
// engine's copy of the rule: data, not algorithm, so a reference fold built on
// it does not depend on the Unicode version of whoever compiled it.
const personUnicodeVersion = ucd.Version

// splitPerson splits a person identifier into scheme and value on the FIRST
// colon, per spec/identifiers.md. The first colon and not "a colon": an email
// address may legally carry a colon inside a quoted local part, so
// `email:"a:b"@example.com` is scheme `email` with value `"a:b"@example.com`.
func splitPerson(s string) (scheme, value string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return "", s, false
	}
	return s[:i], s[i+1:], true
}

// normalizePerson normalizes a person identifier string per
// spec/identifiers.md: the scheme is lowercased, and the value is trimmed of
// leading and trailing whitespace and folded by foldPersonValue.
//
// A string carrying no colon is not a conforming identifier; it is folded as
// a flat string and preserved rather than rejected, because what a reader
// does with a non-conforming identifier is a separate decision
// (WRIT-124/126).
//
// The reference fold is deliberately standalone — it is what independent
// implementations read — so it carries its own copy of the rule rather than
// importing the engine's, which lives under engine/internal and is not
// reachable from here in any case. TestReffoldNormalizePersonMatchesEngine
// binds the two together so they cannot drift.
func normalizePerson(s string) string {
	s = strings.TrimSpace(s)
	scheme, value, ok := splitPerson(s)
	if !ok {
		return foldPersonValue(s)
	}
	return strings.ToLower(scheme) + ":" + foldPersonValue(strings.TrimSpace(value))
}

// foldPersonValue applies the value half of the normalization rule in
// spec/identifiers.md §Normalization rules, pinned to Unicode personUnicodeVersion:
//
//  1. NFC
//  2. Unicode default case folding (UAX #21 §2.3 toCasefold, the full C+F
//     mappings, no locale tailoring)
//  3. NFC again
//
// One algorithm for every scheme. The trailing NFC is not redundant: case
// folding does not preserve a normal form, so folding NFC input can leave a
// composable sequence behind — U+017F followed by U+0301 folds to "s" plus
// U+0301, which is not NFC — and a rule that stopped after folding would not
// be idempotent. Normalization is applied at the producer, in the fold and
// again in the projection, so a rule that changed its answer on the second
// pass would be the same interop defect it exists to remove.
func foldPersonValue(s string) string {
	// ASCII is already NFC, and folding it is ASCII lowercasing, which is
	// what almost every real identifier needs. TestFoldValueASCIIFastPath
	// checks the shortcut against the general path rather than assuming it.
	if personIsASCII(s) {
		return personLowerASCII(s)
	}
	return personNFC(personCaseFold(personNFC(s)))
}

func personIsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func personLowerASCII(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if b == nil {
				b = []byte(s)
			}
			b[i] = c + ('a' - 'A')
		}
	}
	if b == nil {
		return s
	}
	return string(b)
}

// personCaseFold applies Unicode default case folding, the full C and F mappings of
// CaseFolding.txt, one code point at a time. That is the whole of it because
// toCasefold is context-free: unlike lowercasing, which has the final-sigma
// rule, no case-folding mapping depends on neighbouring characters. Bytes
// that are not valid UTF-8 are copied through untouched.
func personCaseFold(s string) string {
	var b strings.Builder
	copied := 0
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if f := ucd.Fold(r); f != "" {
			if b.Len() == 0 {
				b.Grow(len(s))
			}
			b.WriteString(s[copied:i])
			b.WriteString(f)
			copied = i + n
		}
		i += n
	}
	if copied == 0 {
		return s
	}
	b.WriteString(s[copied:])
	return b.String()
}

// personNFC returns s in Normalization Form C.
//
// The composition is done here, segment by segment, over a canonically
// ordered decomposition, rather than by a library's whole-string NFC, because
// of what spec/identifiers.md says no implementation may do: apply
// Stream-Safe Text. A library that implements UAX #15 §13 applies it
// unconditionally — past 30 consecutive non-starters it inserts U+034F and
// stops composing — and that is reachable well inside the 320-code-point
// bound. Its segment boundaries are Stream-Safe's too, a cut after 30
// non-starters rather than at a position nothing composes across.
//
// The Unicode facts come from internal/person/ucd; this code is the
// algorithm, and is swept exhaustively against CPython, which shares nothing
// with it.
func personNFC(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for len(s) > 0 {
		n := personSegmentLen(s)
		b.WriteString(personNFCSegment(s[:n]))
		s = s[n:]
	}
	return b.String()
}

// personSegmentLen returns the byte length of the leading normalization segment of
// s: its first rune, plus every following rune that cannot begin a segment of
// its own.
//
// The rule is ucd.BoundaryBefore — ccc == 0 and does not combine backwards —
// applied rune by rune. That is the definition of a position nothing can
// personCompose across, so cutting there cannot separate a composing pair, and it
// has no length limit.
//
// Combining backwards, and not merely being a non-starter, is what keeps
// Hangul whole: V (U+1161..U+1175) and T (U+11A8..U+11C2) have ccc == 0 and
// personCompose onto the syllable before them, as do spacing marks such as Grantha
// U+1133E and Tamil U+0BBE. All of them report BoundaryBefore false and stay
// with their base.
func personSegmentLen(s string) int {
	for i := 0; i < len(s); {
		// Invalid UTF-8 decodes one byte at a time, so this always advances
		// and cannot loop; a person identifier is not required to be well
		// formed for the fold to terminate on it.
		r, n := utf8.DecodeRuneInString(s[i:])
		if i > 0 && ucd.BoundaryBefore(r) {
			return i
		}
		i += n
	}
	return len(s)
}

// personNFCSegment composes one normalization segment. See personNFC for why it does the
// composing itself.
func personNFCSegment(seg string) string {
	if !utf8.ValidString(seg) {
		// Not a conforming identifier at all. Return the bytes untouched
		// rather than route them through a decoder that would replace them:
		// normalization is not where malformed input is decided, and identity
		// is at least deterministic and lossless.
		return seg
	}
	return personCompose(personDecompose(seg))
}

// personDecompose returns seg canonically decomposed and canonically ordered,
// alongside each rune's combining class, which the caller needs too and which
// is measurably worth computing once.
//
// Decomposition is applied one rune at a time because it is context-free —
// NFD(xy) is NFD(x) followed by NFD(y), reordered.
func personDecompose(seg string) ([]rune, []uint8) {
	rs := make([]rune, 0, len(seg))
	for _, r := range seg {
		if d := ucd.Decompose(r); d != "" {
			rs = append(rs, []rune(d)...)
		} else {
			rs = append(rs, r)
		}
	}
	cc := make([]uint8, len(rs))
	for i, r := range rs {
		cc[i] = ucd.CCC(r)
	}
	personCanonicalOrder(rs, cc)
	return rs, cc
}

// personCanonicalOrder applies UAX #15's Canonical Ordering Algorithm in place:
// non-starters are sorted by combining class, stably, and runes with ccc 0 are
// fixed points that nothing moves across.
//
// It sorts each maximal run of non-starters rather than the whole slice, and
// it is not the obvious insertion sort. A person identifier's segment length
// is bounded only by what an op body carries — Check is a producer-side guard
// and the fold deliberately does not call it — so this runs on attacker-chosen
// input on every reader of the repository, and the rule it replaced was
// linear. An O(m^2) sort here is an amplification vector, not a slow path.
func personCanonicalOrder(rs []rune, cc []uint8) {
	for i := 0; i < len(rs); {
		if cc[i] == 0 {
			i++
			continue
		}
		j := i
		for j < len(rs) && cc[j] != 0 {
			j++
		}
		personSortByCCC(rs[i:j], cc[i:j])
		i = j
	}
}

// personSortRunInsertionMax is the run length below which an insertion sort wins:
// almost every real combining sequence is one or two marks, and a counting
// sort's 256-entry histogram costs more than the whole run.
const personSortRunInsertionMax = 32

// personSortByCCC stably sorts one run of non-starters by combining class. Insertion
// sort for the short runs that occur in practice, counting sort — linear, and
// stable because it walks the run in order — for the long ones that make a
// quadratic sort worth attacking.
func personSortByCCC(rs []rune, cc []uint8) {
	if len(rs) < 2 {
		return
	}
	if len(rs) <= personSortRunInsertionMax {
		for i := 1; i < len(rs); i++ {
			r, c := rs[i], cc[i]
			j := i
			for ; j > 0 && cc[j-1] > c; j-- {
				rs[j], cc[j] = rs[j-1], cc[j-1]
			}
			rs[j], cc[j] = r, c
		}
		return
	}
	var counts [256]int
	for _, c := range cc {
		counts[c]++
	}
	sum := 0
	for i := range counts {
		counts[i], sum = sum, sum+counts[i]
	}
	outR := make([]rune, len(rs))
	outC := make([]uint8, len(cc))
	for i, c := range cc {
		outR[counts[c]], outC[counts[c]] = rs[i], c
		counts[c]++
	}
	copy(rs, outR)
	copy(cc, outC)
}

// personCompose applies UAX #15's Canonical Composition Algorithm to a canonically
// ordered decomposition. Every Unicode fact it needs — the combining classes,
// and whether a given pair composes — comes from internal/person/ucd.
//
// It tracks the last-retained starter (L) per UAX #15 so that
// backward-combining starters that appear as first elements of compositions
// (such as those introduced in Unicode 17.0.0) correctly compose when
// preceded by other text in a segment.
func personCompose(rs []rune, cc []uint8) string {
	if len(rs) == 0 {
		return ""
	}
	out := make([]rune, 0, len(rs))
	out = append(out, rs[0])
	lastStarter := -1
	if cc[0] == 0 {
		lastStarter = 0
	}
	maxRetained := -1
	for i, c := range rs[1:] {
		n := int(cc[i+1])
		if lastStarter >= 0 && maxRetained < n {
			if p, ok := ucd.Compose(out[lastStarter], c); ok {
				out[lastStarter] = p
				continue
			}
		}
		out = append(out, c)
		if n == 0 {
			lastStarter = len(out) - 1
			maxRetained = -1
		} else if n > maxRetained {
			maxRetained = n
		}
	}
	return string(out)
}

// personMaxNonStarterRun mirrors engine/internal/person's MaxNonStarterRun
// (spec/identifiers.md §Value shape: Stream-Safe Text). It sits outside the
// block TestReffoldIsTheSameAlgorithmAsTheEngine compares source-for-source,
// which stops at personCompose above, because the rule it backs is producer-side:
// reffold.go is the reference fold, not a reference producer, and has no
// Check of its own to mirror in full.
const personMaxNonStarterRun = 30

// PersonValueIsStreamSafe reports whether a person identifier's value
// conforms to spec/identifiers.md §Value shape: Stream-Safe Text: its NFD
// carries no run of more than personMaxNonStarterRun consecutive
// non-starters.
//
// It exists for two callers. TestInvalidPersonVectors uses it to check a
// testdata/persons vector whose rejection is enforced only at the producer,
// never by the person-id JSON Schema, which has no way to express a
// Canonical_Combining_Class-run rule — for that it is deliberately narrower
// than a reference Check, naming only this one rule, not the whole grammar.
// And engine/internal/person's own tests bind it to the engine's one
// definition of the same rule (IsStreamSafe), reaching across the package
// boundary from that side rather than through engine/state: api/engine.txt
// is generated from ./engine only, so an export here never lands on the
// engine's public API baseline, and engine/internal/person can import spec
// with no cycle — spec has no engine imports at all.
//
// The decomposition discipline matches engine/internal/person's maxRunLen: a
// run is measured rune by rune off each code point's canonical decomposition,
// which needs no reordering because a run cannot cross a starter.
func PersonValueIsStreamSafe(id string) bool {
	_, value, ok := splitPerson(id)
	if !ok {
		value = id
	}
	run := 0
	for _, r := range value {
		d := ucd.Decompose(r)
		if d == "" {
			d = string(r)
		}
		for _, dr := range d {
			if ucd.CCC(dr) == 0 {
				run = 0
				continue
			}
			run++
			if run > personMaxNonStarterRun {
				return false
			}
		}
	}
	return true
}

// PersonValueRepertoireOK reports whether a person identifier's value
// conforms to spec/identifiers.md §Value character repertoire: it carries
// none of the code points internal/textsafe.Forbidden names.
//
// It exists for the same reason PersonValueIsStreamSafe does, and sits
// beside it rather than in the source-for-source parity block above
// (foldPersonValue through personCompose): this is producer-side hygiene, not
// part of the fold, so there is no engine algorithm for
// TestReffoldIsTheSameAlgorithmAsTheEngine to compare it against
// line-for-line. TestInvalidPersonVectors uses it to check a
// testdata/persons/invalid vector whose rejection is enforced only at the
// producer, never by the person-id JSON Schema -- specifically the
// supplementary-plane Forbidden ranges (the tag block and friends), which
// an ECMA-262 character class cannot express without the u flag (see
// spec/identifiers.md §Rendering a person identifier and the schema's own
// pattern description).
//
// It delegates to internal/textsafe rather than mirroring the ranges a
// fourth time (spec/identifiers.md's table, the schema pattern, and
// internal/textsafe.Forbidden already carry the list) -- that import is
// stdlib-only and creates no cycle: textsafe imports only "strings", and
// spec imports nothing from engine.
func PersonValueRepertoireOK(id string) bool {
	_, value, ok := splitPerson(id)
	if !ok {
		value = id
	}
	_, bad := textsafe.First(value)
	return !bad
}

// PersonValueAssignedOK reports whether a person identifier's value conforms to
// spec/identifiers.md §Value character repertoire's unassigned-code-point rule:
// it carries no code point whose General_Category is Cn at Unicode 17.0.0.
//
// It sits beside PersonValueRepertoireOK on the same terms — producer-side
// hygiene, outside the parity block, there so TestInvalidPersonVectors can pin
// a testdata/persons/invalid vector to the one rule that rejects it. The rule
// is separate from the forbidden-code-point table because it is a different
// kind of rule: that table is a blocklist of what renders deceptively, this
// one exists because Unicode's stability policies freeze normalization and
// case folding only for assigned code points. Private use is assigned (Co);
// noncharacters are Cn.
func PersonValueAssignedOK(id string) bool {
	_, value, ok := splitPerson(id)
	if !ok {
		value = id
	}
	for i := 0; i < len(value); {
		r, n := utf8.DecodeRuneInString(value[i:])
		if !(r == utf8.RuneError && n == 1) && !ucd.Assigned(r) {
			return false
		}
		i += n
	}
	return true
}

// EffectiveTimes computes the causality-monotone effective timestamp
// t*(u) = max(u.time, max_{p in Parents_S(u)} t*(p))
// for all ops in the restricted input set for target objectID.
func EffectiveTimes(ops []OrderOp, objectID string) map[string]int64 {
	inSet := make(map[string]bool)
	for _, op := range ops {
		if op.ObjectID == objectID {
			inSet[op.ID] = true
		}
	}
	tStar, _ := EffectiveTimesInSet(ops, inSet)
	return tStar
}

// EffectiveTimesInSet computes effective timestamps for ops within an explicit inSet.
// Returns an error if a directed cycle is detected within the restricted DAG.
func EffectiveTimesInSet(ops []OrderOp, inSet map[string]bool) (map[string]int64, error) {
	tStar := make(map[string]int64, len(inSet))
	opMap := make(map[string]OrderOp, len(ops))
	for _, op := range ops {
		opMap[op.ID] = op
	}

	state := make(map[string]int, len(inSet)) // 0: unvisited, 1: visiting, 2: visited

	var getTStar func(id string) (int64, error)
	getTStar = func(id string) (int64, error) {
		if t, ok := tStar[id]; ok {
			return t, nil
		}
		if state[id] == 1 {
			return 0, fmt.Errorf("cycle detected involving op %q", id)
		}
		if state[id] == 2 {
			return tStar[id], nil
		}

		state[id] = 1
		op := opMap[id]
		res := op.Time
		for _, p := range op.Parents {
			if inSet[p] {
				pt, err := getTStar(p)
				if err != nil {
					return 0, err
				}
				if pt > res {
					res = pt
				}
			}
		}
		state[id] = 2
		tStar[id] = res
		return res, nil
	}

	for id := range inSet {
		if state[id] == 0 {
			if _, err := getTStar(id); err != nil {
				return nil, err
			}
		}
	}
	return tStar, nil
}

// TotalOrder produces the deterministic total order sequence L of ops for target objectID
// using Kahn's algorithm with a priority queue ordered by (t*, id).
//
// This is the spec's reference implementation of the total order algorithm defined in spec/fold.md §4.
func TotalOrder(ops []OrderOp, objectID string) ([]string, error) {
	inSet := make(map[string]bool)
	for _, op := range ops {
		if op.ObjectID == objectID {
			inSet[op.ID] = true
		}
	}
	if len(inSet) == 0 {
		return nil, nil
	}

	tStar, err := EffectiveTimesInSet(ops, inSet)
	if err != nil {
		return nil, fmt.Errorf("computing effective timestamps: %w", err)
	}

	inDegree := make(map[string]int, len(inSet))
	children := make(map[string][]string, len(inSet))
	for _, op := range ops {
		if !inSet[op.ID] {
			continue
		}
		var parentsInSet int
		for _, p := range op.Parents {
			if inSet[p] {
				parentsInSet++
				children[p] = append(children[p], op.ID)
			}
		}
		inDegree[op.ID] = parentsInSet
	}

	// Ready queue
	var ready []string
	for id, deg := range inDegree {
		if deg == 0 {
			ready = append(ready, id)
		}
	}

	var order []string
	for len(ready) > 0 {
		// Pick ready op with minimal (t*, id)
		bestIdx := 0
		bestID := ready[0]
		bestT := tStar[bestID]

		for i := 1; i < len(ready); i++ {
			candID := ready[i]
			candT := tStar[candID]
			if candT < bestT || (candT == bestT && candID < bestID) {
				bestIdx = i
				bestID = candID
				bestT = candT
			}
		}

		// Remove chosen from ready
		ready = append(ready[:bestIdx], ready[bestIdx+1:]...)

		order = append(order, bestID)

		// Unblock children
		for _, ch := range children[bestID] {
			inDegree[ch]--
			if inDegree[ch] == 0 {
				ready = append(ready, ch)
			}
		}
	}

	if len(order) != len(inSet) {
		return nil, fmt.Errorf("cycle detected in restricted DAG: emitted %d of %d ops", len(order), len(inSet))
	}

	return order, nil
}

// BuildReachabilityMap computes transitive reachability (isAncestor) in the restricted DAG.
func BuildReachabilityMap(ops []MergeOp, inSet map[string]bool) map[string]map[string]bool {
	parentsMap := make(map[string][]string, len(inSet))
	for _, op := range ops {
		if !inSet[op.ID] {
			continue
		}
		var pList []string
		for _, p := range op.Parents {
			if inSet[p] {
				pList = append(pList, p)
			}
		}
		parentsMap[op.ID] = pList
	}

	ancestors := make(map[string]map[string]bool, len(inSet))
	for id := range inSet {
		ancestors[id] = make(map[string]bool)
	}

	var dfs func(curr, target string)
	dfs = func(curr, target string) {
		for _, p := range parentsMap[curr] {
			if !ancestors[target][p] {
				ancestors[target][p] = true
				dfs(p, target)
			}
		}
	}

	for id := range inSet {
		dfs(id, id)
	}

	return ancestors
}

// UnknownOp records an operation that was preserved in the DAG and participated
// in ordering and ancestry, but contributed no field writes per FC-5.
type UnknownOp struct {
	Commit     string `json:"commit"`
	ObjectType string `json:"object_type"`
	OpType     string `json:"op_type"`
	OpVersion  int64  `json:"op_version"`
}

// FoldResult is the output of the reference fold: the materialized state, and
// the operations that contributed nothing to it.
type FoldResult struct {
	// State is the folded state, keyed by field name.
	State map[string]any
	// UnknownOps lists, in total order, the operations that were
	// preserved in the DAG and participated in ordering and ancestry but
	// contributed no field writes: operations matching no declared rule
	// (spec/fold.md §7) and operations a declared rule found uninterpretable
	// (spec/fold.md §7.1).
	UnknownOps []UnknownOp
}

// uninterpretable reports whether an operation is uninterpretable because a
// field carrying a declared merge rule holds a JSON value that is not the
// shape the field's strategy consumes, per spec/fold.md §7.1.
//
// The unit is the operation, not the field. An operation is the unit of
// signature and of intent, so half-applying one asserts something nobody
// signed; and one op-level rule is implementable identically in every
// language, where a per-field fallback needs specifying once per field per
// strategy.
//
// Only a field with a declared rule is inspected: unknown fields and unknown
// op types keep preserve-and-ignore untouched. The check reads the value at
// the declared field and, where the strategy consumes a collection, its
// immediate elements. It never recurses — fold treats structured payloads such
// as anchors as opaque data (spec/fold.md §6), so an anchor whose context
// collar is null is well formed.
func uninterpretable(op MergeOp, rules []FieldRule) bool {
	for _, r := range rules {
		if !opMatchesRule(op, r) {
			continue
		}
		if !ruleAccepts(r, op.OpType, op.Body) {
			return true
		}
	}
	return false
}

// opMatchesRule reports whether rule r governs op. An empty OpType or a zero
// OpVersion on the rule matches anything; the same holds for ObjectType
// (spec/fold.md §5), read straight off op with no clock, I/O or ambient
// state involved. A zero OpVersion or empty ObjectType on the op does NOT
// also match anything (WRIT-275): the op-envelope schema requires
// op_version >= 1 and a non-empty object_type on every op that reaches the
// log, so that half of the wildcard was unreachable through the log and
// existed only for a hand-built op fed straight to Fold.
func opMatchesRule(op MergeOp, r FieldRule) bool {
	if r.OpType != "" && r.OpType != op.OpType {
		return false
	}
	if r.OpVersion != 0 && r.OpVersion != op.OpVersion {
		return false
	}
	if r.ObjectType != "" && r.ObjectType != op.ObjectType {
		return false
	}
	return true
}

// refOrSetItems returns the items one side of an OR-set body carries. The side
// holds a string or an array of strings; anything else made the op
// uninterpretable (spec/fold.md §7.1) before it reached here, so a non-string
// is unreachable and skipped rather than rendered.
//
// This is the reducer half of ruleAccepts's set-observed-remove arm and MUST
// consume exactly what that accepts. They disagreed once: the predicate took a
// bare string on a side and the reducer read only arrays, so `{"add": "solo"}`
// folded to the empty set — the silent drop that "skip invents an absence"
// names.
func refOrSetItems(raw any) []string {
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

// ruleAccepts reports whether body carries a value rule r's strategy can
// consume. A field the body does not carry is not a write and is accepted.
// opType is the operation's op_type, consulted only for the
// set-observed-remove scalar shape (spec/fold.md §5.4), which has no member
// names of its own to read the side off and so reads it off the op type
// instead.
func ruleAccepts(r FieldRule, opType string, body map[string]any) bool {
	// A keyed-lww key component is consumed as a string whether or not the
	// declared field itself is present in this body: it decides which register
	// the write addresses.
	if r.Strategy == "keyed-lww" {
		for _, kf := range r.Key {
			if v, present := body[kf]; present && !isRefString(v) {
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
			return refOrSetAccepts(r.Field, body[r.Field], body)
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
		// claimed.
		return v != nil
	case "append":
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
	case "set-union":
		return isRefStringOrStringSlice(v)
	case "set-observed-remove":
		if _, nested := v.(map[string]any); !nested {
			// Scalar shape: the op type says which side the value lands on
			// (spec/fold.md §5.4). An op type mapping to neither side has no
			// side to consume the write on and is uninterpretable per §7.1,
			// rather than being silently dropped (WRIT-338).
			add, remove := refOrSetScalarSide(opType)
			if !add && !remove {
				return false
			}
		}
		return refOrSetAccepts(r.Field, v, body)
	case "tombstone":
		_, ok := v.(bool)
		return ok
	case "lattice":
		return isRefString(v)
	case "multi-value":
		return isRefString(v)
	}
	return true
}

// refOrSetScalarSide reports which side of an OR-set the scalar body shape
// (spec/fold.md §5.4) maps opType onto: op type "add" or a "add-" prefix maps
// to the add side, "remove" or a "remove-" prefix maps to the remove side. An
// op type mapping to neither side returns false, false — ruleAccepts then
// makes the whole operation uninterpretable per §7.1 rather than silently
// dropping the write.
//
// This is the predicate half of the set-observed-remove reducer's scalar
// branch below and MUST agree with it exactly: both call this function so
// the mapping cannot drift between the two, the way refOrSetItems' own doc
// comment warns ruleAccepts and the reducer must not.
func refOrSetScalarSide(opType string) (add, remove bool) {
	if opType == "add" || strings.HasPrefix(opType, "add-") {
		return true, false
	}
	if opType == "remove" || strings.HasPrefix(opType, "remove-") {
		return false, true
	}
	return false, false
}

func refOrSetAccepts(field string, v any, body map[string]any) bool {
	sideOK := func(member any, present bool) bool {
		if !present {
			return true
		}
		if obj, ok := member.(map[string]any); ok {
			for _, side := range []string{"add", "remove"} {
				m, p := obj[side]
				if !p || isRefStringOrStringSlice(m) {
					continue
				}
				return false
			}
			return true
		}
		return isRefStringOrStringSlice(member)
	}
	if field == "add" || field == "remove" {
		sibling := "remove"
		if field == "remove" {
			sibling = "add"
		}
		member, sidePresent := body[sibling]
		if !sideOK(member, sidePresent) {
			return false
		}
		vMember, vPresent := body[field]
		return sideOK(vMember, vPresent)
	}
	return sideOK(v, true)
}

func isRefString(v any) bool {
	_, ok := v.(string)
	return ok
}

// isRefStringSlice reports whether v is an array whose every element is a
// string. The []string arm exists because a body assembled in Go rather than
// decoded from JSON can carry one; a decoded body never does.
func isRefStringSlice(v any) bool {
	switch slice := v.(type) {
	case []any:
		for _, item := range slice {
			if !isRefString(item) {
				return false
			}
		}
		return true
	case []string:
		return true
	}
	return false
}

func isRefStringOrStringSlice(v any) bool {
	return isRefString(v) || isRefStringSlice(v)
}

// Fold is the spec's reference fold reducer. It executes deterministic fold reduction
// on an input set of operations against the declared catalogue field rules.
//
// This is the normative reference reducer used to produce and check golden fold outputs.
// Engine reducers (WRIT-25/26/27) are independent implementations validated against the same goldens.
func Fold(ops []MergeOp, rules []FieldRule) (FoldResult, error) {
	if len(ops) == 0 {
		return FoldResult{State: make(map[string]any)}, nil
	}

	objectID := ops[0].ObjectID
	inSet := make(map[string]bool)
	var orderOps []OrderOp
	opMap := make(map[string]MergeOp, len(ops))

	for _, op := range ops {
		opMap[op.ID] = op
		if op.ObjectID == objectID {
			inSet[op.ID] = true
		}
		orderOps = append(orderOps, OrderOp{
			ID:       op.ID,
			Parents:  op.Parents,
			Time:     op.Time,
			ObjectID: op.ObjectID,
		})
	}

	totalOrder, err := TotalOrder(orderOps, objectID)
	if err != nil {
		return FoldResult{}, fmt.Errorf("spec: ordering ops: %w", err)
	}

	ancestors := BuildReachabilityMap(ops, inSet)
	isAncestor := func(a, b string) bool {
		return ancestors[b][a]
	}

	// Quarantine, in total order, the ops that contribute no field writes: ops
	// matching no declared rule (spec/fold.md §7) and ops whose body a declared
	// rule cannot consume (spec/fold.md §7.1). Both remain full members of the
	// restricted DAG — they are in the total order and in every ancestry
	// calculation — and neither is an error. One bad op costs that op, never
	// the object.
	rejected := make(map[string]bool)
	var unknownOps []UnknownOp
	var reduceOrder []string
	for _, id := range totalOrder {
		op := opMap[id]
		known := false
		for _, r := range rules {
			if opMatchesRule(op, r) {
				known = true
				break
			}
		}
		if known && uninterpretable(op, rules) {
			rejected[id] = true
			known = false
		}
		if known {
			reduceOrder = append(reduceOrder, id)
		} else {
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
		}
	}

	// Find all rules that match ops actually present in the input set
	matchedRulesByField := make(map[string][]FieldRule)
	for _, r := range rules {
		for _, op := range ops {
			if !inSet[op.ID] || rejected[op.ID] {
				continue
			}
			if opMatchesRule(op, r) {
				hasWrite := false
				if _, present := op.Body[r.Field]; present || op.OpType == "delete" || op.OpType == "undelete" {
					hasWrite = true
				} else if r.Strategy == "set-observed-remove" {
					if r.Field == "add" && op.Body["remove"] != nil {
						hasWrite = true
					} else if r.Field == "remove" && op.Body["add"] != nil {
						hasWrite = true
					}
				}
				if hasWrite {
					targetKey := r.TargetKey()
					matchedRulesByField[targetKey] = append(matchedRulesByField[targetKey], r)
					break
				}
			}
		}
	}

	// Canonical rule order (spec/fold.md §5): a target's matching rules
	// contribute in ascending (op_type, op_version, field), never in the
	// order the caller's slice happened to list them. A rule table declares
	// at most one rule per (op_type, op_version, field) tuple, so this
	// comparison is total and needs no stable sort to be deterministic.
	for _, frs := range matchedRulesByField {
		sort.Slice(frs, func(i, j int) bool { return fieldRuleOrderLess(frs[i], frs[j]) })
	}

	state := make(map[string]any)

	// Iterate fields in deterministic order
	var targetKeys []string
	for f := range matchedRulesByField {
		targetKeys = append(targetKeys, f)
	}
	sort.Strings(targetKeys)

	// Every strategy below walks frs (every rule bound to targetKey, not
	// just frs[0]) inside its op loop and applies each one that matches —
	// never breaking after the first. Rules sharing a target MUST agree on
	// strategy (spec/fold.md §5's "MUST agree on every merge attribute"),
	// so a second matching rule for the same op is the same strategy
	// running again, not a competing behavior to choose between: an
	// operation writing two fields that share one target under one
	// (op_type, op_version) envelope contributes both writes. This is
	// deliberate (WRIT-201) — the reference and the engine reducer
	// (engine/internal/fold) must apply-all identically, and
	// spec/testdata/fold/merge/append-two-fields-shared-target.json pins it.
	// Where the strategy cares which of one operation's two writes lands
	// first (append's list position, a same-operation lww/create-once/
	// keyed-lww write), frs is already in canonical rule order above, so
	// neither implementation consults a caller's slice order for it.
	for _, targetKey := range targetKeys {
		frs := matchedRulesByField[targetKey]
		if len(frs) == 0 {
			continue
		}
		primaryRule := frs[0]

		switch primaryRule.Strategy {
		case "lww":
			for _, id := range reduceOrder {
				op := opMap[id]
				for _, r := range frs {
					if opMatchesRule(op, r) {
						if val, present := op.Body[r.Field]; present && val != nil {
							// Empty scalar contract (spec/fold.md §5.1): empty strings
							// (including person identifiers that normalize to empty) are
							// preserved in the generic fold map as deliberate scalar writes.
							if s, ok := val.(string); ok && r.NormalizesValue() {
								val = normalizePerson(s)
							}
							state[targetKey] = val
						}
					}
				}
			}

		case "create-once":
			for _, id := range reduceOrder {
				op := opMap[id]
				for _, r := range frs {
					if opMatchesRule(op, r) {
						if _, alreadySet := state[targetKey]; !alreadySet {
							if val, present := op.Body[r.Field]; present && val != nil {
								// create-once is a scalar position
								// (spec/value-types.md §Normalization), so a
								// person-ref value normalizes here exactly as
								// it does under lww.
								if s, ok := val.(string); ok && r.NormalizesValue() {
									val = normalizePerson(s)
								}
								state[targetKey] = val
							}
						}
					}
				}
			}

		case "set-union":
			unionSet := make(map[string]bool)
			hasSet := false
			for _, id := range reduceOrder {
				op := opMap[id]
				for _, r := range frs {
					if opMatchesRule(op, r) {
						if raw, present := op.Body[r.Field]; present {
							hasSet = true
							normalizeItem := func(it string) string {
								if r.NormalizesItems() {
									return normalizePerson(it)
								}
								return it
							}
							// Elements that are the empty string are dropped (spec/fold.md §5.3).
							add := func(item string) {
								if norm := normalizeItem(item); norm != "" {
									unionSet[norm] = true
								}
							}
							// Every item is a string: an op carrying anything
							// else at this field is uninterpretable and never
							// reaches a reducer (spec/fold.md §7.1).
							switch val := raw.(type) {
							case []any:
								for _, item := range val {
									if s, isStr := item.(string); isStr {
										add(s)
									}
								}
							case []string:
								for _, item := range val {
									add(item)
								}
							case string:
								add(val)
							}
						}
					}
				}
			}
			if hasSet {
				result := make([]string, 0, len(unionSet))
				for k := range unionSet {
					result = append(result, k)
				}
				sort.Strings(result)
				state[targetKey] = result
			}

		case "set-observed-remove":
			type addRecord struct {
				opID string
				item string
			}
			var adds []addRecord
			type removeRecord struct {
				opID string
				item string
			}
			var removes []removeRecord
			hasOps := false

			for _, op := range ops {
				if !inSet[op.ID] || rejected[op.ID] {
					continue
				}
				for _, r := range frs {
					if opMatchesRule(op, r) {
						var addItems, remItems []string
						if r.Field == "add" || r.Field == "remove" {
							if m, ok := op.Body["add"].(map[string]any); ok {
								addItems = append(addItems, refOrSetItems(m["add"])...)
								remItems = append(remItems, refOrSetItems(m["remove"])...)
							} else {
								addItems = append(addItems, refOrSetItems(op.Body["add"])...)
							}
							if m, ok := op.Body["remove"].(map[string]any); ok {
								addItems = append(addItems, refOrSetItems(m["add"])...)
								remItems = append(remItems, refOrSetItems(m["remove"])...)
							} else {
								remItems = append(remItems, refOrSetItems(op.Body["remove"])...)
							}
						} else if bodyMap, ok := op.Body[r.Field].(map[string]any); ok {
							addItems = append(addItems, refOrSetItems(bodyMap["add"])...)
							remItems = append(remItems, refOrSetItems(bodyMap["remove"])...)
						} else if raw, ok := op.Body[r.Field]; ok && raw != nil {
							// Scalar shape: which side raw lands on is decided
							// by op.OpType, on exactly the terms ruleAccepts
							// above already validated the op against, via the
							// same refOrSetScalarSide helper.
							if add, remove := refOrSetScalarSide(op.OpType); add {
								addItems = append(addItems, refOrSetItems(raw)...)
							} else if remove {
								remItems = append(remItems, refOrSetItems(raw)...)
							}
						}

						if len(addItems) > 0 || len(remItems) > 0 {
							hasOps = true
							// Person-valued fields normalize per spec/identifiers.md;
							// every other item is taken verbatim. Items that are empty
							// after normalization are dropped from both sides of the
							// OR-set, whatever the op type (spec/fold.md §5.4).
							normalizeItem := func(it string) string {
								if r.NormalizesItems() {
									return normalizePerson(it)
								}
								return it
							}

							// Every item is a string; see the set-union arm. A
							// side holds one item or an array of them, and
							// refOrSetItems consumes exactly what ruleAccepts
							// admitted.
							for _, it := range addItems {
								if item := normalizeItem(it); item != "" {
									adds = append(adds, addRecord{opID: op.ID, item: item})
								}
							}
							for _, it := range remItems {
								if item := normalizeItem(it); item != "" {
									removes = append(removes, removeRecord{opID: op.ID, item: item})
								}
							}
						}
					}
				}
			}

			if hasOps {
				presentSet := make(map[string]bool)
				for _, add := range adds {
					removed := false
					for _, rem := range removes {
						if rem.item == add.item && isAncestor(add.opID, rem.opID) {
							removed = true
							break
						}
					}
					if !removed {
						presentSet[add.item] = true
					}
				}
				result := make([]string, 0, len(presentSet))
				for k := range presentSet {
					result = append(result, k)
				}
				sort.Strings(result)
				state[targetKey] = result
			}

		case "append":
			var list []any
			hasAppend := false
			for _, id := range reduceOrder {
				op := opMap[id]
				for _, r := range frs {
					if opMatchesRule(op, r) {
						if raw, present := op.Body[r.Field]; present {
							hasAppend = true
							if slice, ok := raw.([]any); ok {
								list = append(list, slice...)
							} else {
								list = append(list, raw)
							}
						}
					}
				}
			}
			if hasAppend {
				// The initial state of an append field is the empty list, not
				// null (spec/fold.md §5.5), so an op writing the field with an
				// empty array folds to [] — a written-but-empty list, which is
				// what it says.
				if list == nil {
					list = []any{}
				}
				state[targetKey] = list
			}

		case "tombstone":
			var deletes []string
			var undeletes []string
			hasTombstone := false
			for _, op := range ops {
				if !inSet[op.ID] || rejected[op.ID] {
					continue
				}
				for _, r := range frs {
					if opMatchesRule(op, r) {
						// The payload field is authoritative when the
						// operation carries it, and the op type is metadata
						// describing intent, not the data (spec/fold.md
						// §5.6); the literal "delete"/"undelete" op type
						// decides only when the operation writes no value
						// for the field at all. §7.1 already makes an
						// operation uninterpretable before this runs if the
						// field is present but not a bool.
						if val, hasField := op.Body[r.Field]; hasField {
							if val == true {
								deletes = append(deletes, op.ID)
							} else {
								undeletes = append(undeletes, op.ID)
							}
							hasTombstone = true
						} else if op.OpType == "delete" {
							deletes = append(deletes, op.ID)
							hasTombstone = true
						} else if op.OpType == "undelete" {
							undeletes = append(undeletes, op.ID)
							hasTombstone = true
						}
					}
				}
			}

			if hasTombstone {
				isDeleted := false
				for _, d := range deletes {
					cleared := false
					for _, u := range undeletes {
						if isAncestor(d, u) {
							cleared = true
							break
						}
					}
					if !cleared {
						isDeleted = true
						break
					}
				}
				state[targetKey] = isDeleted
			}

		case "lattice":
			if len(primaryRule.Lattice) == 0 {
				return FoldResult{}, fmt.Errorf("spec: target %q: lattice strategy for field %q requires non-empty lattice elements", targetKey, primaryRule.Field)
			}
			rankMap := make(map[string]int, len(primaryRule.Lattice))
			for i, elem := range primaryRule.Lattice {
				rankMap[elem] = i
			}
			currentRank := -1
			var currentVal string
			hasLattice := false
			for _, id := range reduceOrder {
				op := opMap[id]
				for _, r := range frs {
					if opMatchesRule(op, r) {
						if raw, present := op.Body[r.Field]; present {
							// The value is a string; see the set-union arm. A
							// string outside the declared lattice is ignored
							// rather than rejected: that is a value from a
							// future vocabulary, which preserve-and-ignore
							// covers.
							valStr, isStr := raw.(string)
							if !isStr {
								continue
							}
							if rk, ok := rankMap[valStr]; ok {
								if rk > currentRank {
									currentRank = rk
									currentVal = valStr
									hasLattice = true
								}
							}
						}
					}
				}
			}
			if hasLattice {
				state[targetKey] = currentVal
			}

		case "keyed-lww":
			// A caller-supplied rule table, unlike one derived from a schema
			// in the log (WRIT-234 withholds the whole target there), can
			// bind one keyed-lww target to rules whose Key tuples differ in
			// length. The ordering below compares key components pairwise
			// (spec/fold.md §5's "ordered by their key tuples, compared
			// component-wise"), which is undefined across tuples of
			// different length and previously panicked with index out of
			// range. Refuse the rule table outright rather than making the
			// comparator defensively total: silently ordering shorter-first
			// would bless two prefix-related tuples as permanent distinct
			// registers, inventing a merge semantics nobody asked for
			// (WRIT-234's ruling, carried forward by WRIT-239's). This
			// checks arity, not full Key equality: rules sharing a target
			// may legally disagree on which columns make up an equal-length
			// key (TestFoldKeyedLWWMultiRuleField).
			for _, fr := range frs[1:] {
				if len(fr.Key) != len(primaryRule.Key) {
					err := fmt.Errorf("keyed-lww rules disagree on key arity (%d vs %d)", len(primaryRule.Key), len(fr.Key))
					return FoldResult{}, fmt.Errorf("spec: target %q: %w", targetKey, err)
				}
			}
			if len(primaryRule.Key) == 0 {
				return FoldResult{}, fmt.Errorf("spec: target %q: keyed-lww strategy for field %q requires non-empty key", targetKey, primaryRule.Field)
			}

			type keyedEntry struct {
				key   []string
				value any
			}
			latest := make(map[string]*keyedEntry)
			hasKeyed := false
			for _, id := range reduceOrder {
				op := opMap[id]
				for _, rule := range frs {
					if opMatchesRule(op, rule) {
						val, present := op.Body[rule.Field]
						if !present {
							continue
						}
						if rule.NormalizesValue() {
							if s, isStr := val.(string); isStr {
								val = normalizePerson(s)
							}
						}
						hasKeyed = true
						key := make([]string, 0, len(rule.Key))
						for _, kf := range rule.Key {
							// Every present key component is a string; see the
							// set-union arm. An absent one contributes the
							// empty component.
							vStr, _ := op.Body[kf].(string)
							if rule.NormalizesKey(kf) {
								vStr = normalizePerson(vStr)
							}
							key = append(key, vStr)
						}
						// The map key groups writes addressing the same
						// register and is never serialized. It is a JSON array
						// so the encoding is injective over the key tuple
						// without depending on any one language's rendering of
						// a list.
						keyBytes, err := json.Marshal(key)
						if err != nil {
							return FoldResult{}, fmt.Errorf("spec: encoding keyed-lww key for field %q: %w", targetKey, err)
						}
						latest[string(keyBytes)] = &keyedEntry{key: key, value: val}
					}
				}
			}

			if hasKeyed {
				entries := make([]*keyedEntry, 0, len(latest))
				for _, e := range latest {
					entries = append(entries, e)
				}
				sort.Slice(entries, func(i, j int) bool {
					a, b := entries[i].key, entries[j].key
					for x := range a {
						if a[x] != b[x] {
							return a[x] < b[x]
						}
					}
					return false
				})
				keyed := make([]any, 0, len(entries))
				for _, e := range entries {
					keyed = append(keyed, map[string]any{"key": e.key, "value": e.value})
				}
				state[targetKey] = keyed
			}

		case "multi-value":
			type mvWrite struct {
				opID string
				val  string
			}
			var writes []mvWrite
			for _, id := range reduceOrder {
				op := opMap[id]
				for _, rule := range frs {
					if opMatchesRule(op, rule) {
						if raw, ok := op.Body[rule.Field]; ok && raw != nil {
							if s, ok := raw.(string); ok {
								// multi-value is a scalar position
								// (spec/value-types.md §Normalization), so a
								// person-ref value normalizes here exactly as
								// it does under lww and create-once.
								if rule.NormalizesValue() {
									s = normalizePerson(s)
								}
								writes = append(writes, mvWrite{opID: op.ID, val: s})
							}
						}
					}
				}
			}
			if len(writes) > 0 {
				var maximal []mvWrite
				for i, w1 := range writes {
					superseded := false
					for j, w2 := range writes {
						if i != j && isAncestor(w1.opID, w2.opID) {
							superseded = true
							break
						}
					}
					if !superseded {
						maximal = append(maximal, w1)
					}
				}
				seen := make(map[string]bool)
				var vals []any
				for _, w := range maximal {
					if !seen[w.val] {
						seen[w.val] = true
						vals = append(vals, w.val)
					}
				}
				sort.Slice(vals, func(i, j int) bool {
					return vals[i].(string) < vals[j].(string)
				})
				if len(vals) == 1 {
					state[targetKey] = vals[0]
				} else {
					state[targetKey] = vals
				}
			}

		default:
			return FoldResult{}, fmt.Errorf("spec: target %q: unknown strategy %q", targetKey, primaryRule.Strategy)
		}
	}

	return FoldResult{State: state, UnknownOps: unknownOps}, nil
}
