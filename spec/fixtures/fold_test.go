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

	rules, err := spec.FieldRules()
	if err != nil {
		return nil, fmt.Errorf("loading field rules: %w", err)
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

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	for _, objID := range objectIDs {
		codecOps := opsByObject[objID]
		if len(codecOps) == 0 {
			continue
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
		var writRules []writ.Rule
		for _, r := range rules {
			writRules = append(writRules, writ.Rule{
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

// TestFoldCoverage asserts that every (op_type, field) rule in published field-rules.json
// is exercised by at least one op in some fold-* fixture repo.
// Catalogue strategies with no v1 vocabulary field are explicitly exempted and required
// to be covered by abstract merge vectors instead.
func TestFoldCoverage(t *testing.T) {
	rules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("loading field rules: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("no field rules loaded")
	}

	corpus, err := fixtures.LoadCorpus()
	if err != nil {
		t.Fatalf("loading fixture corpus: %v", err)
	}

	// Collect all (op_type, field) writes present in fold-* descriptions
	coveredFields := make(map[string]bool)

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
					opType := c.Op.OpType
					if opType == "delete" {
						coveredFields[fmt.Sprintf("%s:deleted", opType)] = true
					}
					if bodyMap, ok := c.Op.Body.(map[string]any); ok {
						for f := range bodyMap {
							coveredFields[fmt.Sprintf("%s:%s", opType, f)] = true
						}
					}
				}
			}
		}
	}

	// Check that every published rule for repo-scoped fixtures (review-ops, comments) has coverage in fold-* fixtures
	for _, rule := range rules {
		if rule.Vocabulary != "review-ops" && rule.Vocabulary != "comments" {
			continue
		}
		key := fmt.Sprintf("%s:%s", rule.OpType, rule.Field)
		if !coveredFields[key] {
			t.Errorf("uncovered field rule in fold-* fixtures: (%s, op_version: %d, field: %s, strategy: %s)",
				rule.OpType, rule.OpVersion, rule.Field, rule.Strategy)
		}
	}

	// Catalogue strategies with no v1 vocabulary field (covered by abstract merge vectors only)
	exemptStrategies := map[string]bool{
		"set-union":           true,
		"set-observed-remove": true,
		"lattice":             true,
	}

	mergeVectors, err := spec.MergeVectors()
	if err != nil {
		t.Fatalf("loading merge vectors: %v", err)
	}

	abstractCoverage := make(map[string]bool)
	for _, vec := range mergeVectors {
		for _, cfg := range vec.Fields {
			abstractCoverage[cfg.Strategy] = true
		}
	}

	for strat := range exemptStrategies {
		if !abstractCoverage[strat] {
			t.Errorf("exempt catalogue strategy %q has no abstract merge vector under testdata/fold/merge/", strat)
		}
	}

	// Check that every catalogue strategy is accounted for (either in published rules or exempt list)
	usedStrategies := make(map[string]bool)
	for _, r := range rules {
		usedStrategies[r.Strategy] = true
	}
	for strat := range spec.KnownCatalogueStrategies {
		if !usedStrategies[strat] && !exemptStrategies[strat] {
			t.Errorf("catalogue strategy %q is neither used in field rules nor in exemptStrategies", strat)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return b
}

