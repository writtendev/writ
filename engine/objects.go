package writ

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/internal/fold"
	"github.com/writtendev/writ/engine/state"
)

// NewOp specifies one operation to append against a schema-declared object
// type: an op type, an optional explicit op version, and a body.
//
// Fields is keyed by declared FIELD name — the same key a define-field
// rule's `field` names, and the key an op's body carries on the wire. This
// is not always the key Object.Fields reports the same data back under:
// when a rule declares a `target`, every field sharing that target folds
// into one target-keyed entry. `assign.add` and `assign.remove` both write
// body field `add`/`remove` here but both read back under `assignees`
// (spec/schema-ops.md, WRIT-198) — the single easiest thing for a caller of
// this API to get wrong.
//
// Version 0 asks Create/Apply to resolve the op's version from the
// installed vocabulary (Store.Types): unambiguous only when the object type
// declares exactly one version of that op type. An object type declaring
// more than one version of the same op type requires an explicit Version.
type NewOp struct {
	Type    string
	Version int64
	Fields  map[string]any
}

// Object is the caller-visible, schema-shaped folded state of one
// collaborative object (ARCHITECTURE.md §Public API shape). It deliberately
// omits ObjectState.TotalOrder and every OpRef.TStar: a caller who supplied
// only an object id never asked for the object's total order or its
// causality-monotone timestamps, and TotalOrder is exactly the shape
// ARCHITECTURE.md's "no SHAs, no refspecs" rule exists to keep off this
// type.
//
// That rule has one deliberate, narrower exception: UnknownOps' entries
// (state.UnknownOp) carry a Commit field, which is the op commit's SHA — the
// only way to name an op the installed vocabulary cannot interpret, for a
// caller who needs some identifier to report or investigate it by. This is
// forward-compatibility provenance, not the general-purpose op history
// TotalOrder would be, and it is not new here: Store.Schema's UnknownOps
// carries the same field today.
//
// Fields is keyed by TARGET KEY (state.Rule.TargetKey(): a rule's declared
// target when it has one, otherwise its field name) — not always the field
// name a NewOp used to write it; see NewOp.Fields. UnknownOps lists every op
// this object carries that no installed rule interprets: forward
// compatibility, not an error. An object type with no installed rules at
// all folds to an empty Fields and every op in UnknownOps, which is the
// correct answer for a type this repository's schema does not declare.
type Object struct {
	ObjectID   string
	ObjectType string
	Fields     map[string]any
	UnknownOps []UnknownOp
	// Verification is the worst signature-verification outcome (codec's
	// valid < wrong-key < unsigned < corrupted-signature < payload-mutated
	// ordering) among every op that folded into this object, known and
	// unknown alike. It is an envelope-level fact, like an op's author or
	// timestamp, and never affects Fields: every op folds regardless of
	// its outcome (AGENTS.md "Fold is pure and deterministic"; WRIT-251
	// ruling 1). A caller that presents Fields as authentic content must
	// check this field itself and decide what to do — see the package
	// doc's trust model.
	Verification string
}

// Objects provides generic create, apply, and get operations over
// collaborative objects of any schema-declared type — the schema-shaped
// replacement for the per-type typed services writ once shipped: AGENTS.md
// "the shapes callers see come from the schema in the log, not from Go
// structs writ ships."
type Objects struct {
	store *Store
}

// Create appends the op that starts a new object of objectType, minting and
// returning its object ID. objectType and op.Type must both be declared by
// the vocabulary in effect (Store.Types) — a log-declared type may name its
// creating op anything; nothing here assumes "create" (spec/op-envelope.md
// §op_type gives it only as an example).
//
// Create runs no body validation of its own beyond what dagStore.Append
// already performs: producer validation resolves the governing vocabulary
// from the log (WRIT-188) and refuses a body the installed schema rejects,
// or an object_type nothing declares, before this ever reaches the DAG. A
// second, hand-written copy of that check here would only drift from it.
func (o *Objects) Create(ctx context.Context, objectType string, op NewOp) (string, error) {
	if o == nil || o.store == nil {
		return "", fmt.Errorf("writ: store is nil")
	}
	if err := o.store.ensureWritable(); err != nil {
		return "", err
	}
	if objectType == "" {
		return "", fmt.Errorf("writ: object type cannot be empty")
	}
	if op.Type == "" {
		return "", fmt.Errorf("writ: op type cannot be empty")
	}

	version := op.Version
	if version == 0 {
		types, err := o.store.Types(ctx)
		if err != nil {
			return "", fmt.Errorf("writ: create object: resolve types: %w", err)
		}
		v, err := resolveOpVersion(types, objectType, op.Type)
		if err != nil {
			return "", fmt.Errorf("writ: create object: %w", err)
		}
		version = v
	}

	bodyBytes, err := json.Marshal(nonNilFields(op.Fields))
	if err != nil {
		return "", fmt.Errorf("writ: marshal op fields: %w", err)
	}

	id := newObjectID()
	env := codec.Envelope{
		ObjectID:   id,
		ObjectType: objectType,
		OpType:     op.Type,
		OpVersion:  version,
		Body:       bodyBytes,
	}

	if _, err := o.store.dagStore.Append(ctx, env, nil); err != nil {
		return "", fmt.Errorf("writ: create object: %w", err)
	}

	_ = o.store.maybeAutoRefresh(ctx)
	return id, nil
}

// Apply appends a further op against an existing object, causally following
// its current frontier.
func (o *Objects) Apply(ctx context.Context, objectID string, op NewOp) error {
	if o == nil || o.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if err := o.store.ensureWritable(); err != nil {
		return err
	}
	if objectID == "" {
		return fmt.Errorf("writ: object id cannot be empty")
	}
	if op.Type == "" {
		return fmt.Errorf("writ: op type cannot be empty")
	}

	if err := o.store.maybeAutoRefresh(ctx); err != nil {
		return fmt.Errorf("writ: auto refresh: %w", err)
	}

	existing, err := o.store.projection.Object(objectID)
	if err != nil {
		return err
	}

	version := op.Version
	if version == 0 {
		types, err := o.store.Types(ctx)
		if err != nil {
			return fmt.Errorf("writ: apply object: resolve types: %w", err)
		}
		v, err := resolveOpVersion(types, existing.ObjectType, op.Type)
		if err != nil {
			return fmt.Errorf("writ: apply object: %w", err)
		}
		version = v
	}

	frontier, err := o.store.projection.Frontier(objectID)
	if err != nil {
		return fmt.Errorf("writ: get frontier: %w", err)
	}

	bodyBytes, err := json.Marshal(nonNilFields(op.Fields))
	if err != nil {
		return fmt.Errorf("writ: marshal op fields: %w", err)
	}

	env := codec.Envelope{
		ObjectID:   objectID,
		ObjectType: existing.ObjectType,
		OpType:     op.Type,
		OpVersion:  version,
		Body:       bodyBytes,
	}

	if _, err := o.store.dagStore.Append(ctx, env, frontier); err != nil {
		return fmt.Errorf("writ: apply object: %w", err)
	}

	_ = o.store.maybeAutoRefresh(ctx)
	return nil
}

// Get folds objectID's state directly from the DAG and returns it.
//
// Get folds every op the object carries regardless of its signature
// verification outcome, and reports the worst of them on
// Object.Verification rather than filtering on it — see the package
// doc's trust model.
//
// This reads from the DAG, never the projection: Store.Schema already sets
// this precedent, for the same reason — the projection is a droppable cache
// (ARCHITECTURE.md §The six machines #5), and nothing here may require it
// to have been built or refreshed. Get returns identical Fields before and
// after Store.Rebuild, and before and after the projection cache file is
// deleted outright.
//
// The cost is one full dagStore.Enumerate() per call — every op for every
// object in the repository, decoded — which is genuinely more expensive
// than a SQLite point lookup. Signature verification is already scoped to
// this object, not the whole enumeration (dag.VerifyOnly, WRIT-251): real
// ed25519 verification runs only for objectID's own ops. What is not
// scoped is decoding: every other object's ops still go through
// codec.FromGitCommit + codec.DecodeCommit in full — the op.json size
// cap, canonical-payload byte-equality check and envelope JSON-Schema
// validation — on every single Get.
//
// WRIT-249 measured whether that remaining decode cost is worth a second,
// cheaper pre-decode filter — peeking each commit's op.json for object_id
// before running it through the real decoder, and skipping the ones that
// don't match. BenchmarkObjectsGet's before/after numbers
// (this ticket's PR body has the full data) showed roughly a 20-30%
// reduction at 100-1,000 objects, shrinking to something in the
// 10-20% range — inconsistent between runs, sometimes lower — at 5,000,
// the largest, most realistic scale tested: a paired, back-to-back
// comparison at 5,000 objects (same benchmark, same process, immediately
// sequential to cancel out this environment's own noise) measured 2.35s
// before and 2.05s after, ~13%. The win shrinks rather than compounds as
// the repository grows because the ancestry walk itself — one
// object.GetCommit per commit just to read parent hashes, unavoidable
// without an index (ARCHITECTURE.md's chain layout: chains are per
// writer×type, object_id rides the payload, and one object's ops can sit
// in several types' chains) — and the underlying git object-store I/O
// dominate at that scale, not the JSON validation a pre-decode filter
// would skip. That is below the plan's ~30%-at-1,000+-objects bar for
// adding a second enumeration path, so this ticket stops here: the
// benchmark lands, permanently, as the regression net for whoever revisits
// this with a real index instead of a peek.
//
// Serving Get from the generated projection tables instead would mean
// inverting writeTypeRow (engine/projection/materialize.go) back into
// state.Fold's exact output shape across scalar columns, child tables,
// keyed-lww groups, per-target append tables, __members and
// unknown_fields, and a subtly wrong inversion is a silent correctness
// bug, not a visible failure. That optimisation belongs in its own
// ticket, with evidence behind it — not this one.
//
// Get never mutates the DAG and never modifies state.Fold's own behavior:
// it does I/O to fetch ops and then calls the pure fold, exactly as the
// projection's own materializer does.
func (o *Objects) Get(ctx context.Context, objectID string) (Object, error) {
	if o == nil || o.store == nil {
		return Object{}, fmt.Errorf("writ: store is nil")
	}
	if objectID == "" {
		return Object{}, fmt.Errorf("writ: object id cannot be empty")
	}

	// ts is read fresh from disk on every call, never the one Open froze.
	ts, _ := o.store.currentTrustStore()
	enumRes, err := o.store.dagStore.Enumerate(
		dag.VerifyOnly(func(op codec.Op) bool { return op.ObjectID == objectID }),
		dag.WithLiveTrustStore(ts),
	)
	if err != nil {
		return Object{}, fmt.Errorf("writ: get object: enumerate: %w", err)
	}

	ops := enumRes.Ops[objectID]
	if len(ops) == 0 {
		return Object{}, ErrNotFound
	}

	objectType := fold.DetermineObjectType(ops)

	rules, err := o.store.rules(ctx)
	if err != nil {
		return Object{}, fmt.Errorf("writ: get object: resolve rules: %w", err)
	}

	st, err := state.Fold(ops, rules[objectType])
	if err != nil {
		return Object{}, fmt.Errorf("writ: get object: fold: %w", err)
	}

	fields := st.State
	if fields == nil {
		fields = map[string]any{}
	}

	outcomes := make([]codec.VerificationOutcome, len(ops))
	for i, op := range ops {
		outcomes[i] = op.Verification.Outcome
	}

	return Object{
		ObjectID:     objectID,
		ObjectType:   objectType,
		Fields:       fields,
		UnknownOps:   st.UnknownOps,
		Verification: string(codec.WorstOutcome(outcomes...)),
	}, nil
}

// resolveOpVersion looks up objectType's declared version(s) of opType
// within types (Store.Types' shape) and returns the one unambiguous
// version, refusing and naming the candidates when the type declares more
// than one.
func resolveOpVersion(types []SchemaType, objectType, opType string) (int64, error) {
	var td *SchemaType
	for i := range types {
		if types[i].Name == objectType {
			td = &types[i]
			break
		}
	}
	if td == nil {
		return 0, fmt.Errorf("object type %q is not declared by the installed vocabulary", objectType)
	}

	versionSet := make(map[int64]bool)
	for _, o := range td.Ops {
		if o.OpType == opType {
			versionSet[o.OpVersion] = true
		}
	}
	if len(versionSet) == 0 {
		return 0, fmt.Errorf("object type %q declares no op %q", objectType, opType)
	}
	if len(versionSet) > 1 {
		versions := make([]int64, 0, len(versionSet))
		for v := range versionSet {
			versions = append(versions, v)
		}
		sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
		return 0, fmt.Errorf("object type %q declares %d versions of op %q (%v): specify NewOp.Version explicitly", objectType, len(versions), opType, versions)
	}
	for v := range versionSet {
		return v, nil
	}
	panic("unreachable")
}

// nonNilFields returns fields, or an empty map when fields is nil:
// spec/schemas/op-envelope.schema.json requires body to be a JSON object,
// and json.Marshal(map[string]any(nil)) encodes as the JSON literal null,
// not {}.
func nonNilFields(fields map[string]any) map[string]any {
	if fields == nil {
		return map[string]any{}
	}
	return fields
}
