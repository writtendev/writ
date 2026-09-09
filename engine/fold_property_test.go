package writ_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	writ "github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/spec"
)

// --------------------------------------------------------------------------
// 1. Helpers & Conversions
// --------------------------------------------------------------------------

var interestingStrings = []string{
	"",
	"   ",
	"\t\n\r",
	"simple",
	"MixedCaseString",
	"  leading space",
	"trailing space  ",
	"  both spaces  ",
	"日本語",
	"Ünïcodé",
	"email:alice@example.com",
	"email:  ALICE@example.com  ",
	"user:octocat",
	"user:  OctoCat  ",
	// NFC vs NFD unicode pair for "café"
	"\u0063\u0061\u0066\u00e9",       // NFC
	"\u0063\u0061\u0066\u00e5\u0301", // NFD
	// Cherokee fixed-points
	"\u13a0",
	"\uab70",
}

func isValidOpSet(ops []codec.Op) bool {
	if len(ops) == 0 {
		return true
	}
	objID := ops[0].ObjectID
	objType := ops[0].ObjectType
	if objID == "" || objType == "" {
		return false
	}
	seen := make(map[string]bool, len(ops))
	for _, o := range ops {
		if o.ID == "" || seen[o.ID] || o.ObjectID != objID || o.ObjectType != objType || o.OpVersion < 1 {
			return false
		}
		seen[o.ID] = true
		if len(o.Body) > 0 {
			var bm map[string]any
			if err := json.Unmarshal(o.Body, &bm); err != nil {
				return false
			}
			// WRIT-197: codec.Op here is built straight from fuzzer-mutated
			// JSON (json.Unmarshal(data, &fc)), never through
			// codec.DecodePayload -- the real ingestion boundary every op
			// crosses before Fold ever sees it. DecodePayload's byte-equality
			// rule (canonicaljson.Marshal, engine/codec/decode.go) refuses a
			// payload whose canonicalization fails outright, which per
			// canonicaljson.Marshal's own doc comment happens for exactly
			// three classes of input that encoding/json accepts and silently
			// normalizes instead of erroring on: bytes that are not valid
			// UTF-8, an object with a duplicate member key, and a string
			// carrying a lone (unpaired) UTF-16 surrogate escape. Unlike a
			// plain string field, o.Body is a json.RawMessage: unmarshaling
			// into it round-trips raw bytes verbatim with no sanitization, so
			// fuzz-mutated JSON can carry any of the three straight past the
			// json.Unmarshal check above into Fold. A create-once field's
			// byte-exact raw preservation (engine/internal/fold/strategy.go,
			// WRIT-124) then carries the offending bytes into State
			// unchanged -- whether they sit in the top-level Body or nested
			// inside one raw-preserved field's value -- and the harness's
			// own canonical-JSON comparison (toCanonicalJSON) legitimately
			// refuses to encode them -- a refusal DecodePayload would have
			// produced too, just earlier. Reject here instead, the same way
			// DecodePayload would (by running the same canonicaljson.Marshal
			// it runs), rather than let Fold see an op no real ingestion path
			// would ever produce.
			if _, err := canonicaljson.Marshal(o.Body); err != nil {
				return false
			}
		}
	}
	return true
}

func toSpecOps(ops []codec.Op) []spec.MergeOp {
	mergeOps := make([]spec.MergeOp, 0, len(ops))
	for _, o := range ops {
		var bm map[string]any
		if len(o.Body) > 0 {
			_ = json.Unmarshal(o.Body, &bm)
		}
		if bm == nil {
			bm = make(map[string]any)
		}
		mergeOps = append(mergeOps, spec.MergeOp{
			ID:         o.ID,
			Parents:    o.Parents,
			Time:       o.Author.When.UTC().Unix(),
			ObjectID:   o.ObjectID,
			ObjectType: o.ObjectType,
			OpType:     o.OpType,
			OpVersion:  o.OpVersion,
			Author: spec.MergeAuthor{
				Name:  o.Author.Name,
				Email: o.Author.Email,
			},
			Body: bm,
		})
	}
	return mergeOps
}

func toSpecRules(rules []writ.Rule) []spec.FieldRule {
	specRules := make([]spec.FieldRule, 0, len(rules))
	for _, r := range rules {
		specRules = append(specRules, spec.FieldRule{
			OpType:     r.OpType,
			OpVersion:  r.OpVersion,
			Field:      r.Field,
			Target:     r.Target,
			Strategy:   r.Strategy,
			Key:        r.Key,
			Lattice:    r.Lattice,
			ValueType:  r.ValueType,
			Enum:       r.Enum,
			MaxLength:  r.MaxLength,
			KeyTypes:   r.KeyTypes,
			ObjectType: r.ObjectType,
		})
	}
	return specRules
}

// filterValidRules drops any rule spec.ValidateFieldRule would reject
// before it reaches the fold. Schema-sourced rules are gated the same way
// in production (engine/schema.go's RulesFromSchemas, WRIT-186 §Rule
// validation), and the abstract fuzz path's fc.Rules is fuzz-mutated JSON
// with no producer standing behind it: a mutation that turns Strategy into
// "" or an unknown string must not reach NewAccumulator here either
// (WRIT-196) — that recurrence, after this filter lands, is a real
// failure, not the flake it fixes.
func filterValidRules(rules []writ.Rule) []writ.Rule {
	var out []writ.Rule
	for i, sr := range toSpecRules(rules) {
		if spec.ValidateFieldRule(sr) == nil {
			out = append(out, rules[i])
		}
	}
	return out
}

func toCanonicalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	canon, err := canonicaljson.Marshal(b)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal failed: %v", err)
	}
	return canon
}

// The five rule tables below are stated here as literal []writ.Rule values.
// Writ hard-codes no object type but `schema`, so there is no installed
// vocabulary to read realistic field-rule shapes out of any more: a property
// suite over the fold declares its own, exactly as a consumer's schema
// declares the ones its objects fold under.
//
// Between them they cover every strategy in the closed catalogue
// (spec/fold.md §5) — lww, create-once, multi-value, append, set-union,
// set-observed-remove, keyed-lww, lattice and tombstone — and every value
// type that normalizes, including the shapes the historical regression
// vectors below were minimized from: a multi-column keyed-lww whose key
// carries a person-ref column, an add/remove pair collapsing onto one
// target, and an enum. The abstract synthetic stream
// (generateAbstractSyntheticStream) exercises the same catalogue against
// arbitrary JSON values; these exercise it against value-typed fields.

// The keyed-lww keys the tables below share. Every rule in one keyed group
// must declare the identical key, so each group names one slice and one
// key-type map rather than restating them per rule; nothing mutates them.
var (
	widgetApprovalKey      = []string{"subject", "revision"}
	widgetApprovalKeyTypes = map[string]string{"subject": "person-ref", "revision": "git-oid"}
	widgetGaugeKey         = []string{"revision", "name"}
	widgetGaugeKeyTypes    = map[string]string{"revision": "git-oid", "name": "string"}
	linkKey                = []string{"target"}
	linkKeyTypes           = map[string]string{"target": "object-ref"}
)

// widgetRules is the widest table: lww over strings and an enum, append,
// an add/remove pair collapsed onto one target, and two multi-column
// keyed-lww groups — one of which keys on a person-ref column, the shape
// spec/fold.md §5.7 normalizes and regressionVectorWRIT112 pins.
func widgetRules() []writ.Rule {
	return []writ.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "revision", OpVersion: 1, Field: "base", Strategy: "append", ValueType: "git-oid"},
		{OpType: "revision", OpVersion: 1, Field: "head", Strategy: "append", ValueType: "git-oid"},
		{OpType: "set-status", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "enum", Enum: []string{"draft", "open", "closed", "merged"}},
		{OpType: "set-status", OpVersion: 1, Field: "merge_commit", Strategy: "lww", ValueType: "git-oid"},
		{OpType: "set-status", OpVersion: 1, Field: "reason", Strategy: "lww", ValueType: "string"},
		{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref"},
		{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref"},
		{OpType: "approval", OpVersion: 1, Field: "revision", Strategy: "keyed-lww", Key: widgetApprovalKey, KeyTypes: widgetApprovalKeyTypes, ValueType: "git-oid"},
		{OpType: "approval", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww", Key: widgetApprovalKey, KeyTypes: widgetApprovalKeyTypes, ValueType: "enum", Enum: []string{"approve", "request-changes", "none"}},
		{OpType: "approval", OpVersion: 1, Field: "subject", Strategy: "keyed-lww", Key: widgetApprovalKey, KeyTypes: widgetApprovalKeyTypes, ValueType: "person-ref"},
		{OpType: "approval", OpVersion: 1, Field: "message", Strategy: "keyed-lww", Key: widgetApprovalKey, KeyTypes: widgetApprovalKeyTypes, ValueType: "text"},
		{OpType: "gauge", OpVersion: 1, Field: "revision", Target: "gauge_revision", Strategy: "keyed-lww", Key: widgetGaugeKey, KeyTypes: widgetGaugeKeyTypes, ValueType: "git-oid"},
		{OpType: "gauge", OpVersion: 1, Field: "name", Strategy: "keyed-lww", Key: widgetGaugeKey, KeyTypes: widgetGaugeKeyTypes, ValueType: "string"},
		{OpType: "gauge", OpVersion: 1, Field: "state", Strategy: "keyed-lww", Key: widgetGaugeKey, KeyTypes: widgetGaugeKeyTypes, ValueType: "enum", Enum: []string{"pending", "success", "failure", "error", "cancelled", "neutral", "skipped"}},
		{OpType: "gauge", OpVersion: 1, Field: "url", Strategy: "keyed-lww", Key: widgetGaugeKey, KeyTypes: widgetGaugeKeyTypes, ValueType: "string"},
		{OpType: "gauge", OpVersion: 1, Field: "description", Target: "gauge_description", Strategy: "keyed-lww", Key: widgetGaugeKey, KeyTypes: widgetGaugeKeyTypes, ValueType: "string"},
		{OpType: "gauge", OpVersion: 1, Field: "started_at", Strategy: "keyed-lww", Key: widgetGaugeKey, KeyTypes: widgetGaugeKeyTypes, ValueType: "timestamp"},
		{OpType: "gauge", OpVersion: 1, Field: "completed_at", Strategy: "keyed-lww", Key: widgetGaugeKey, KeyTypes: widgetGaugeKeyTypes, ValueType: "timestamp"},
		{OpType: "gauge", OpVersion: 1, Field: "external_id", Strategy: "keyed-lww", Key: widgetGaugeKey, KeyTypes: widgetGaugeKeyTypes, ValueType: "string"},
		{OpType: "tag", OpVersion: 1, Field: "add", Target: "tags", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "tag", OpVersion: 1, Field: "remove", Target: "tags", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "link", OpVersion: 1, Field: "target", Strategy: "keyed-lww", Key: linkKey, KeyTypes: linkKeyTypes, ValueType: "object-ref"},
		{OpType: "link", OpVersion: 1, Field: "target_type", Strategy: "keyed-lww", Key: linkKey, KeyTypes: linkKeyTypes, ValueType: "string"},
		{OpType: "link", OpVersion: 1, Field: "relation", Strategy: "keyed-lww", Key: linkKey, KeyTypes: linkKeyTypes, ValueType: "enum", Enum: []string{"fixes", "relates", "none"}},
	}
}

// gadgetRules adds the numeric and ordering value types — int, number and
// position — to the same set-observed-remove and single-column keyed-lww
// shapes widgetRules carries.
func gadgetRules() []writ.Rule {
	return []writ.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "rank", Strategy: "lww", ValueType: "int"},
		{OpType: "create", OpVersion: 1, Field: "estimate", Strategy: "lww", ValueType: "number"},
		{OpType: "create", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "rank", Strategy: "lww", ValueType: "int"},
		{OpType: "update", OpVersion: 1, Field: "estimate", Strategy: "lww", ValueType: "number"},
		{OpType: "update", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "set-state", OpVersion: 1, Field: "state", Strategy: "lww", ValueType: "object-ref"},
		{OpType: "set-state", OpVersion: 1, Field: "reason", Strategy: "lww", ValueType: "string"},
		{OpType: "set-state", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref"},
		{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref"},
		{OpType: "tag", OpVersion: 1, Field: "add", Target: "tags", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "tag", OpVersion: 1, Field: "remove", Target: "tags", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "link", OpVersion: 1, Field: "target", Strategy: "keyed-lww", Key: linkKey, KeyTypes: linkKeyTypes, ValueType: "object-ref"},
		{OpType: "link", OpVersion: 1, Field: "target_type", Strategy: "keyed-lww", Key: linkKey, KeyTypes: linkKeyTypes, ValueType: "string"},
		{OpType: "link", OpVersion: 1, Field: "relation", Strategy: "keyed-lww", Key: linkKey, KeyTypes: linkKeyTypes, ValueType: "enum", Enum: []string{"fixes", "relates", "none"}},
	}
}

// sprocketRules is the create-once / multi-value / tombstone table: an
// untyped create-once field kept byte-exact (spec/fold.md §5.2), a text
// field under lww beside one under multi-value, and a tombstone driven by
// the `delete` op type rather than a body field.
func sprocketRules() []writ.Rule {
	return []writ.Rule{
		{OpType: "create", OpVersion: 1, Field: "subject", Strategy: "create-once"},
		{OpType: "create", OpVersion: 1, Field: "text", Strategy: "lww", ValueType: "text"},
		{OpType: "create", OpVersion: 1, Field: "body", Strategy: "multi-value", ValueType: "text"},
		{OpType: "create", OpVersion: 1, Field: "in_reply_to", Strategy: "create-once", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "anchor", Strategy: "create-once", ValueType: "anchor"},
		{OpType: "edit", OpVersion: 1, Field: "text", Strategy: "lww", ValueType: "text"},
		{OpType: "edit", OpVersion: 1, Field: "body", Strategy: "multi-value", ValueType: "text"},
		{OpType: "delete", OpVersion: 1, Field: "deleted", Strategy: "tombstone", ValueType: "bool"},
		{OpType: "resolve", OpVersion: 1, Field: "resolved", Strategy: "lww", ValueType: "bool"},
		{OpType: "resolve", OpVersion: 1, Field: "resolved_by", Strategy: "lww", ValueType: "person-ref"},
	}
}

// gizmoRules is the membership table: an add/remove pair that does NOT
// collapse onto a shared target (two op types writing the same field name
// instead), plus the two catalogue strategies no other table here uses —
// set-union and lattice.
func gizmoRules() []writ.Rule {
	return []writ.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "set-status", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "enum", Enum: []string{"planned", "active", "paused", "completed", "canceled"}},
		{OpType: "set-status", OpVersion: 1, Field: "reason", Strategy: "lww", ValueType: "string"},
		{OpType: "advance", OpVersion: 1, Field: "stage", Strategy: "lattice", Lattice: []string{"planned", "active", "completed"}, ValueType: "string"},
		{OpType: "watch", OpVersion: 1, Field: "watcher", Strategy: "set-union", ValueType: "person-ref"},
		{OpType: "add-item", OpVersion: 1, Field: "item", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "remove-item", OpVersion: 1, Field: "item", Strategy: "set-observed-remove", ValueType: "object-ref"},
	}
}

// thingRules pairs timestamp-valued lww fields written by two different op
// types with the same non-collapsing membership pair gizmoRules uses.
func thingRules() []writ.Rule {
	return []writ.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "starts_at", Strategy: "lww", ValueType: "timestamp"},
		{OpType: "create", OpVersion: 1, Field: "ends_at", Strategy: "lww", ValueType: "timestamp"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "set-dates", OpVersion: 1, Field: "starts_at", Strategy: "lww", ValueType: "timestamp"},
		{OpType: "set-dates", OpVersion: 1, Field: "ends_at", Strategy: "lww", ValueType: "timestamp"},
		{OpType: "add-item", OpVersion: 1, Field: "item", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "remove-item", OpVersion: 1, Field: "item", Strategy: "set-observed-remove", ValueType: "object-ref"},
	}
}

// --------------------------------------------------------------------------
// 3. Three-Way Assertions
// --------------------------------------------------------------------------

func assertUnknownOpsParity(t *testing.T, writU []writ.UnknownOp, specU []spec.UnknownOp) {
	t.Helper()
	if len(writU) != len(specU) {
		t.Fatalf("unknown ops length mismatch: writ=%d, spec=%d", len(writU), len(specU))
	}
	for i, u := range writU {
		su := specU[i]
		if u.Commit != su.Commit || u.ObjectType != su.ObjectType || u.OpType != su.OpType || u.OpVersion != su.OpVersion {
			t.Fatalf("unknown op mismatch at %d: writ=%+v, spec=%+v", i, u, su)
		}
	}
}

func assertTotalOrderMatchesSpec(t *testing.T, ops []codec.Op, writRefs []writ.OpRef) {
	t.Helper()
	if len(ops) == 0 {
		return
	}
	orderOps := make([]spec.OrderOp, len(ops))
	for i, o := range ops {
		orderOps[i] = spec.OrderOp{
			ID:       o.ID,
			Parents:  o.Parents,
			Time:     o.Author.When.UTC().Unix(),
			ObjectID: o.ObjectID,
		}
	}
	specOrder, err := spec.TotalOrder(orderOps, ops[0].ObjectID)
	if err != nil {
		t.Fatalf("spec.TotalOrder failed: %v", err)
	}
	if len(writRefs) != len(specOrder) {
		t.Fatalf("total order length mismatch: writ=%d, spec=%d", len(writRefs), len(specOrder))
	}
	for i, ref := range writRefs {
		if ref.Commit != specOrder[i] {
			t.Fatalf("total order mismatch at %d: writ=%s, spec=%s", i, ref.Commit, specOrder[i])
		}
	}
}

func assertThreeWayFoldAbstract(t *testing.T, ops []codec.Op, rules []writ.Rule) {
	t.Helper()
	writRes, writErr := writ.Fold(ops, rules)
	mergeOps := toSpecOps(ops)
	specRules := toSpecRules(rules)
	specRes, specErr := spec.Fold(mergeOps, specRules)

	if (writErr != nil) != (specErr != nil) {
		t.Fatalf("error parity mismatch: writErr=%v, specErr=%v", writErr, specErr)
	}
	if writErr != nil {
		return
	}

	// 1. Total order
	assertTotalOrderMatchesSpec(t, ops, writRes.TotalOrder)

	// 2. Unknown ops
	assertUnknownOpsParity(t, writRes.UnknownOps, specRes.UnknownOps)

	// 3. State canonical JSON byte equality
	writJSON := toCanonicalJSON(t, writRes.State)
	specJSON := toCanonicalJSON(t, specRes.State)
	if !bytes.Equal(writJSON, specJSON) {
		t.Fatalf("canonical JSON mismatch between writ.Fold and spec.Fold:\n writ: %s\n spec: %s", string(writJSON), string(specJSON))
	}
}

// --------------------------------------------------------------------------
// 4. DAG & Operation Stream Generator
// --------------------------------------------------------------------------

func randomValue(rng *rand.Rand, depth int) any {
	if depth > 2 {
		return interestingStrings[rng.Intn(len(interestingStrings))]
	}
	switch rng.Intn(8) {
	case 0:
		return interestingStrings[rng.Intn(len(interestingStrings))]
	case 1:
		return rng.Intn(1000)
	case 2:
		return math.Round(rng.Float64()*1000) / 10
	case 3:
		return rng.Intn(2) == 1
	case 4:
		m := make(map[string]any)
		n := rng.Intn(3) + 1
		for i := 0; i < n; i++ {
			k := fmt.Sprintf("k%d", i)
			m[k] = randomValue(rng, depth+1)
		}
		return m
	case 5:
		n := rng.Intn(3)
		arr := make([]any, n)
		for i := 0; i < n; i++ {
			arr[i] = randomValue(rng, depth+1)
		}
		return arr
	case 6:
		return nil
	default:
		return "test-val"
	}
}

func generateDAGSkeleton(rng *rand.Rand, numOps int, objectID, objectType string) []codec.Op {
	numWriters := 1 + rng.Intn(5) // 1 to 5 writer chains
	writerTips := make([]string, numWriters)
	allOps := make([]codec.Op, 0, numOps)
	seenIDs := make(map[string]bool)

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	forceIdenticalTime := rng.Intn(3) == 0
	commonTime := baseTime + 100

	for i := 0; i < numOps; i++ {
		var id string
		for {
			b := make([]byte, 20)
			rng.Read(b)
			id = hex.EncodeToString(b)
			if !seenIDs[id] {
				seenIDs[id] = true
				break
			}
		}

		w := rng.Intn(numWriters)
		var parents []string
		if writerTips[w] != "" {
			parents = append(parents, writerTips[w])
		}

		// 0 to 2 causal parents from earlier ops (acyclic)
		if len(allOps) > 0 && rng.Intn(3) > 0 {
			randIdx := rng.Intn(len(allOps))
			pID := allOps[randIdx].ID
			already := false
			for _, p := range parents {
				if p == pID {
					already = true
					break
				}
			}
			if !already {
				parents = append(parents, pID)
			}
		}

		writerTips[w] = id

		var opTime int64
		if forceIdenticalTime && rng.Intn(2) == 0 {
			opTime = commonTime // Simultaneous timestamp forcing alphabetical SHA tiebreak
		} else {
			opTime = baseTime + int64(rng.Intn(1000))
		}

		op := codec.Op{
			ID:      id,
			Parents: parents,
			Author: codec.Identity{
				Name:  fmt.Sprintf("Writer-%d", w),
				Email: fmt.Sprintf("w%d@example.test", w),
				When:  time.Unix(opTime, 0).UTC(),
			},
			Envelope: codec.Envelope{
				ObjectID:   objectID,
				ObjectType: objectType,
				OpVersion:  1,
			},
		}
		allOps = append(allOps, op)
	}

	return allOps
}

func generateAbstractSyntheticStream(rng *rand.Rand) ([]codec.Op, []writ.Rule) {
	numOps := 5 + rng.Intn(15)
	ops := generateDAGSkeleton(rng, numOps, "obj-synthetic", "synthetic")

	rules := []writ.Rule{
		{OpType: "op", OpVersion: 1, Field: "field_lww", Strategy: "lww"},
		{OpType: "op", OpVersion: 1, Field: "field_create_once", Strategy: "create-once"},
		{OpType: "op", OpVersion: 1, Field: "field_set_union", Strategy: "set-union"},
		{OpType: "op", OpVersion: 1, Field: "add", Strategy: "set-observed-remove"},
		{OpType: "op", OpVersion: 1, Field: "remove", Strategy: "set-observed-remove"},
		{OpType: "op", OpVersion: 1, Field: "field_append", Strategy: "append"},
		{OpType: "op", OpVersion: 1, Field: "deleted", Strategy: "tombstone"},
		{OpType: "op", OpVersion: 1, Field: "field_lattice", Strategy: "lattice", Lattice: []string{"draft", "ready", "approved", "merged"}},
		{OpType: "op", OpVersion: 1, Field: "field_keyed", Strategy: "keyed-lww", Key: []string{"k1", "k2"}},
	}

	latticeVals := []string{"draft", "ready", "approved", "merged", "unknown_lattice"}

	for i := range ops {
		// Forward-compatibility test: unknown op_type, future op_version, or unrecognized fields
		isUnknown := rng.Intn(10) == 0
		if isUnknown {
			ops[i].OpType = "future-type"
			ops[i].OpVersion = int64(2 + rng.Intn(5))
			body := map[string]any{
				"unrecognized_field": randomValue(rng, 1),
			}
			ops[i].Body, _ = json.Marshal(body)
			continue
		}

		ops[i].OpType = "op"
		body := make(map[string]any)

		// LWW
		if rng.Intn(2) == 0 {
			body["field_lww"] = randomValue(rng, 0)
		}
		// Create-once: string, number, bool, object, array, null, absent
		if rng.Intn(2) == 0 {
			body["field_create_once"] = randomValue(rng, 0)
		}
		// Set-union: string, number, bool, object, array, null, absent (WRIT-126)
		if rng.Intn(2) == 0 {
			switch rng.Intn(3) {
			case 0:
				body["field_set_union"] = interestingStrings[rng.Intn(len(interestingStrings))]
			case 1:
				body["field_set_union"] = []string{
					interestingStrings[rng.Intn(len(interestingStrings))],
					interestingStrings[rng.Intn(len(interestingStrings))],
				}
			case 2:
				body["field_set_union"] = randomValue(rng, 0)
			}
		}
		// Set-observed-remove: string, number, bool, object, array, null, absent (flat strings/arrays, nested maps, and non-strings/null)
		if rng.Intn(2) == 0 {
			switch rng.Intn(4) {
			case 0:
				item := fmt.Sprintf("item-%d", rng.Intn(5))
				if rng.Intn(2) == 0 {
					body["add"] = item
				} else {
					body["remove"] = item
				}
			case 1:
				items := []string{
					fmt.Sprintf("item-%d", rng.Intn(5)),
					interestingStrings[rng.Intn(len(interestingStrings))],
				}
				if rng.Intn(2) == 0 {
					body["add"] = items
				} else {
					body["remove"] = items
				}
			case 2:
				nested := make(map[string]any)
				if rng.Intn(2) == 0 {
					nested["add"] = []string{fmt.Sprintf("item-%d", rng.Intn(5))}
				}
				if rng.Intn(2) == 0 {
					nested["remove"] = []string{fmt.Sprintf("item-%d", rng.Intn(5))}
				}
				if rng.Intn(2) == 0 {
					body["add"] = nested
				} else {
					body["remove"] = nested
				}
			case 3:
				if rng.Intn(2) == 0 {
					body["add"] = randomValue(rng, 0)
				} else {
					body["remove"] = randomValue(rng, 0)
				}
			}
		}
		// Append: string, number, bool, object, array, null, absent
		if rng.Intn(2) == 0 {
			switch rng.Intn(4) {
			case 0:
				body["field_append"] = []any{} // empty array append preserves []
			case 1:
				// Heterogeneous slice
				body["field_append"] = []any{
					interestingStrings[rng.Intn(len(interestingStrings))],
					rng.Intn(1000),
					rng.Intn(2) == 1,
				}
			case 2:
				body["field_append"] = randomValue(rng, 0)
			case 3:
				body["field_append"] = interestingStrings[rng.Intn(len(interestingStrings))]
			}
		}
		// Tombstone: bool, and non-bool value types (string, number, object, array, null), absent
		if rng.Intn(3) == 0 {
			if rng.Intn(2) == 0 {
				body["deleted"] = (rng.Intn(2) == 1)
			} else {
				body["deleted"] = randomValue(rng, 0)
			}
		}
		// Lattice: string in/out of lattice, and non-string value types (number, bool, object, array, null), absent
		if rng.Intn(2) == 0 {
			if rng.Intn(2) == 0 {
				body["field_lattice"] = latticeVals[rng.Intn(len(latticeVals))]
			} else {
				body["field_lattice"] = randomValue(rng, 0)
			}
		}
		// Keyed-LWW: string, number, bool, object, array, null, absent for value, and string, absent, non-string keys (WRIT-124)
		if rng.Intn(2) == 0 {
			switch rng.Intn(4) {
			case 0:
				body["k1"] = fmt.Sprintf("group-%d", rng.Intn(3))
			case 1:
				body["k1"] = interestingStrings[rng.Intn(len(interestingStrings))]
			case 2:
				body["k1"] = randomValue(rng, 0)
			case 3:
				// absent
			}

			switch rng.Intn(4) {
			case 0:
				body["k2"] = fmt.Sprintf("sub-%d", rng.Intn(3))
			case 1:
				body["k2"] = interestingStrings[rng.Intn(len(interestingStrings))]
			case 2:
				body["k2"] = randomValue(rng, 0)
			case 3:
				// absent
			}

			body["field_keyed"] = randomValue(rng, 0)
		}

		ops[i].Body, _ = json.Marshal(body)
	}

	return ops, rules
}

func generateWidgetStream(rng *rand.Rand) ([]codec.Op, []writ.Rule, string) {
	numOps := 6 + rng.Intn(12)
	ops := generateDAGSkeleton(rng, numOps, "w-property", "widget")
	rules := widgetRules()

	// First op: create
	createBody := map[string]any{
		"title":       "Initial Property Widget Title",
		"description": "Initial description",
	}
	ops[0].OpType = "create"
	ops[0].Body, _ = json.Marshal(createBody)

	for i := 1; i < len(ops); i++ {
		// Forward-compatibility test
		if rng.Intn(12) == 0 {
			ops[i].OpType = "custom-widget-op"
			ops[i].OpVersion = 2
			ops[i].Body, _ = json.Marshal(map[string]any{"extra": "val"})
			continue
		}

		switch rng.Intn(7) {
		case 0:
			// update
			ops[i].OpType = "update"
			ops[i].Body, _ = json.Marshal(map[string]any{
				"title":       interestingStrings[rng.Intn(len(interestingStrings))],
				"description": interestingStrings[rng.Intn(len(interestingStrings))],
			})
		case 1:
			// set-status
			ops[i].OpType = "set-status"
			ops[i].Body, _ = json.Marshal(map[string]any{
				"status":       []string{"open", "merged", "closed"}[rng.Intn(3)],
				"merge_commit": "abcdef1234567890abcdef1234567890abcdef12",
				"reason":       interestingStrings[rng.Intn(len(interestingStrings))],
			})
		case 2:
			// revision: test both symmetric and asymmetric (base only or head only) writes
			ops[i].OpType = "revision"
			b := make(map[string]any)
			switch rng.Intn(3) {
			case 0:
				b["base"] = fmt.Sprintf("base-%d", i)
				b["head"] = fmt.Sprintf("head-%d", i)
			case 1:
				b["base"] = fmt.Sprintf("base-%d", i)
			case 2:
				b["head"] = fmt.Sprintf("head-%d", i)
			}
			ops[i].Body, _ = json.Marshal(b)
		case 3:
			// approval
			ops[i].OpType = "approval"
			subj := ""
			if rng.Intn(3) > 0 {
				subj = interestingStrings[rng.Intn(len(interestingStrings))]
			}
			verdict := []string{"approve", "request-changes", "none", ""}[rng.Intn(4)]
			b := map[string]any{
				"revision": "head-1",
				"verdict":  verdict,
				"message":  interestingStrings[rng.Intn(len(interestingStrings))],
			}
			if subj != "" {
				b["subject"] = subj
			}
			ops[i].Body, _ = json.Marshal(b)
		case 4:
			// gauge: test presence and absence of state and other fields
			ops[i].OpType = "gauge"
			b := map[string]any{
				"revision": "head-1",
			}
			if rng.Intn(4) > 0 {
				b["name"] = fmt.Sprintf("gauge-%d", rng.Intn(3))
			}
			if rng.Intn(4) > 0 {
				b["state"] = []string{"pending", "success", "failure"}[rng.Intn(3)]
			}
			if rng.Intn(2) == 0 {
				b["url"] = "https://example.test/gauge"
			}
			if rng.Intn(2) == 0 {
				b["description"] = "gauge run"
			}
			ops[i].Body, _ = json.Marshal(b)
		case 5:
			// link: test presence and absence of relation and target_type
			ops[i].OpType = "link"
			b := map[string]any{
				"target": fmt.Sprintf("gadget-%d", rng.Intn(3)),
			}
			if rng.Intn(2) == 0 {
				b["target_type"] = "gadget"
			}
			if rng.Intn(4) > 0 {
				b["relation"] = []string{"fixes", "relates-to", "none", ""}[rng.Intn(4)]
			}
			ops[i].Body, _ = json.Marshal(b)
		case 6:
			// tag and assign can both appear in the same widget stream,
			// drawing from the same underlying identifier pool: a tag
			// item can name the identical string an assign op introduces
			// (assign.add's normalization is a no-op on a colonless string),
			// the overlap WRIT-198's collapsed-target fix must survive —
			// disjoint pools could never exercise a tag.remove naming a
			// value an assign.add introduced.
			opType := []string{"assign", "tag"}[rng.Intn(2)]
			ops[i].OpType = opType
			item := fmt.Sprintf("shared-%d", rng.Intn(4))
			b := make(map[string]any)
			if rng.Intn(2) == 0 {
				if rng.Intn(2) == 0 {
					b["add"] = item
				} else {
					b["add"] = []string{item, interestingStrings[rng.Intn(len(interestingStrings))]}
				}
			} else {
				if rng.Intn(2) == 0 {
					b["remove"] = item
				} else {
					b["remove"] = []string{item}
				}
			}
			ops[i].Body, _ = json.Marshal(b)
		}
	}

	return ops, rules, ""
}

func generateGadgetStream(rng *rand.Rand) ([]codec.Op, []writ.Rule, string) {
	numOps := 5 + rng.Intn(10)
	ops := generateDAGSkeleton(rng, numOps, "g-property", "gadget")
	rules := gadgetRules()

	ops[0].OpType = "create"
	ops[0].Body, _ = json.Marshal(map[string]any{
		"title":       "Gadget Title",
		"description": "Gadget Description",
		"rank":        rng.Intn(5),
		"estimate":    math.Round(rng.Float64()*100) / 10,
		"position":    "aN",
	})

	for i := 1; i < len(ops); i++ {
		if rng.Intn(10) == 0 {
			ops[i].OpType = "unknown-gadget-op"
			ops[i].OpVersion = 2
			ops[i].Body, _ = json.Marshal(map[string]any{"foo": "bar"})
			continue
		}

		switch rng.Intn(4) {
		case 0:
			ops[i].OpType = "update"
			ops[i].Body, _ = json.Marshal(map[string]any{
				"title":       interestingStrings[rng.Intn(len(interestingStrings))],
				"description": interestingStrings[rng.Intn(len(interestingStrings))],
				"rank":        rng.Intn(5),
				"estimate":    math.Round(rng.Float64()*100) / 10,
			})
		case 1:
			ops[i].OpType = "set-state"
			ops[i].Body, _ = json.Marshal(map[string]any{
				"state":    []string{"open", "in-progress", "closed", ""}[rng.Intn(4)],
				"reason":   interestingStrings[rng.Intn(len(interestingStrings))],
				"position": fmt.Sprintf("a%d", rng.Intn(5)),
			})
		case 2:
			ops[i].OpType = "link"
			b := map[string]any{
				"target": fmt.Sprintf("widget-%d", rng.Intn(3)),
			}
			if rng.Intn(2) == 0 {
				b["target_type"] = "widget"
			}
			if rng.Intn(4) > 0 {
				b["relation"] = []string{"fixed-by", "relates-to", "none", ""}[rng.Intn(4)]
			}
			ops[i].Body, _ = json.Marshal(b)
		case 3:
			// tag and assign draw from the same underlying identifier
			// pool (see generateWidgetStream's case 6), so a tag item can
			// name the identical string an assign op introduces.
			opType := []string{"assign", "tag"}[rng.Intn(2)]
			ops[i].OpType = opType
			item := fmt.Sprintf("shared-%d", rng.Intn(3))
			b := make(map[string]any)
			if rng.Intn(2) == 0 {
				b["add"] = []string{item, interestingStrings[rng.Intn(len(interestingStrings))]}
			} else {
				b["remove"] = []string{item}
			}
			ops[i].Body, _ = json.Marshal(b)
		}
	}

	return ops, rules, ""
}

func generateSprocketStream(rng *rand.Rand) ([]codec.Op, []writ.Rule) {
	numOps := 4 + rng.Intn(8)
	ops := generateDAGSkeleton(rng, numOps, "s-property", "sprocket")
	rules := sprocketRules()

	ops[0].OpType = "create"
	ops[0].Body, _ = json.Marshal(map[string]any{
		"subject": map[string]any{
			"object_type": "widget",
			"object_id":   "w-1",
		},
		"text": "Initial sprocket text",
		"body": "Initial sprocket body",
		"anchor": map[string]any{
			"new": map[string]any{
				"commit": "1111111111111111111111111111111111111111",
				"path":   "main.go",
				"line":   10,
			},
		},
	})

	for i := 1; i < len(ops); i++ {
		switch rng.Intn(3) {
		case 0:
			// edit (exercising empty scalar write WRIT-125, and the
			// multi-value register beside the lww one: concurrent edits to
			// "body" must survive as a set until one causally succeeds them)
			ops[i].OpType = "edit"
			var txt string
			if rng.Intn(3) == 0 {
				txt = ""
			} else {
				txt = interestingStrings[rng.Intn(len(interestingStrings))]
			}
			ops[i].Body, _ = json.Marshal(map[string]any{
				"text": txt,
				"body": interestingStrings[rng.Intn(len(interestingStrings))],
			})
		case 1:
			// resolve (exercising whitespace-only actor WRIT-118)
			ops[i].OpType = "resolve"
			var actor string
			if rng.Intn(3) == 0 {
				actor = "   \t\n   "
			} else {
				actor = fmt.Sprintf("email:resolver%d@example.com", rng.Intn(3))
			}
			ops[i].Body, _ = json.Marshal(map[string]any{
				"resolved":    (rng.Intn(2) == 1),
				"resolved_by": actor,
			})
		case 2:
			// delete
			ops[i].OpType = "delete"
			ops[i].Body, _ = json.Marshal(map[string]any{"deleted": true})
		}
	}

	return ops, rules
}

func generateGizmoStream(rng *rand.Rand) ([]codec.Op, []writ.Rule) {
	numOps := 4 + rng.Intn(6)
	ops := generateDAGSkeleton(rng, numOps, "gz-property", "gizmo")
	rules := gizmoRules()

	ops[0].OpType = "create"
	ops[0].Body, _ = json.Marshal(map[string]any{
		"title":       "Gizmo v1",
		"description": "Gizmo Desc",
	})

	for i := 1; i < len(ops); i++ {
		switch rng.Intn(5) {
		case 0:
			ops[i].OpType = "update"
			ops[i].Body, _ = json.Marshal(map[string]any{
				"title": interestingStrings[rng.Intn(len(interestingStrings))],
			})
		case 1:
			ops[i].OpType = "set-status"
			ops[i].Body, _ = json.Marshal(map[string]any{
				"status": []string{"planned", "in-progress", "completed"}[rng.Intn(3)],
				"reason": interestingStrings[rng.Intn(len(interestingStrings))],
			})
		case 2:
			// advance: lattice values in and out of the declared order, so
			// the monotonic join is exercised against an unknown element too
			ops[i].OpType = "advance"
			ops[i].Body, _ = json.Marshal(map[string]any{
				"stage": []string{"planned", "active", "completed", "unknown-stage"}[rng.Intn(4)],
			})
		case 3:
			// watch: set-union over a person-ref, single item and slice
			ops[i].OpType = "watch"
			b := make(map[string]any)
			if rng.Intn(2) == 0 {
				b["watcher"] = fmt.Sprintf("email:watcher%d@example.com", rng.Intn(3))
			} else {
				b["watcher"] = []string{
					fmt.Sprintf("email:watcher%d@example.com", rng.Intn(3)),
					interestingStrings[rng.Intn(len(interestingStrings))],
				}
			}
			ops[i].Body, _ = json.Marshal(b)
		case 4:
			// The membership pair that does NOT collapse onto a shared
			// target: two op types writing the same field name.
			if rng.Intn(2) == 0 {
				ops[i].OpType = "add-item"
			} else {
				ops[i].OpType = "remove-item"
			}
			ops[i].Body, _ = json.Marshal(map[string]any{
				"item": fmt.Sprintf("item-%d", rng.Intn(4)),
			})
		}
	}

	return ops, rules
}

func generateThingStream(rng *rand.Rand) ([]codec.Op, []writ.Rule) {
	numOps := 4 + rng.Intn(6)
	ops := generateDAGSkeleton(rng, numOps, "th-property", "thing")
	rules := thingRules()

	ops[0].OpType = "create"
	ops[0].Body, _ = json.Marshal(map[string]any{
		"title":       "Thing 1",
		"description": "Thing Desc",
		"starts_at":   "2026-09-01T00:00:00Z",
		"ends_at":     "2026-09-14T00:00:00Z",
	})

	for i := 1; i < len(ops); i++ {
		switch rng.Intn(4) {
		case 0:
			ops[i].OpType = "update"
			ops[i].Body, _ = json.Marshal(map[string]any{
				"title":       interestingStrings[rng.Intn(len(interestingStrings))],
				"description": interestingStrings[rng.Intn(len(interestingStrings))],
			})
		case 1:
			// set-dates: the same two timestamp fields create writes, under
			// a second op type, so lww resolves across op types too
			ops[i].OpType = "set-dates"
			ops[i].Body, _ = json.Marshal(map[string]any{
				"starts_at": fmt.Sprintf("2026-09-%02dT00:00:00Z", 1+rng.Intn(9)),
				"ends_at":   fmt.Sprintf("2026-09-%02dT00:00:00Z", 10+rng.Intn(9)),
			})
		default:
			if rng.Intn(2) == 0 {
				ops[i].OpType = "add-item"
			} else {
				ops[i].OpType = "remove-item"
			}
			ops[i].Body, _ = json.Marshal(map[string]any{
				"item": fmt.Sprintf("item-%d", rng.Intn(4)),
			})
		}
	}

	return ops, rules
}

// --------------------------------------------------------------------------
// 5. Seed Corpus: Historical Regression Vectors
// --------------------------------------------------------------------------

type FuzzCase struct {
	ObjectType string      `json:"object_type,omitempty"`
	Rules      []writ.Rule `json:"rules,omitempty"`
	Ops        []codec.Op  `json:"ops"`
	Mode       string      `json:"mode,omitempty"`
}

// WRIT-112: keyed-lww approval subject denormalized / case-folded subject key.
func regressionVectorWRIT112() FuzzCase {
	now := time.Unix(100, 0).UTC()
	ops := []codec.Op{
		{
			ID: "op-alice-app",
			Envelope: codec.Envelope{
				ObjectID:   "w-112",
				ObjectType: "widget",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","subject":"Alice@Example.COM","verdict":"approve","message":"LGTM"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		},
		{
			ID:      "op-alice-update",
			Parents: []string{"op-alice-app"},
			Envelope: codec.Envelope{
				ObjectID:   "w-112",
				ObjectType: "widget",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","subject":"  alice@example.com  ","verdict":"approve","message":"Updated msg"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(60 * time.Second)},
		},
	}
	return FuzzCase{
		ObjectType: "widget",
		Rules:      widgetRules(),
		Ops:        ops,
	}
}

// WRIT-116: set-observed-remove and set-union handling of empty and whitespace-only items.
func regressionVectorWRIT116() FuzzCase {
	now := time.Unix(100, 0).UTC()
	ops := []codec.Op{
		{
			ID: "op-tag-add",
			Envelope: codec.Envelope{
				ObjectID:   "g-116",
				ObjectType: "gadget",
				OpType:     "tag",
				OpVersion:  1,
				Body:       json.RawMessage(`{"add":["","   ","\t\n","bug","feature"]}`),
			},
			Author: codec.Identity{When: now},
		},
		{
			ID:      "op-tag-rem",
			Parents: []string{"op-tag-add"},
			Envelope: codec.Envelope{
				ObjectID:   "g-116",
				ObjectType: "gadget",
				OpType:     "tag",
				OpVersion:  1,
				Body:       json.RawMessage(`{"remove":["","   ","feature"]}`),
			},
			Author: codec.Identity{When: now.Add(60 * time.Second)},
		},
	}
	return FuzzCase{
		ObjectType: "gadget",
		Rules:      gadgetRules(),
		Ops:        ops,
		Mode:       "tag",
	}
}

// WRIT-118: lww whitespace-only resolve actor normalized to "".
func regressionVectorWRIT118() FuzzCase {
	now := time.Unix(100, 0).UTC()
	ops := []codec.Op{
		{
			ID: "op-sprocket-create",
			Envelope: codec.Envelope{
				ObjectID:   "s-118",
				ObjectType: "sprocket",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"subject":{"object_type":"widget","object_id":"w-1"},"text":"Initial text"}`),
			},
			Author: codec.Identity{When: now},
		},
		{
			ID:      "op-sprocket-resolve",
			Parents: []string{"op-sprocket-create"},
			Envelope: codec.Envelope{
				ObjectID:   "s-118",
				ObjectType: "sprocket",
				OpType:     "resolve",
				OpVersion:  1,
				Body:       json.RawMessage(`{"resolved":true,"resolved_by":"   \t\n   "}`),
			},
			Author: codec.Identity{When: now.Add(60 * time.Second)},
		},
	}
	return FuzzCase{
		ObjectType: "sprocket",
		Rules:      sprocketRules(),
		Ops:        ops,
	}
}

// WRIT-124: keyed-lww non-string key components quarantined into UnknownOps without leaking Go formatting verbs.
func regressionVectorWRIT124() FuzzCase {
	now := time.Unix(100, 0).UTC()
	ops := []codec.Op{
		{
			ID: "op-bad-key",
			Envelope: codec.Envelope{
				ObjectID:   "w-124",
				ObjectType: "widget",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","subject":12345,"verdict":"approve"}`),
			},
			Author: codec.Identity{When: now},
		},
	}
	return FuzzCase{
		ObjectType: "widget",
		Rules:      widgetRules(),
		Ops:        ops,
	}
}

// WRIT-125: omitempty empty-scalar retention in generic fold maps and omission in typed JSON.
func regressionVectorWRIT125() FuzzCase {
	now := time.Unix(100, 0).UTC()
	ops := []codec.Op{
		{
			ID: "op-c-create",
			Envelope: codec.Envelope{
				ObjectID:   "s-125",
				ObjectType: "sprocket",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"subject":{"object_type":"widget","object_id":"w-1"},"text":"Initial text"}`),
			},
			Author: codec.Identity{When: now},
		},
		{
			ID:      "op-c-edit-empty",
			Parents: []string{"op-c-create"},
			Envelope: codec.Envelope{
				ObjectID:   "s-125",
				ObjectType: "sprocket",
				OpType:     "edit",
				OpVersion:  1,
				Body:       json.RawMessage(`{"text":""}`),
			},
			Author: codec.Identity{When: now.Add(60 * time.Second)},
		},
	}
	return FuzzCase{
		ObjectType: "sprocket",
		Rules:      sprocketRules(),
		Ops:        ops,
	}
}

// WRIT-126: Non-string and null elements in set-union and append triggering op-level rejection into UnknownOps, plus empty array append producing [].
func regressionVectorWRIT126() FuzzCase {
	now := time.Unix(100, 0).UTC()
	rules := []writ.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww"},
		{OpType: "add-remote", OpVersion: 1, Field: "remote", Strategy: "set-union"},
		{OpType: "append-entries", OpVersion: 1, Field: "entries", Strategy: "append"},
	}
	ops := []codec.Op{
		{
			ID: "op-create",
			Envelope: codec.Envelope{
				ObjectID:   "obj-126",
				ObjectType: "synthetic-126",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"writ-126"}`),
			},
			Author: codec.Identity{When: now},
		},
		{
			ID:      "op-remote-bad-num",
			Parents: []string{"op-create"},
			Envelope: codec.Envelope{
				ObjectID:   "obj-126",
				ObjectType: "synthetic-126",
				OpType:     "add-remote",
				OpVersion:  1,
				Body:       json.RawMessage(`{"remote":[12345]}`),
			},
			Author: codec.Identity{When: now.Add(10 * time.Second)},
		},
		{
			ID:      "op-remote-bad-null",
			Parents: []string{"op-remote-bad-num"},
			Envelope: codec.Envelope{
				ObjectID:   "obj-126",
				ObjectType: "synthetic-126",
				OpType:     "add-remote",
				OpVersion:  1,
				Body:       json.RawMessage(`{"remote":null}`),
			},
			Author: codec.Identity{When: now.Add(20 * time.Second)},
		},
		{
			ID:      "op-remote-good",
			Parents: []string{"op-remote-bad-null"},
			Envelope: codec.Envelope{
				ObjectID:   "obj-126",
				ObjectType: "synthetic-126",
				OpType:     "add-remote",
				OpVersion:  1,
				Body:       json.RawMessage(`{"remote":["origin"]}`),
			},
			Author: codec.Identity{When: now.Add(30 * time.Second)},
		},
		{
			ID:      "op-append-bad-null",
			Parents: []string{"op-remote-good"},
			Envelope: codec.Envelope{
				ObjectID:   "obj-126",
				ObjectType: "synthetic-126",
				OpType:     "append-entries",
				OpVersion:  1,
				Body:       json.RawMessage(`{"entries":null}`),
			},
			Author: codec.Identity{When: now.Add(40 * time.Second)},
		},
		{
			ID:      "op-append-bad-null-elem",
			Parents: []string{"op-append-bad-null"},
			Envelope: codec.Envelope{
				ObjectID:   "obj-126",
				ObjectType: "synthetic-126",
				OpType:     "append-entries",
				OpVersion:  1,
				Body:       json.RawMessage(`{"entries":["second",null]}`),
			},
			Author: codec.Identity{When: now.Add(50 * time.Second)},
		},
		{
			ID:      "op-append-empty",
			Parents: []string{"op-append-bad-null-elem"},
			Envelope: codec.Envelope{
				ObjectID:   "obj-126",
				ObjectType: "synthetic-126",
				OpType:     "append-entries",
				OpVersion:  1,
				Body:       json.RawMessage(`{"entries":[]}`),
			},
			Author: codec.Identity{When: now.Add(60 * time.Second)},
		},
	}
	return FuzzCase{
		Rules: rules,
		Ops:   ops,
	}
}

// WRIT-196: a rule with Strategy == "" and a rule with an unknown strategy
// string must be dropped by filterValidRules before the fold ever sees
// them -- fuzz-mutated JSON decodes a missing/garbled "strategy" key into
// exactly these shapes, and undropped they reach NewAccumulator
// (engine/internal/fold/strategy.go) as `unknown strategy ""`.
func regressionVectorWRIT196() FuzzCase {
	now := time.Unix(100, 0).UTC()
	rules := []writ.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww"},
		{OpType: "create", OpVersion: 1, Field: "empty_strategy", Strategy: ""},
		{OpType: "create", OpVersion: 1, Field: "unknown_strategy", Strategy: "not-a-real-strategy"},
	}
	ops := []codec.Op{
		{
			ID: "op-create",
			Envelope: codec.Envelope{
				ObjectID:   "obj-196",
				ObjectType: "synthetic-196",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"writ-196","empty_strategy":"x","unknown_strategy":"y"}`),
			},
			Author: codec.Identity{When: now},
		},
	}
	return FuzzCase{
		Rules: rules,
		Ops:   ops,
	}
}

// WRIT-197: an op body whose raw bytes are not valid UTF-8 must be rejected
// by isValidOpSet before the fold ever sees it. This is the first of three
// sibling vectors (this one, regressionVectorWRIT197LoneSurrogate, and
// regressionVectorWRIT197DuplicateKey) pinning the three input classes
// canonicaljson.Marshal deliberately rejects rather than silently
// normalizing (see its doc comment) -- isValidOpSet's guard now runs that
// same function, and each vector isolates exactly one class so a guard
// regression narrow enough to miss just one of the three still fails its
// own dedicated vector. codec.Op here is built directly from fuzzer-mutated
// JSON (json.Unmarshal(data, &fc)), never through codec.DecodePayload --
// the real ingestion boundary every op crosses, which refuses exactly this
// payload via the byte-equality rule (canonicaljson.Marshal,
// engine/codec/decode.go). Unlike a plain string field, Body is a
// json.RawMessage: unmarshaling into it round-trips raw bytes verbatim with
// no UTF-8 sanitization, so fuzz-mutated JSON can smuggle an invalid byte
// through Go's (UTF-8-tolerant) JSON syntax scanner straight into Fold.
// There a create-once field's byte-exact raw preservation
// (engine/internal/fold/strategy.go) carries the invalid bytes into State
// unchanged, and canonicalizing that State for the writ.Fold/spec.Fold
// byte-equality comparison (toCanonicalJSON) fails with "canonicaljson:
// input is not valid UTF-8" -- a refusal DecodePayload would have produced
// too, just earlier, on the op itself rather than on the folded state.
func regressionVectorWRIT197() FuzzCase {
	now := time.Unix(100, 0).UTC()
	rules := []writ.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "create-once"},
	}
	// 0xff is not a valid UTF-8 byte on its own. It survives Go's JSON
	// syntax scanner (which does not enforce strict UTF-8) sitting raw
	// inside a string literal, and survives unmarshaling into the
	// json.RawMessage Body field verbatim -- unlike a plain string field,
	// which json.Unmarshal would already have sanitized to U+FFFD.
	body := append(append([]byte(`{"title":"x`), 0xff), []byte(`y"}`)...)
	ops := []codec.Op{
		{
			ID: "op-create",
			Envelope: codec.Envelope{
				ObjectID:   "obj-197",
				ObjectType: "synthetic-197",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(body),
			},
			Author: codec.Identity{When: now},
		},
	}
	return FuzzCase{
		Rules: rules,
		Ops:   ops,
	}
}

// WRIT-197 (round 2): second of the three sibling vectors -- see
// regressionVectorWRIT197's comment for the shared rationale. A body
// carrying a lone (unpaired) UTF-16 surrogate escape, e.g. \ud800 with no
// following low surrogate, passes both json.Unmarshal (encoding/json
// decodes an unpaired surrogate escape to U+FFFD rather than erroring) and
// utf8.Valid (the raw escape text "\ud800" is itself plain ASCII, so the
// lone surrogate is invisible at the byte level -- it only exists once the
// escape is interpreted). isValidOpSet must therefore run
// canonicaljson.Marshal itself, the same as DecodePayload does, rather than
// re-deriving a narrower check: canonicaljson.Marshal detects the lone
// surrogate by scanning the raw \u escape text directly (checkSurrogateEscapes
// in engine/codec/canonicaljson/canonicaljson.go), which is exactly
// what the byte-equality rule (Rule 2, engine/codec/decode.go) relies on to
// refuse this payload during real ingestion. A create-once field's
// byte-exact raw preservation then carries the escape into State unchanged,
// and toCanonicalJSON fails with "canonicaljson: lone surrogate \ud800 in
// string" -- the same refusal DecodePayload would have produced earlier, on
// the op itself.
func regressionVectorWRIT197LoneSurrogate() FuzzCase {
	now := time.Unix(100, 0).UTC()
	rules := []writ.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "create-once"},
	}
	ops := []codec.Op{
		{
			ID: "op-create",
			Envelope: codec.Envelope{
				ObjectID:   "obj-197-surrogate",
				ObjectType: "synthetic-197-surrogate",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"a\ud800b"}`),
			},
			Author: codec.Identity{When: now},
		},
	}
	return FuzzCase{
		Rules: rules,
		Ops:   ops,
	}
}

// WRIT-197 (round 2): third of the three sibling vectors -- see
// regressionVectorWRIT197's comment for the shared rationale. A body whose
// raw text has a duplicate object key survives json.Unmarshal (encoding/json
// keeps the last occurrence, silently) whenever the duplicate sits inside a
// non-scalar create-once field's value: unmarshaling that value into
// map[string]any collapses the duplicate away, but unmarshaling into the
// rawBody map[string]json.RawMessage (engine/internal/fold/fold.go) captures
// the field's raw text verbatim, duplicate key and all, because Go decodes
// a json.RawMessage by byte range, not by re-serializing the parsed value.
// isValidOpSet must therefore run canonicaljson.Marshal itself: it rejects a
// duplicate object key by construction (decodeValue walks tokens rather than
// decoding into a map, so it catches a repeated key the moment it appears),
// exactly matching what the byte-equality rule (Rule 2,
// engine/codec/decode.go) relies on to refuse this payload during real
// ingestion. The create-once field's byte-exact raw preservation then
// carries the duplicate-bearing object into State unchanged, and
// toCanonicalJSON fails with `canonicaljson: duplicate object key "a"` --
// the same refusal DecodePayload would have produced earlier, on the op
// itself.
func regressionVectorWRIT197DuplicateKey() FuzzCase {
	now := time.Unix(100, 0).UTC()
	rules := []writ.Rule{
		{OpType: "create", OpVersion: 1, Field: "subject", Strategy: "create-once"},
	}
	ops := []codec.Op{
		{
			ID: "op-create",
			Envelope: codec.Envelope{
				ObjectID:   "obj-197-dupkey",
				ObjectType: "synthetic-197-dupkey",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"subject":{"a":1,"a":2}}`),
			},
			Author: codec.Identity{When: now},
		},
	}
	return FuzzCase{
		Rules: rules,
		Ops:   ops,
	}
}

// --------------------------------------------------------------------------
// 6. Test Suite & Property Tests
// --------------------------------------------------------------------------

func TestProperty_FoldThreeWay(t *testing.T) {
	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))
	t.Logf("property test random seed: %d", seed)

	// Subtests for the 7 historical regression vectors
	t.Run("Regression_WRIT_112_ApprovalSubjectDenormalized", func(t *testing.T) {
		c := regressionVectorWRIT112()
		assertThreeWayFoldAbstract(t, c.Ops, c.Rules)
	})

	t.Run("Regression_WRIT_116_EmptySetItems", func(t *testing.T) {
		c := regressionVectorWRIT116()
		assertThreeWayFoldAbstract(t, c.Ops, c.Rules)
	})

	t.Run("Regression_WRIT_118_WhitespaceResolveActor", func(t *testing.T) {
		c := regressionVectorWRIT118()
		assertThreeWayFoldAbstract(t, c.Ops, c.Rules)
	})

	t.Run("Regression_WRIT_124_KeyedLWWNonStringKeys", func(t *testing.T) {
		c := regressionVectorWRIT124()
		assertThreeWayFoldAbstract(t, c.Ops, c.Rules)
	})

	t.Run("Regression_WRIT_125_OmitemptyEmptyScalars", func(t *testing.T) {
		c := regressionVectorWRIT125()
		assertThreeWayFoldAbstract(t, c.Ops, c.Rules)
	})

	t.Run("Regression_WRIT_126_NonStringNullSetUnionAppend", func(t *testing.T) {
		c := regressionVectorWRIT126()
		if c.ObjectType != "" {
			assertThreeWayFoldAbstract(t, c.Ops, c.Rules)
		} else {
			assertThreeWayFoldAbstract(t, c.Ops, c.Rules)
		}
	})

	t.Run("Regression_WRIT_196_EmptyAndUnknownStrategyFiltered", func(t *testing.T) {
		c := regressionVectorWRIT196()
		filtered := filterValidRules(c.Rules)
		if len(filtered) != len(c.Rules)-2 {
			t.Fatalf("filterValidRules: got %d rules, want %d (rules with Strategy \"\" and an unknown strategy must be dropped): %+v", len(filtered), len(c.Rules)-2, filtered)
		}
		for _, r := range filtered {
			if !spec.KnownCatalogueStrategies[r.Strategy] {
				t.Fatalf("filterValidRules let an invalid strategy through: %+v", r)
			}
		}
		assertThreeWayFoldAbstract(t, c.Ops, filtered)
	})

	t.Run("Regression_WRIT_197_NonUTF8BodyRejectedByHarness", func(t *testing.T) {
		c := regressionVectorWRIT197()
		if !isValidOpSet(c.Ops) {
			// Correctly filtered before Fold ever sees it -- the fix. A real
			// op could never reach this point with this Body:
			// codec.DecodePayload refuses the same bytes outright.
			return
		}
		// Guard regressed: prove this reproduces the original failure
		// (canonicaljson: input is not valid UTF-8 out of toCanonicalJSON),
		// not just a symptom of the missing guard.
		assertThreeWayFoldAbstract(t, c.Ops, c.Rules)
	})

	t.Run("Regression_WRIT_197_LoneSurrogateBodyRejectedByHarness", func(t *testing.T) {
		c := regressionVectorWRIT197LoneSurrogate()
		if !isValidOpSet(c.Ops) {
			// Correctly filtered before Fold ever sees it. A real op could
			// never reach this point with this Body: codec.DecodePayload
			// refuses the same lone surrogate outright.
			return
		}
		// Guard regressed: prove this reproduces the original failure
		// (canonicaljson: lone surrogate \ud800 in string, out of
		// toCanonicalJSON), not just a symptom of the missing guard.
		assertThreeWayFoldAbstract(t, c.Ops, c.Rules)
	})

	t.Run("Regression_WRIT_197_DuplicateKeyBodyRejectedByHarness", func(t *testing.T) {
		c := regressionVectorWRIT197DuplicateKey()
		if !isValidOpSet(c.Ops) {
			// Correctly filtered before Fold ever sees it. A real op could
			// never reach this point with this Body: codec.DecodePayload
			// refuses the same duplicate key outright.
			return
		}
		// Guard regressed: prove this reproduces the original failure
		// (canonicaljson: duplicate object key "a", out of toCanonicalJSON),
		// not just a symptom of the missing guard.
		assertThreeWayFoldAbstract(t, c.Ops, c.Rules)
	})

	t.Run("Widget_GaugeOnlyRevision", func(t *testing.T) {
		now := time.Unix(100, 0).UTC()
		ops := []codec.Op{
			{
				ID: "op-widget-create",
				Envelope: codec.Envelope{
					ObjectID:   "w-gauge-rev",
					ObjectType: "widget",
					OpType:     "create",
					OpVersion:  1,
					Body:       json.RawMessage(`{"title":"Widget with a gauge"}`),
				},
				Author: codec.Identity{When: now},
			},
			{
				ID:      "op-app",
				Parents: []string{"op-widget-create"},
				Envelope: codec.Envelope{
					ObjectID:   "w-gauge-rev",
					ObjectType: "widget",
					OpType:     "approval",
					OpVersion:  1,
					Body:       json.RawMessage(`{"revision":"head-1","subject":"alice@example.com","verdict":"approve"}`),
				},
				Author: codec.Identity{When: now.Add(10 * time.Second)},
			},
			{
				ID:      "op-gauge-only-rev",
				Parents: []string{"op-app"},
				Envelope: codec.Envelope{
					ObjectID:   "w-gauge-rev",
					ObjectType: "widget",
					OpType:     "gauge",
					OpVersion:  1,
					Body:       json.RawMessage(`{"revision":"head-1"}`),
				},
				Author: codec.Identity{When: now.Add(20 * time.Second)},
			},
		}
		assertThreeWayFoldAbstract(t, ops, widgetRules())
	})

	t.Run("Gadget_ExplicitEmptyState", func(t *testing.T) {
		now := time.Unix(100, 0).UTC()
		ops := []codec.Op{
			{
				ID: "op-gadget-create",
				Envelope: codec.Envelope{
					ObjectID:   "g-empty-state",
					ObjectType: "gadget",
					OpType:     "create",
					OpVersion:  1,
					Body:       json.RawMessage(`{"title":"Gadget title"}`),
				},
				Author: codec.Identity{When: now},
			},
			{
				ID:      "op-gadget-set-empty",
				Parents: []string{"op-gadget-create"},
				Envelope: codec.Envelope{
					ObjectID:   "g-empty-state",
					ObjectType: "gadget",
					OpType:     "set-state",
					OpVersion:  1,
					Body:       json.RawMessage(`{"state":""}`),
				},
				Author: codec.Identity{When: now.Add(10 * time.Second)},
			},
		}
		assertThreeWayFoldAbstract(t, ops, gadgetRules())
	})

	// 100 randomized property iterations over abstract strategies and domain object streams
	const iterations = 100
	for i := 0; i < iterations; i++ {
		// 1. Abstract synthetic strategies
		synthOps, synthRules := generateAbstractSyntheticStream(rng)
		assertThreeWayFoldAbstract(t, synthOps, synthRules)

		// 2. Domain object stream
		domainIdx := rng.Intn(5)
		switch domainIdx {
		case 0:
			ops, rules, _ := generateWidgetStream(rng)
			assertThreeWayFoldAbstract(t, ops, rules)
		case 1:
			ops, rules, _ := generateGadgetStream(rng)
			assertThreeWayFoldAbstract(t, ops, rules)
		case 2:
			ops, rules := generateSprocketStream(rng)
			assertThreeWayFoldAbstract(t, ops, rules)
		case 3:
			ops, rules := generateGizmoStream(rng)
			assertThreeWayFoldAbstract(t, ops, rules)
		case 4:
			ops, rules := generateThingStream(rng)
			assertThreeWayFoldAbstract(t, ops, rules)
		}
	}
}

// --------------------------------------------------------------------------
// 7. Go Native Fuzz Target
// --------------------------------------------------------------------------

func FuzzFoldThreeWay(f *testing.F) {
	// Seed with the 10 regression vectors
	seedVectors := []FuzzCase{
		regressionVectorWRIT112(),
		regressionVectorWRIT116(),
		regressionVectorWRIT118(),
		regressionVectorWRIT124(),
		regressionVectorWRIT125(),
		regressionVectorWRIT126(),
		regressionVectorWRIT196(),
		regressionVectorWRIT197(),
		regressionVectorWRIT197LoneSurrogate(),
		regressionVectorWRIT197DuplicateKey(),
	}
	for _, vec := range seedVectors {
		if data, err := json.Marshal(vec); err == nil {
			f.Add(data)
		}
	}

	// Seed with generated cases for each domain
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 5; i++ {
		var fc FuzzCase
		switch i {
		case 0:
			ops, rules, mode := generateWidgetStream(rng)
			fc = FuzzCase{ObjectType: "widget", Rules: rules, Ops: ops, Mode: mode}
		case 1:
			ops, rules, mode := generateGadgetStream(rng)
			fc = FuzzCase{ObjectType: "gadget", Rules: rules, Ops: ops, Mode: mode}
		case 2:
			ops, rules := generateSprocketStream(rng)
			fc = FuzzCase{ObjectType: "sprocket", Rules: rules, Ops: ops}
		case 3:
			ops, rules := generateGizmoStream(rng)
			fc = FuzzCase{ObjectType: "gizmo", Rules: rules, Ops: ops}
		case 4:
			ops, rules := generateThingStream(rng)
			fc = FuzzCase{ObjectType: "thing", Rules: rules, Ops: ops}
		}
		if data, err := json.Marshal(fc); err == nil {
			f.Add(data)
		}
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var fc FuzzCase
		if err := json.Unmarshal(data, &fc); err == nil && len(fc.Ops) > 0 {
			if !isValidOpSet(fc.Ops) {
				return
			}
			if fc.ObjectType != "" && fc.Ops[0].ObjectType != fc.ObjectType {
				return
			}
			if fc.ObjectType != "" {
				var rules []writ.Rule
				switch fc.ObjectType {
				case "widget":
					rules = widgetRules()
				case "gadget":
					rules = gadgetRules()
				case "sprocket":
					rules = sprocketRules()
				case "gizmo":
					rules = gizmoRules()
				case "thing":
					rules = thingRules()
				default:
					return
				}
				assertThreeWayFoldAbstract(t, fc.Ops, rules)
			} else {
				if len(fc.Rules) == 0 {
					return
				}
				fc.Rules = filterValidRules(fc.Rules)
				if len(fc.Rules) == 0 {
					return
				}
				assertThreeWayFoldAbstract(t, fc.Ops, fc.Rules)
			}
			return
		}

		// If data is not valid FuzzCase JSON, use entropy to seed property generator
		if len(data) >= 8 {
			seed := int64(binary.BigEndian.Uint64(data[:8]))
			localRNG := rand.New(rand.NewSource(seed))
			choice := localRNG.Intn(6)
			if choice == 0 {
				synthOps, synthRules := generateAbstractSyntheticStream(localRNG)
				assertThreeWayFoldAbstract(t, synthOps, synthRules)
			} else {
				switch choice {
				case 1:
					ops, rules, _ := generateWidgetStream(localRNG)
					assertThreeWayFoldAbstract(t, ops, rules)
				case 2:
					ops, rules, _ := generateGadgetStream(localRNG)
					assertThreeWayFoldAbstract(t, ops, rules)
				case 3:
					ops, rules := generateSprocketStream(localRNG)
					assertThreeWayFoldAbstract(t, ops, rules)
				case 4:
					ops, rules := generateGizmoStream(localRNG)
					assertThreeWayFoldAbstract(t, ops, rules)
				case 5:
					ops, rules := generateThingStream(localRNG)
					assertThreeWayFoldAbstract(t, ops, rules)
				}
			}
		}
	})
}
