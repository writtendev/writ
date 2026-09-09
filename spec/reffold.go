package spec

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// personUnicodeVersion is the Unicode version spec/identifiers.md pins the
// person-identifier normalization algorithm to. x/text selects its tables by
// Go build tag rather than by module version, so this is checked against the
// tables actually compiled in rather than assumed.
const personUnicodeVersion = "15.0.0"

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

// personFoldCaser performs Unicode default case folding. cases.Fold documents the
// returned Caser as stateless and safe for concurrent use, so one is shared.
var personFoldCaser = cases.Fold()

// Cherokee uppercase letters case-fold to themselves. Cherokee is the one
// script whose folding maps lowercase *up* — CaseFolding.txt has
// "AB70..ABBF; C; 13A0..13EF" and "13F8..13FD; C; 13F0..13F5" — and x/text
// encodes case mappings as XOR deltas, which cannot express a mapping that is
// not an involution. cases.Fold therefore toggles these code points instead of
// holding them fixed: it answers U+AB70 for U+13A0, where CPython, ICU and
// CaseFolding.txt all answer U+13A0 (golang/go#46101, open since 2021).
//
// Folding is idempotent by definition, so a toggle is a defect rather than a
// tailoring, and a normalization built on it would not settle: U+13A0 and
// U+AB70 would swap places on every pass.
const personCherokeeLo, personCherokeeHi = 0x13A0, 0x13F5

// personCaseFold applies Unicode default case folding, holding the Cherokee code
// points x/text toggles at their correct fixed points.
//
// Folding runs of the string separately and copying the fixed points through
// gives the same answer as folding the whole string would, because toCasefold
// is context-free: unlike lowercasing, which has the final-sigma rule, no
// case-folding mapping depends on neighbouring characters.
func personCaseFold(s string) string {
	if !personHasCherokee(s) {
		return personFoldCaser.String(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	run := 0
	for i, r := range s {
		if r < personCherokeeLo || r > personCherokeeHi {
			continue
		}
		b.WriteString(personFoldCaser.String(s[run:i]))
		b.WriteRune(r)
		run = i + utf8.RuneLen(r)
	}
	b.WriteString(personFoldCaser.String(s[run:]))
	return b.String()
}

func personHasCherokee(s string) bool {
	for _, r := range s {
		if r >= personCherokeeLo && r <= personCherokeeHi {
			return true
		}
	}
	return false
}

// personNFC returns s in Normalization Form C.
//
// It does not ask x/text to normalize anything larger than a single rune or a
// single pair, because x/text's answer for a whole string is wrong in four
// ways. The first three are defects; the fourth is a deliberate behaviour that
// this specification does not want:
//
//  1. Over-composition. A composition pair is packed into a 32-bit key as
//     uint16(a)<<16|uint16(b) (unicode/norm/forminfo.go), so a starter above
//     U+FFFF is truncated to its low 16 bits and matches the BMP entry sharing
//     them: NFC of U+10041 U+0300 returns "À". 16,956 (starter, mark) pairs
//     compose falsely this way, and each one merges two distinct people.
//  2. Stream-Safe Text, applied unconditionally through String, Bytes and Iter
//     alike: past 30 consecutive non-starters it inserts U+034F and stops
//     composing. spec/identifiers.md forbids that, and it is reachable well
//     inside the 320-code-point bound.
//  3. Composing across a blocker. NFC of U+00C5 U+0BD7 U+0316 U+0301 returns
//     U+01FA U+0BD7 U+0316: the acute composed onto the base across a ccc-0
//     mark that blocks it, and was then dropped from the output.
//  4. Stream-safe *boundaries*. norm.NextBoundaryInString is driven by
//     streamSafe.next, so it reports a cut after 30 non-starters rather than a
//     position nothing composes across. x/text carries a TODO at
//     unicode/norm/normalize.go saying the two are not the same thing.
//
// A round-trip guard alone cannot cover this. NFD(NFC(x)) == NFD(x) catches
// (1), because a false composition is not canonically equivalent. It is blind
// to (2), because NFD inserts the same joiner and it is present on both sides
// of the comparison. It sees (3) only because a code point goes missing, and
// the only repair available to a guard — fall back to NFD — then discards the
// composition that was legitimate.
//
// So the composition is done here, segment by segment, over a canonically
// ordered decomposition, with x/text asked only the three questions it answers
// correctly: what one rune decomposes to, what a code point's combining class
// is, and whether one specific pair composes. All three are swept exhaustively
// against CPython, which shares no code with x/text.
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
// The rule is Properties.BoundaryBefore — ccc == 0 and does not combine
// backwards — applied rune by rune. That is the definition of a position
// nothing can compose across, so cutting there cannot separate a composing
// pair, and it has no length limit. See personNFC (4) for why this is not
// norm.NextBoundaryInString.
//
// Combining backwards, and not merely being a non-starter, is what keeps
// Hangul whole: V (U+1161..U+1175) and T (U+11A8..U+11C2) have ccc == 0 and
// compose onto the syllable before them, as do spacing marks such as Grantha
// U+1133E and Tamil U+0BBE. All of them report BoundaryBefore false and stay
// with their base.
func personSegmentLen(s string) int {
	for i := 0; i < len(s); {
		p := norm.NFC.PropertiesString(s[i:])
		if i > 0 && p.BoundaryBefore() {
			return i
		}
		n := p.Size()
		if n <= 0 {
			// Invalid UTF-8. Advance a byte so this cannot loop; a person
			// identifier is not required to be well formed for the fold to
			// terminate on it.
			n = 1
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
// NFD(xy) is NFD(x) followed by NFD(y), reordered — and because no single
// rune's decomposition is long enough to reach the stream-safe limit, so
// x/text answers each one correctly even where it cannot answer for the whole
// segment.
func personDecompose(seg string) ([]rune, []uint8) {
	rs := make([]rune, 0, len(seg))
	for _, r := range seg {
		rs = append(rs, []rune(norm.NFD.String(string(r)))...)
	}
	cc := make([]uint8, len(rs))
	for i, r := range rs {
		cc[i] = personCCC(r)
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
// and whether a given pair composes — comes from x/text; nothing here is a
// table this repository has to keep current.
//
// It composes only onto rs[0] and never promotes a later starter to be a new
// base, which UAX #15 permits in general. That is sound here because of an
// empirical property of the pinned Unicode version rather than anything about
// the algorithm: of the 75 starters in 15.0.0 that combine backwards — and so
// stay inside a segment rather than beginning one — exactly zero are the first
// element of any composition. A later starter therefore has nothing to compose
// with even if it were promoted. TestPinnedUnicodeVersion is what keeps this
// true: a Unicode version bump has to re-establish it.
func personCompose(rs []rune, cc []uint8) string {
	if len(rs) == 0 {
		return ""
	}
	out := make([]rune, 0, len(rs))
	out = append(out, rs[0])
	// A segment that opens on a non-starter has no starter to compose onto.
	composable := cc[0] == 0
	// UAX #15 D115: c is blocked from the base when *any* character already
	// retained between them has ccc 0, or a class at least as high as c's.
	// Looking only at the last one is not enough — canonical ordering leaves
	// ccc-0 marks where they are, so a retained blocker can end up behind a
	// later mark of lower class and would otherwise be forgotten.
	blockedAll := false
	maxRetained := -1
	for i, c := range rs[1:] {
		n := int(cc[i+1])
		blocked := blockedAll || maxRetained >= n
		if composable && !blocked {
			if p, ok := personCombine(out[0], c); ok {
				out[0] = p
				continue
			}
		}
		out = append(out, c)
		if n == 0 {
			blockedAll = true
		} else if n > maxRetained {
			maxRetained = n
		}
	}
	return string(out)
}

// personCombine reports the primary composite of a and b, if there is one. It asks
// x/text, on a two-rune string that cannot reach the stream-safe limit, and
// applies the round-trip guard so a composition invented by the truncated key
// (personNFC (1)) is refused.
func personCombine(a, b rune) (rune, bool) {
	in := string([]rune{a, b})
	out := norm.NFC.String(in)
	if norm.NFD.String(out) != norm.NFD.String(in) {
		return 0, false
	}
	rs := []rune(out)
	if len(rs) == 1 {
		return rs[0], true
	}
	return 0, false
}

// personCCC reports a rune's canonical combining class. It encodes into a stack
// buffer rather than calling PropertiesString(string(r)), which heap-allocates
// on every call and is invoked once per code point of every identifier folded.
func personCCC(r rune) uint8 {
	var buf [utf8.UTFMax]byte
	n := utf8.EncodeRune(buf[:], r)
	return norm.NFC.Properties(buf[:n]).CCC()
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
// as comment anchors as opaque data (spec/fold.md §6), so an anchor whose
// context collar is null is well formed.
func uninterpretable(op MergeOp, rules []FieldRule) bool {
	for _, r := range rules {
		if !opMatchesRule(op, r) {
			continue
		}
		if !ruleAccepts(r, op.Body) {
			return true
		}
	}
	return false
}

// opMatchesRule reports whether rule r governs op. An empty OpType or a zero
// OpVersion on the rule, or a zero OpVersion on the op, matches anything; the
// same holds for ObjectType (spec/fold.md §5), read straight off op with no
// clock, I/O or ambient state involved.
func opMatchesRule(op MergeOp, r FieldRule) bool {
	if r.OpType != "" && r.OpType != op.OpType {
		return false
	}
	if r.OpVersion != 0 && op.OpVersion != 0 && r.OpVersion != op.OpVersion {
		return false
	}
	if r.ObjectType != "" && op.ObjectType != "" && r.ObjectType != op.ObjectType {
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
func ruleAccepts(r FieldRule, body map[string]any) bool {
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
				} else if r.Field == "subject" && op.OpType == "approval" && op.Author.Email != "" {
					hasWrite = true
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
							if strings.HasPrefix(op.OpType, "add-") || op.OpType == "add" {
								addItems = append(addItems, refOrSetItems(raw)...)
							} else if strings.HasPrefix(op.OpType, "remove-") || op.OpType == "remove" {
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
						if op.OpType == "delete" || op.Body[r.Field] == true {
							deletes = append(deletes, op.ID)
							hasTombstone = true
						} else if op.OpType == "undelete" || op.Body[r.Field] == false {
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
							if rule.Field == "subject" && op.OpType == "approval" && op.Author.Email != "" {
								val = normalizePerson("email:" + op.Author.Email)
							} else {
								continue
							}
						} else if rule.NormalizesValue() {
							if s, isStr := val.(string); isStr {
								norm := normalizePerson(s)
								if norm == "" && rule.Field == "subject" && op.OpType == "approval" && op.Author.Email != "" {
									norm = normalizePerson("email:" + op.Author.Email)
								}
								val = norm
							}
						}
						hasKeyed = true
						key := make([]string, 0, len(rule.Key))
						for _, kf := range rule.Key {
							// Every present key component is a string; see the
							// set-union arm. An absent one contributes the
							// empty component, except for approval subject which
							// falls back to the commit author's email.
							vStr, _ := op.Body[kf].(string)
							if rule.NormalizesKey(kf) {
								vStr = normalizePerson(vStr)
							}
							if vStr == "" && kf == "subject" && op.OpType == "approval" && op.Author.Email != "" {
								vStr = normalizePerson("email:" + op.Author.Email)
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
		}
	}

	return FoldResult{State: state, UnknownOps: unknownOps}, nil
}
