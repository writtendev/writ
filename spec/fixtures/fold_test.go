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
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
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
			return strings.HasPrefix(desc.Name, "fold-") || strings.HasPrefix(desc.Name, "forward-compat-")
		},
		Runner: runFoldFixture,
	})
}

type FoldGolden struct {
	Objects []FoldObjectGolden `json:"objects"`
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

	enumRes, err := store.Enumerate()
	if err != nil {
		return nil, fmt.Errorf("store.Enumerate failed: %w", err)
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
	var schemas []writ.Schema
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
			sch, err := writ.FoldSchema(schemaOps)
			if err != nil {
				return nil, fmt.Errorf("writ.FoldSchema for object %s in %s: %w", objID, fix.Name, err)
			}
			schemas = append(schemas, sch)
		}
		if len(otherOps) > 0 {
			nonSchemaOps[objID] = otherOps
			nonSchemaIDs = append(nonSchemaIDs, objID)
		}
	}

	rulesByType, conflicts := writ.RulesFromSchemas(schemas)
	// How RulesFromSchemas reports a collision is the schema-driven family's
	// subject. No fold fixture declares one, and a conflict here would
	// withhold rules for the contested type and silently empty a golden's
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
		rules := make([]spec.FieldRule, 0, len(writRules))
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
			return nil, fmt.Errorf("total order for object %s: %w", objID, err)
		}

		folded, err := spec.Fold(mergeOps, rules)
		if err != nil {
			return nil, fmt.Errorf("fold for object %s: %w", objID, err)
		}
		foldedState := folded.State

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
		var unknownOps []FoldUnknownOp
		for _, u := range folded.UnknownOps {
			unknownOps = append(unknownOps, FoldUnknownOp{
				Commit:     u.Commit,
				Label:      shaToLabel[u.Commit],
				ObjectType: u.ObjectType,
				OpType:     u.OpType,
				OpVersion:  u.OpVersion,
			})
		}

		// Cross-check: public writ.Fold produces byte-identical canonical state and total order
		engineRes, err := writ.Fold(codecOps, writRules)
		if err != nil {
			return nil, fmt.Errorf("writ.Fold for object %s: %w", objID, err)
		}
		engineJSON, err := canonicaljson.Marshal(mustJSON(t, engineRes.State))
		if err != nil {
			return nil, fmt.Errorf("canonicalizing engine state for %s: %w", objID, err)
		}

		// Commutativity verification: shuffle input ops 100 times and verify identical output
		expectedJSON, err := canonicaljson.Marshal(mustJSON(t, foldedState))
		if err != nil {
			return nil, fmt.Errorf("canonicalizing folded state for %s: %w", objID, err)
		}

		if !bytes.Equal(engineJSON, expectedJSON) {
			t.Fatalf("engine fold state differs from spec reference for object %s in fixture %s:\n engine: %s\n ref:    %s",
				objID, fix.Name, string(engineJSON), string(expectedJSON))
		}

		if len(engineRes.TotalOrder) != len(totalOrder) {
			t.Fatalf("engine TotalOrder length mismatch for %s in %s: got %d, want %d",
				objID, fix.Name, len(engineRes.TotalOrder), len(totalOrder))
		}
		for i, ref := range engineRes.TotalOrder {
			if ref.Commit != totalOrder[i] || ref.TStar != effectiveTimes[ref.Commit] {
				t.Fatalf("engine TotalOrder[%d] mismatch for %s in %s: got (%s, %d), want (%s, %d)",
					i, objID, fix.Name, ref.Commit, ref.TStar, totalOrder[i], effectiveTimes[totalOrder[i]])
			}
		}

		// The quarantine channel is cross-checked here on the same terms as
		// State and TotalOrder. It is the third thing fold returns and the one
		// this fixture family exists to exercise; leaving it out would let the
		// two implementations disagree about which ops contributed nothing —
		// and, since a quarantined op contributes no writes, they could reach
		// identical State by quarantining different operations.
		if len(engineRes.UnknownOps) != len(folded.UnknownOps) {
			t.Fatalf("engine quarantined %d ops for %s in %s, spec reference quarantined %d:\n engine: %v\n ref:    %v",
				len(engineRes.UnknownOps), objID, fix.Name, len(folded.UnknownOps),
				engineRes.UnknownOps, folded.UnknownOps)
		}
		for i, engU := range engineRes.UnknownOps {
			refU := folded.UnknownOps[i]
			if engU.Commit != refU.Commit || engU.ObjectType != refU.ObjectType || engU.OpType != refU.OpType || engU.OpVersion != refU.OpVersion {
				t.Fatalf("engine UnknownOps[%d] mismatch for %s in %s:\n got: %+v\nwant: %+v",
					i, objID, fix.Name, engU, refU)
			}
		}

		for i := 0; i < 100; i++ {
			shuffledMerge := make([]spec.MergeOp, len(mergeOps))
			copy(shuffledMerge, mergeOps)
			r.Shuffle(len(shuffledMerge), func(i, j int) {
				shuffledMerge[i], shuffledMerge[j] = shuffledMerge[j], shuffledMerge[i]
			})

			shuffledFolded, err := spec.Fold(shuffledMerge, rules)
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

		var orderEntries []FoldOpOrderEntry
		for _, sha := range totalOrder {
			orderEntries = append(orderEntries, FoldOpOrderEntry{
				Commit: sha,
				Label:  shaToLabel[sha],
				TStar:  effectiveTimes[sha],
			})
		}

		golden.Objects = append(golden.Objects, FoldObjectGolden{
			ObjectID:   objID,
			ObjectType: codecOps[0].ObjectType,
			TotalOrder: orderEntries,
			State:      foldedState,
			UnknownOps: unknownOps,
		})
	}

	b, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal fold golden: %w", err)
	}
	return append(b, '\n'), nil
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
func rulesForObject(rulesByType map[string][]writ.Rule, ops []codec.Op) []writ.Rule {
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

	var rules []writ.Rule
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

