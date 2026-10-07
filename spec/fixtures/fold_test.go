package fixtures_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/codec/canonicaljson"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/identity"
	"github.com/writtendev/writ/internal/state"
	"github.com/writtendev/writ/spec"
	"github.com/writtendev/writ/spec/fixtures"
)

// TestFoldFamily registers the fold fixture family and runs all fold-*.yaml
// and forward-compat-*.yaml descriptions through the golden test harness.
func TestFoldFamily(t *testing.T) {
	fixtures.Run(t, fixtures.Family{
		Name:      "fold",
		GoldenDir: "testdata/golden/fold",
		Filter: func(desc *fixtures.Description) bool {
			return strings.HasPrefix(desc.Name, "fold-") || strings.HasPrefix(desc.Name, "forward-compat-") || desc.Name == "multi-writer-chains" || desc.Name == "reserved-ref-namespaces"
		},
		Runner: runFoldFixture,
	})
}

type FoldGolden struct {
	Objects    []FoldObjectGolden `json:"objects"`
	Rejections []FoldRejection    `json:"rejections,omitempty"`
}

// FoldRejection is one commit the reader walk rejected under
// spec/op-envelope.md §Reader validation, in the enumeration's own sorted
// order. Omitted from the golden when there are none, so a fixture with no
// rejections (reserved-ref-namespaces) pins "zero" by their absence.
type FoldRejection struct {
	Commit string `json:"commit"`
	Label  string `json:"label,omitempty"`
	Reason string `json:"reason"`
}

type FoldObjectGolden struct {
	ObjectID   string             `json:"object_id"`
	ObjectType string             `json:"object_type,omitempty"`
	TotalOrder []FoldOpOrderEntry `json:"total_order"`
	State      map[string]any     `json:"state"`
	UnknownOps []FoldUnknownOp    `json:"unknown_ops,omitempty"`
}

type FoldOpOrderEntry struct {
	Commit string `json:"commit"`
	Label  string `json:"label,omitempty"`
	TStar  int64  `json:"t_star"`
	// Verification is the op's verification outcome, pinned only for an op
	// with more than one carrier (WRIT-312): there it is the cross-carrier
	// rule's whole subject (spec/signing.md §Op Identity), and carriers of
	// one payload can disagree about it. Every other op's outcome is the
	// envelope family's, per commit.
	Verification string `json:"verification,omitempty"`
	// KeyFingerprint is the key fingerprint reported with that outcome, pinned
	// under the same condition: carriers can differ in it, and the tie-break
	// between equally trusted ones is the smallest carrier SHA's.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
}

type FoldUnknownOp struct {
	Commit     string `json:"commit"`
	Label      string `json:"label,omitempty"`
	ObjectType string `json:"object_type"`
	OpType     string `json:"op_type"`
	OpVersion  int64  `json:"op_version"`
}

func runFoldFixture(t *testing.T, fix *fixtures.Fixture) ([]byte, error) {
	t.Helper()

	store, err := dag.OpenRepo(fix.Repo, identity.Identity{})
	if err != nil {
		return nil, fmt.Errorf("dag.OpenRepo failed: %w", err)
	}

	// Verified against the fixture's own trust store, as the envelope family
	// does, so an op's reported outcome is the one a reader holding that
	// allowed_signers file would report. Verification never gates fold, so
	// nothing else in this runner reads it.
	trustStore, err := fixtures.TrustStoreFor(fix.Description)
	if err != nil {
		return nil, fmt.Errorf("create trust store: %w", err)
	}
	enumRes, err := store.Enumerate(dag.WithLiveTrustStore(trustStore))
	if err != nil {
		return nil, fmt.Errorf("store.Enumerate failed: %w", err)
	}

	switch fix.Name {
	case "fold-replayed-op":
		requireReplayedOpExercised(t, fix, enumRes)
	case "fold-replayed-op-verification":
		requireCarrierVerificationExercised(t, fix, enumRes, trustStore)
	}

	// Map commit SHA to description label
	shaToLabel := make(map[string]string)
	commitIdx := 0
	for _, ref := range fix.Description.Refs {
		for _, gen := range ref.History {
			gs := fix.Manifest.Generations[commitIdx]
			commitIdx++
			for ci, cd := range gen.Commits {
				cState := gs.Commits[ci]
				shaToLabel[cState.SHA] = cd.ID
			}
		}
	}

	var golden FoldGolden
	for _, rej := range enumRes.Rejections {
		golden.Rejections = append(golden.Rejections, FoldRejection{
			Commit: rej.CommitID,
			Label:  shaToLabel[rej.CommitID],
			Reason: string(rej.Reason),
		})
	}

	opsByObject := enumRes.Ops
	if len(opsByObject) == 0 {
		opsByObject = make(map[string][]codec.Op)
		seenCommits := make(map[string]bool)
		cIdx := 0
		for _, ref := range fix.Description.Refs {
			isControl := strings.HasSuffix(ref.Name, "-control")
			for _, gen := range ref.History {
				gs := fix.Manifest.Generations[cIdx]
				cIdx++
				if isControl {
					continue
				}
				for ci := range gen.Commits {
					cState := gs.Commits[ci]
					if seenCommits[cState.SHA] {
						continue
					}
					seenCommits[cState.SHA] = true
					commitObj, err := fix.Repo.CommitObject(plumbing.NewHash(cState.SHA))
					if err != nil {
						return nil, fmt.Errorf("lookup commit %s: %w", cState.SHA, err)
					}
					pureCommit, err := codec.FromGitCommit(fix.Repo.Storer, commitObj)
					if err != nil {
						return nil, fmt.Errorf("from git commit %s: %w", cState.SHA, err)
					}
					op, err := codec.DecodeCommit(pureCommit)
					if err != nil {
						continue
					}
					opsByObject[op.ObjectID] = append(opsByObject[op.ObjectID], op)
				}
			}
		}
	}

	// Sort object IDs for deterministic golden output
	var objectIDs []string
	for objID := range opsByObject {
		objectIDs = append(objectIDs, objID)
	}
	sort.Strings(objectIDs)

	// Merge rules come from the log, exactly as runSchemaDrivenFixture
	// resolves them: `schema` is the one object type writ hard-codes, so
	// every schema object in the fixture is folded with writ.FoldSchema and
	// writ.RulesFromSchemas turns the result into per-object_type rules.
	// A fixture that declares no schema still folds — with no rules, every
	// op reaching unknown_ops — which is a legal outcome, not an error.
	//
	// Schema objects themselves are not folded into the golden's objects:
	// here they are the rule source, and their own materialization is the
	// schema family's subject (TestSchemaFamily).
	var schemas []state.Schema
	nonSchemaOps := make(map[string][]codec.Op)
	var nonSchemaIDs []string
	for _, objID := range objectIDs {
		var schemaOps, otherOps []codec.Op
		for _, op := range opsByObject[objID] {
			if op.ObjectType == "schema" {
				schemaOps = append(schemaOps, op)
			} else {
				otherOps = append(otherOps, op)
			}
		}
		if len(schemaOps) > 0 {
			sch, err := state.FoldSchema(schemaOps)
			if err != nil {
				return nil, fmt.Errorf("state.FoldSchema for object %s in %s: %w", objID, fix.Name, err)
			}
			schemas = append(schemas, sch)
		}
		if len(otherOps) > 0 {
			nonSchemaOps[objID] = otherOps
			nonSchemaIDs = append(nonSchemaIDs, objID)
		}
	}

	rulesByType, conflicts := state.RulesFromSchemas(schemas)
	// How RulesFromSchemas reports a collision is the schema-driven family's
	// subject. No fold fixture declares one, and a conflict here would
	// withhold rules for the affected type and silently empty a golden's
	// state, so it fails loudly instead.
	if len(conflicts) > 0 {
		t.Fatalf("fixture %s declares conflicting schemas: %+v", fix.Name, conflicts)
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	for _, objID := range nonSchemaIDs {
		codecOps := nonSchemaOps[objID]
		if len(codecOps) == 0 {
			continue
		}

		// One object_id can carry ops of more than one object_type — an
		// unknown object type writing against a known object is precisely
		// what forward-compat-mixed-dag pins — and each of those ops folds
		// under the rules declared for its own type. spec.Fold's
		// opMatchesRule already scopes a rule to its declaring object type
		// (spec/fold.md §5), so handing it the union of the rules for the
		// types present does exactly that.
		writRules := rulesForObject(rulesByType, codecOps)

		// Cross-check: public state.Fold produces byte-identical canonical
		// state, TotalOrder and UnknownOps against the spec reference
		// reducer. This fixture family's own golden is built from the
		// reference fold's output (crossCheckSpecFold's returned State,
		// TotalOrder, EffectiveTimes and UnknownOps below), not the
		// engine's, so a divergence fails loudly here rather than baking
		// itself into the golden silently.
		engineRes, err := state.Fold(codecOps, writRules)
		if err != nil {
			return nil, fmt.Errorf("state.Fold for object %s: %w", objID, err)
		}

		cc, err := crossCheckSpecFold(t, fix.Name, objID, codecOps, writRules, engineRes)
		if err != nil {
			return nil, err
		}

		// The golden's unknown_ops is the fold's own quarantine channel, not a
		// second guess at it. Two populations reach that channel and
		// spec/fold.md §7.1 rule 2 puts them in the same one: an op whose
		// (op_type, op_version) no rule claims (§7), and an op a declared rule
		// found uninterpretable (§7.1). Deriving the list from the rule table
		// alone would see only the first, and a fixture repo carrying a
		// malformed body would emit a golden that omits an op both folds
		// quarantine — a normatively wrong golden in the artifact that is the
		// spec.
		//
		// The order is the fold's too. spec/reffold.go documents UnknownOps as
		// carrying the total order $L$, and a golden that re-sorted it by the
		// fixture's own commit order would record a weaker contract than the
		// channel actually has: two conforming readers could then disagree
		// about the sequence and both match the golden.
		byID := make(map[string]codec.Op, len(codecOps))
		for _, cop := range codecOps {
			byID[cop.ID] = cop
		}
		expectedObjectType := byID[cc.TotalOrder[0]].ObjectType

		var unknownOps []FoldUnknownOp
		for _, u := range cc.UnknownOps {
			unknownOps = append(unknownOps, FoldUnknownOp{
				Commit:     u.Commit,
				Label:      shaToLabel[u.Commit],
				ObjectType: u.ObjectType,
				OpType:     u.OpType,
				OpVersion:  u.OpVersion,
			})
		}

		if engineRes.ObjectType != expectedObjectType {
			t.Fatalf("engine ObjectType mismatch for %s in %s: got %q, want %q",
				objID, fix.Name, engineRes.ObjectType, expectedObjectType)
		}
		if got := state.DetermineObjectType(codecOps); got != expectedObjectType {
			t.Fatalf("state.DetermineObjectType mismatch for %s in %s: got %q, want %q",
				objID, fix.Name, got, expectedObjectType)
		}

		// Commutativity verification: shuffle input ops 100 times and verify identical output
		expectedJSON, err := canonicaljson.Marshal(mustJSON(t, cc.State))
		if err != nil {
			return nil, fmt.Errorf("canonicalizing folded state for %s: %w", objID, err)
		}

		for i := 0; i < 100; i++ {
			shuffledMerge := make([]spec.MergeOp, len(cc.MergeOps))
			copy(shuffledMerge, cc.MergeOps)
			r.Shuffle(len(shuffledMerge), func(i, j int) {
				shuffledMerge[i], shuffledMerge[j] = shuffledMerge[j], shuffledMerge[i]
			})

			shuffledFolded, err := spec.Fold(shuffledMerge, cc.Rules)
			if err != nil {
				t.Fatalf("commutativity violation on permutation #%d for object %s: %v", i, objID, err)
			}

			shuffledJSON, err := canonicaljson.Marshal(mustJSON(t, shuffledFolded.State))
			if err != nil {
				t.Fatalf("canonicalizing shuffled folded state on permutation #%d: %v", i, err)
			}

			if !bytes.Equal(shuffledJSON, expectedJSON) {
				t.Fatalf("commutativity violation on permutation #%d for object %s in fixture %s:\n got:  %s\n want: %s",
					i, objID, fix.Name, string(shuffledJSON), string(expectedJSON))
			}
		}

		opsByID := make(map[string]codec.Op, len(codecOps))
		for _, cop := range codecOps {
			opsByID[cop.ID] = cop
		}
		var orderEntries []FoldOpOrderEntry
		for _, sha := range cc.TotalOrder {
			entry := FoldOpOrderEntry{
				Commit: sha,
				Label:  shaToLabel[sha],
				TStar:  cc.EffectiveTimes[sha],
			}
			if len(enumRes.Carriers[sha]) > 1 {
				entry.Verification = string(opsByID[sha].Verification.Outcome)
				entry.KeyFingerprint = opsByID[sha].Verification.KeyFingerprint
			}
			orderEntries = append(orderEntries, entry)
		}

		golden.Objects = append(golden.Objects, FoldObjectGolden{
			ObjectID:   objID,
			ObjectType: expectedObjectType,
			TotalOrder: orderEntries,
			State:      cc.State,
			UnknownOps: unknownOps,
		})
	}

	b, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal fold golden: %w", err)
	}
	return append(b, '\n'), nil
}

// requireReplayedOpExercised pins what fold-replayed-op is for (WRIT-312), so
// the fixture cannot silently stop exercising it: Alice's revision and its
// armor-rewrapped replay are two commits but one op, whose id is the
// lowest-sorting of them; and Bob's commit names the carrier that did NOT
// become the op id as its parent line, so the golden only comes out right if
// the reader rewrote that edge to the op id.
func requireReplayedOpExercised(t *testing.T, fix *fixtures.Fixture, enumRes *dag.EnumerateResult) {
	t.Helper()
	sha := func(label string) string {
		s, ok := fix.CommitSHA(label)
		if !ok {
			t.Fatalf("%s: no commit labelled %q", fix.Name, label)
		}
		return s
	}
	original, replay, bob := sha("widget-revision"), sha("widget-revision-replay"), sha("bob-update")
	if original == replay {
		t.Fatalf("%s: armor-rewrap left the replay's SHA unchanged (%s)", fix.Name, original)
	}
	survivor, dropped := original, replay
	if replay < original {
		survivor, dropped = replay, original
	}

	if got := enumRes.Carriers[survivor]; len(got) != 2 || got[0] != survivor || got[1] != dropped {
		t.Fatalf("%s: Carriers[%s] = %v, want [%s %s]", fix.Name, survivor, got, survivor, dropped)
	}
	if _, ok := enumRes.Carriers[dropped]; ok {
		t.Fatalf("%s: dropped carrier %s is itself an op", fix.Name, dropped)
	}

	bobCommit, err := fix.Repo.CommitObject(plumbing.NewHash(bob))
	if err != nil {
		t.Fatalf("%s: lookup bob's commit: %v", fix.Name, err)
	}
	if len(bobCommit.ParentHashes) != 1 || bobCommit.ParentHashes[0].String() != dropped {
		t.Fatalf("%s: bob's parent line is %v, want the non-surviving carrier %s — repoint bob-update's parents at the other revision label",
			fix.Name, bobCommit.ParentHashes, dropped)
	}
	var bobOp *codec.Op
	for i, op := range enumRes.Ops["w-replayed-op"] {
		if op.ID == bob {
			bobOp = &enumRes.Ops["w-replayed-op"][i]
		}
	}
	if bobOp == nil {
		t.Fatalf("%s: bob's op %s not enumerated", fix.Name, bob)
	}
	if len(bobOp.Parents) != 1 || bobOp.Parents[0] != survivor {
		t.Fatalf("%s: bob's op parents = %v, want [%s] (the op id)", fix.Name, bobOp.Parents, survivor)
	}
}

// carrierTrustOrder is the WRIT-251 trust order, best first, spelled out here
// rather than read from codec so a fixture's coverage of it is judged against
// the ruling itself (spec/signing.md §Op Identity), not against the code under
// test.
var carrierTrustOrder = []codec.VerificationOutcome{
	codec.OutcomeValid,
	codec.OutcomeWrongKey,
	codec.OutcomeUnsigned,
	codec.OutcomeCorruptedSignature,
	codec.OutcomePayloadMutated,
}

// requireCarrierVerificationExercised pins what the fold-replayed-op-verification
// fixtures are for (WRIT-312), so they cannot silently stop exercising it.
// Everything is computed from the commits alone, never from what Enumerate
// reported, so it holds against a reader that has the rule wrong.
//
// Every commit's own outcome is the one the description declares. Then, for
// every op with more than one carrier:
//
//   - If its carriers do not all share one outcome, the lowest-SHA carrier,
//     the one whose SHA becomes the op id, must be strictly less trusted than
//     the best carrier, so a reader that reports the surviving carrier's own
//     verification gets it wrong.
//   - If they all share one outcome, they must carry at least two different
//     key fingerprints, so a reader that breaks the tie by anything other than
//     the smallest carrier SHA has a chance to report the other key; which
//     key is reported is the golden's to pin.
//
// Across the ops, every adjacent pair of carrierTrustOrder must be the best
// and the other outcome of some two-carrier op (so a reader that swaps just
// that pair fails the golden), and the tie-break must be exercised for every
// outcome that carries a key fingerprint (valid, wrong-key, payload-mutated;
// an unsigned or corrupted-signature carrier has no key, so a tie between
// them reports nothing a reader could get wrong).
func requireCarrierVerificationExercised(t *testing.T, fix *fixtures.Fixture, enumRes *dag.EnumerateResult, ts codec.TrustStore) {
	t.Helper()
	verify := func(sha string) codec.Verification {
		c, err := fix.Repo.CommitObject(plumbing.NewHash(sha))
		if err != nil {
			t.Fatalf("%s: lookup commit %s: %v", fix.Name, sha, err)
		}
		pure, err := codec.FromGitCommit(fix.Repo.Storer, c)
		if err != nil {
			t.Fatalf("%s: decode commit %s: %v", fix.Name, sha, err)
		}
		return codec.Verify(pure, ts)
	}
	trustRank := func(o codec.VerificationOutcome) int {
		for i, r := range carrierTrustOrder {
			if r == o {
				return i
			}
		}
		t.Fatalf("%s: outcome %q is outside the trust order", fix.Name, o)
		return -1
	}

	commitIdx := 0
	for _, ref := range fix.Description.Refs {
		for _, gen := range ref.History {
			gs := fix.Manifest.Generations[commitIdx]
			commitIdx++
			for ci, cd := range gen.Commits {
				if cd.Expect == nil || !cd.Expect.Accept {
					continue
				}
				want := codec.OutcomeValid
				if cd.Expect.Verification != "" {
					want = codec.VerificationOutcome(cd.Expect.Verification)
				}
				if got := verify(gs.Commits[ci].SHA).Outcome; got != want {
					t.Fatalf("%s: commit %q verifies as %q, description declares %q", fix.Name, cd.ID, got, want)
				}
			}
		}
	}

	type pair struct{ best, other codec.VerificationOutcome }
	pairs := map[pair]bool{}
	ties := map[codec.VerificationOutcome]bool{}
	for _, ops := range enumRes.Ops {
		for _, op := range ops {
			carriers := enumRes.Carriers[op.ID]
			if len(carriers) < 2 {
				continue
			}
			if carriers[0] != op.ID {
				t.Fatalf("%s: op %s is not its lowest carrier %s", fix.Name, op.ID, carriers[0])
			}
			vs := make([]codec.Verification, len(carriers))
			best, worst := 0, 0
			for i, c := range carriers {
				vs[i] = verify(c)
				if trustRank(vs[i].Outcome) < trustRank(vs[best].Outcome) {
					best = i
				}
				if trustRank(vs[i].Outcome) > trustRank(vs[worst].Outcome) {
					worst = i
				}
			}
			bestOutcome := vs[best].Outcome
			if vs[best].Outcome == vs[worst].Outcome {
				fingerprints := map[string]bool{}
				for _, v := range vs {
					fingerprints[v.KeyFingerprint] = true
				}
				if bestOutcome != codec.OutcomeUnsigned && bestOutcome != codec.OutcomeCorruptedSignature {
					ties[bestOutcome] = true
					if len(fingerprints) < 2 {
						t.Errorf("%s: op %s: its %d %q carriers all carry one key, so the tie-break between them is not observable",
							fix.Name, op.ID, len(carriers), bestOutcome)
					}
				}
				continue
			}
			if own := vs[0].Outcome; own == bestOutcome {
				t.Errorf("%s: op %s: its lowest-SHA carrier %s already has the op's best outcome %q; change the fixture's bodies so a less trusted carrier sorts lowest",
					fix.Name, op.ID, carriers[0], own)
			}
			if len(carriers) == 2 {
				pairs[pair{bestOutcome, vs[worst].Outcome}] = true
			}
		}
	}
	for i := 0; i+1 < len(carrierTrustOrder); i++ {
		if p := (pair{carrierTrustOrder[i], carrierTrustOrder[i+1]}); !pairs[p] {
			t.Errorf("%s: no two-carrier op pins %q over %q", fix.Name, p.best, p.other)
		}
	}
	for _, o := range []codec.VerificationOutcome{codec.OutcomeValid, codec.OutcomeWrongKey, codec.OutcomePayloadMutated} {
		if !ties[o] {
			t.Errorf("%s: no op pins the smallest-SHA tie-break between two %q carriers", fix.Name, o)
		}
	}
}

// TestFoldCoverage asserts that every field rule a fold-* fixture's schema
// object declares is exercised by at least one op in some fold-* fixture repo.
// Writ ships no vocabulary: a fixture's merge rules come from a schema object
// in its own log, so "no declared rule goes unexercised" is a property of the
// corpus's own schemas now, not of a shipped rule table.
//
// Catalogue-level coverage — every strategy in spec.KnownCatalogueStrategies
// having an abstract merge vector under spec/testdata/fold/merge/ — is
// spec/fold_test.go's own assertion, over every strategy rather than only the
// ones no vocabulary happened to use.
func TestFoldCoverage(t *testing.T) {
	corpus, err := fixtures.LoadCorpus()
	if err != nil {
		t.Fatalf("loading fixture corpus: %v", err)
	}

	type fieldKey struct {
		ObjectType string
		OpType     string
		Field      string
	}

	declared := make(map[fieldKey]string)
	covered := make(map[fieldKey]bool)

	for _, desc := range corpus {
		if !strings.HasPrefix(desc.Name, "fold-") {
			continue
		}
		for _, ref := range desc.Refs {
			for _, gen := range ref.History {
				for _, c := range gen.Commits {
					if c.Op == nil {
						continue
					}
					bodyMap, _ := c.Op.Body.(map[string]any)
					if c.Op.ObjectType == "schema" {
						if c.Op.OpType != "define-field" {
							continue
						}
						objType, _ := bodyMap["type"].(string)
						opType, _ := bodyMap["op_type"].(string)
						field, _ := bodyMap["field"].(string)
						declared[fieldKey{objType, opType, field}] = desc.Name
						continue
					}
					for f := range bodyMap {
						covered[fieldKey{c.Op.ObjectType, c.Op.OpType, f}] = true
					}
				}
			}
		}
	}

	if len(declared) == 0 {
		t.Fatal("no schema-declared field rules found in fold-* fixtures")
	}

	for key, declaredBy := range declared {
		if !covered[key] {
			t.Errorf("field rule declared by %s is exercised by no fold-* fixture op: (object_type: %s, op_type: %s, field: %s)",
				declaredBy, key.ObjectType, key.OpType, key.Field)
		}
	}
}

// rulesForObject returns the rules governing one object's ops: the union, in
// ascending object_type order, of the rules resolved for every object_type
// present among them.
func rulesForObject(rulesByType map[string][]state.Rule, ops []codec.Op) []state.Rule {
	seen := make(map[string]bool, len(ops))
	var objTypes []string
	for _, op := range ops {
		if seen[op.ObjectType] {
			continue
		}
		seen[op.ObjectType] = true
		objTypes = append(objTypes, op.ObjectType)
	}
	sort.Strings(objTypes)

	var rules []state.Rule
	for _, objType := range objTypes {
		rules = append(rules, rulesByType[objType]...)
	}
	return rules
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return b
}

// specFoldCrossCheck is the reference reducer's own output for the ops
// crossCheckSpecFold folded, returned so a caller building its golden from
// the reference implementation (as runFoldFixture does) does not need to
// fold a second time.
type specFoldCrossCheck struct {
	Rules          []spec.FieldRule
	MergeOps       []spec.MergeOp
	State          map[string]any
	TotalOrder     []string
	EffectiveTimes map[string]int64
	UnknownOps     []spec.UnknownOp
}

// crossCheckSpecFold folds codecOps through the reference implementation
// (spec.Fold, spec.TotalOrder, spec.EffectiveTimes, built from writRules the
// same way the engine's own rule table is) and requires the result to agree
// byte-for-byte with engineRes -- codecOps folded through state.Fold,
// using the same writRules -- on canonical State, TotalOrder
// (commit + t*), and UnknownOps.
//
// Both runFoldFixture (fold-* / forward-compat-* fixtures) and
// runSchemaDrivenFixture (schema-driven-* fixtures) call this for every
// non-schema object, so a divergence between the engine and the reference
// reducer can't hide in either fixture family (WRIT-274) -- before this, only
// the fold-* family ran the comparison at all.
func crossCheckSpecFold(t *testing.T, fixName, objID string, codecOps []codec.Op, writRules []state.Rule, engineRes state.ObjectState) (specFoldCrossCheck, error) {
	t.Helper()

	var rules []spec.FieldRule
	for _, wr := range writRules {
		rules = append(rules, spec.FieldRule{
			OpType:     wr.OpType,
			OpVersion:  wr.OpVersion,
			Field:      wr.Field,
			Target:     wr.Target,
			Strategy:   wr.Strategy,
			Key:        wr.Key,
			Lattice:    wr.Lattice,
			ValueType:  wr.ValueType,
			Enum:       wr.Enum,
			MaxLength:  wr.MaxLength,
			KeyTypes:   wr.KeyTypes,
			ObjectType: wr.ObjectType,
		})
	}

	var orderOps []spec.OrderOp
	var mergeOps []spec.MergeOp

	for _, cop := range codecOps {
		orderOps = append(orderOps, spec.OrderOp{
			ID:       cop.ID,
			Parents:  cop.Parents,
			Time:     cop.Author.When.UTC().Unix(),
			ObjectID: cop.ObjectID,
		})

		var body map[string]any
		if len(cop.Body) > 0 {
			if err := json.Unmarshal(cop.Body, &body); err != nil {
				t.Fatalf("unmarshaling op %s body: %v", cop.ID, err)
			}
		}
		if body == nil {
			body = make(map[string]any)
		}

		mergeOps = append(mergeOps, spec.MergeOp{
			ID:         cop.ID,
			Parents:    cop.Parents,
			Time:       cop.Author.When.UTC().Unix(),
			ObjectID:   cop.ObjectID,
			ObjectType: cop.ObjectType,
			OpType:     cop.OpType,
			OpVersion:  cop.OpVersion,
			Author: spec.MergeAuthor{
				Name:  cop.Author.Name,
				Email: cop.Author.Email,
			},
			Body: body,
		})
	}

	effectiveTimes := spec.EffectiveTimes(orderOps, objID)
	totalOrder, err := spec.TotalOrder(orderOps, objID)
	if err != nil {
		return specFoldCrossCheck{}, fmt.Errorf("total order for object %s in %s: %w", objID, fixName, err)
	}

	folded, err := spec.Fold(mergeOps, rules)
	if err != nil {
		return specFoldCrossCheck{}, fmt.Errorf("fold for object %s in %s: %w", objID, fixName, err)
	}

	engineJSON, err := canonicaljson.Marshal(mustJSON(t, engineRes.State))
	if err != nil {
		return specFoldCrossCheck{}, fmt.Errorf("canonicalizing engine state for %s in %s: %w", objID, fixName, err)
	}
	expectedJSON, err := canonicaljson.Marshal(mustJSON(t, folded.State))
	if err != nil {
		return specFoldCrossCheck{}, fmt.Errorf("canonicalizing spec reference state for %s in %s: %w", objID, fixName, err)
	}

	if !bytes.Equal(engineJSON, expectedJSON) {
		t.Fatalf("engine fold state differs from spec reference for object %s in fixture %s:\n engine: %s\n ref:    %s",
			objID, fixName, string(engineJSON), string(expectedJSON))
	}

	if len(engineRes.TotalOrder) != len(totalOrder) {
		t.Fatalf("engine TotalOrder length mismatch for %s in %s: got %d, want %d",
			objID, fixName, len(engineRes.TotalOrder), len(totalOrder))
	}
	for i, ref := range engineRes.TotalOrder {
		if ref.Commit != totalOrder[i] || ref.TStar != effectiveTimes[ref.Commit] {
			t.Fatalf("engine TotalOrder[%d] mismatch for %s in %s: got (%s, %d), want (%s, %d)",
				i, objID, fixName, ref.Commit, ref.TStar, totalOrder[i], effectiveTimes[totalOrder[i]])
		}
	}

	// The quarantine channel is cross-checked here on the same terms as
	// State and TotalOrder. It is the third thing fold returns; leaving it
	// out would let the two implementations disagree about which ops
	// contributed nothing -- and, since a quarantined op contributes no
	// writes, they could reach identical State by quarantining different
	// operations.
	if len(engineRes.UnknownOps) != len(folded.UnknownOps) {
		t.Fatalf("engine quarantined %d ops for %s in %s, spec reference quarantined %d:\n engine: %v\n ref:    %v",
			len(engineRes.UnknownOps), objID, fixName, len(folded.UnknownOps),
			engineRes.UnknownOps, folded.UnknownOps)
	}
	for i, engU := range engineRes.UnknownOps {
		refU := folded.UnknownOps[i]
		if engU.Commit != refU.Commit || engU.ObjectType != refU.ObjectType || engU.OpType != refU.OpType || engU.OpVersion != refU.OpVersion {
			t.Fatalf("engine UnknownOps[%d] mismatch for %s in %s:\n got: %+v\nwant: %+v",
				i, objID, fixName, engU, refU)
		}
	}

	return specFoldCrossCheck{
		Rules:          rules,
		MergeOps:       mergeOps,
		State:          folded.State,
		TotalOrder:     totalOrder,
		EffectiveTimes: effectiveTimes,
		UnknownOps:     folded.UnknownOps,
	}, nil
}
