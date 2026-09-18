package fixtures_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/codec/canonicaljson"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/identity"
	"github.com/writtendev/writ/spec/fixtures"
)

// TestSchemaFamily registers the schema fixture family and runs all
// schema-*.yaml descriptions (excluding schema-driven-*, Stage B's separate
// family, spec/schemadriven_test.go) through the typed FoldSchema golden test
// harness. This closes the inherited WRIT-186 obligation: spec/schema-ops.md
// §3.1's non-canonical op_version quarantine has, until now, been pinned
// only by two Go tests in engine/state/schema_test.go, not by a conformance
// fixture — schema was the one shipped typed reducer with no signed-fixture
// golden family (spec/schema-ops.md §Conformance Data).
func TestSchemaFamily(t *testing.T) {
	fixtures.Run(t, fixtures.Family{
		Name:      "schema",
		GoldenDir: "testdata/golden/schema",
		Filter: func(desc *fixtures.Description) bool {
			if !strings.HasPrefix(desc.Name, "schema-") || strings.HasPrefix(desc.Name, "schema-driven-") {
				return false
			}
			for _, ref := range desc.Refs {
				for _, gen := range ref.History {
					for _, c := range gen.Commits {
						if c.Op != nil && c.Op.ObjectType == "schema" {
							return true
						}
					}
				}
			}
			return false
		},
		Runner: runSchemaFixture,
	})
}

type SchemaGolden struct {
	Objects []SchemaObjectGolden `json:"objects"`
}

type SchemaObjectGolden struct {
	ObjectID string      `json:"object_id"`
	Schema   writ.Schema `json:"schema"`
}

// canonicalOpVersionPattern mirrors spec/schemas/schema-ops.schema.json's
// op_version_string pattern (spec/schema-ops.md §3.1): digits only, no
// leading zero, non-empty, at most 16 digits.
var canonicalOpVersionPattern = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)

// maxLengthRepresentable mirrors engine/state/schema.go's decodeMaxLength
// gate (spec/schema-ops.md §3.3, WRIT-269): true when raw — a value decoded
// from a define-field body's max_length via json.Decoder.UseNumber, as
// FoldSchema itself decodes it — is a JSON integer representable in
// int64. Duplicated here rather than imported because it is unexported: the
// two must be kept in agreement by hand, the same way this file already
// keeps canonicalOpVersionPattern in agreement with FoldSchema's own
// canonicalOpVersion by hand.
func maxLengthRepresentable(raw any) bool {
	n, ok := raw.(json.Number)
	if !ok {
		return false
	}
	_, err := n.Int64()
	return err == nil
}

func runSchemaFixture(t *testing.T, fix *fixtures.Fixture) ([]byte, error) {
	t.Helper()

	store, err := dag.OpenRepo(fix.Repo, identity.Identity{})
	if err != nil {
		return nil, fmt.Errorf("dag.OpenRepo failed: %w", err)
	}

	enumRes, err := store.Enumerate()
	if err != nil {
		return nil, fmt.Errorf("store.Enumerate failed: %w", err)
	}

	var golden SchemaGolden

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

	var objectIDs []string
	for objID := range opsByObject {
		objectIDs = append(objectIDs, objID)
	}
	sort.Strings(objectIDs)

	r := rand.New(rand.NewSource(42))

	for _, objID := range objectIDs {
		codecOps := opsByObject[objID]
		var schemaOps []codec.Op
		opByID := make(map[string]codec.Op, len(codecOps))
		for _, op := range codecOps {
			opByID[op.ID] = op
			if op.ObjectType == "schema" {
				schemaOps = append(schemaOps, op)
			}
		}
		if len(schemaOps) == 0 {
			continue
		}

		schemaState, err := writ.FoldSchema(schemaOps)
		if err != nil {
			return nil, fmt.Errorf("writ.FoldSchema for object %s in %s: %w", objID, fix.Name, err)
		}

		objectState, err := writ.Fold(schemaOps, writ.SchemaRules())
		if err != nil {
			return nil, fmt.Errorf("writ.Fold for object %s in %s: %w", objID, fix.Name, err)
		}
		assertSchemaFoldSuperset(t, schemaState, objectState, fix.Name, objID, opByID)

		expectedJSON, err := canonicaljson.Marshal(mustJSON(t, schemaState))
		if err != nil {
			return nil, fmt.Errorf("canonicalizing schema state for %s: %w", objID, err)
		}

		for i := 0; i < 100; i++ {
			shuffled := make([]codec.Op, len(schemaOps))
			copy(shuffled, schemaOps)
			r.Shuffle(len(shuffled), func(i, j int) {
				shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
			})

			shuffledSchema, err := writ.FoldSchema(shuffled)
			if err != nil {
				t.Fatalf("commutativity violation on permutation #%d for object %s in %s: %v", i, objID, fix.Name, err)
			}

			shuffledJSON, err := canonicaljson.Marshal(mustJSON(t, shuffledSchema))
			if err != nil {
				t.Fatalf("canonicalizing shuffled schema state on permutation #%d for %s in %s: %v", i, objID, fix.Name, err)
			}

			if !bytes.Equal(shuffledJSON, expectedJSON) {
				t.Fatalf("commutativity violation on permutation #%d for object %s in fixture %s:\n got:  %s\n want: %s",
					i, objID, fix.Name, string(shuffledJSON), string(expectedJSON))
			}
		}

		golden.Objects = append(golden.Objects, SchemaObjectGolden{
			ObjectID: objID,
			Schema:   schemaState,
		})
	}

	if len(golden.Objects) == 0 {
		return nil, fmt.Errorf("schema fixture %s yielded zero schema objects", fix.Name)
	}

	b, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal schema golden: %w", err)
	}
	return append(b, '\n'), nil
}

// assertSchemaFoldSuperset is the asymmetric cross-check spec/schema-ops.md
// §3.1, §3.3, and §3.4 require. Do not copy assertSettingsFoldAgreement
// (settings_test.go) as a symmetric equality: writ.Fold(ops,
// writ.SchemaRules()) does NOT quarantine a non-canonical op_version body
// field, because ruleAccepts treats "01" as an ordinary keyed-lww
// key-component string, nor an unrepresentable max_length, because
// ruleAccepts' keyed-lww case (max_length's own meta-rule) only asks
// v != nil, nor a create whose body namespace disagrees with its
// target's derived-id suffix, because that check is a comparison between
// the op's own ObjectID and a body field — nothing a generic Rule can
// express at all — both checks live one layer up, in the typed
// writ.FoldSchema reducer (state.FoldSchema's canonicalOpVersion,
// decodeMaxLength, and namespace-implied-by-id gates). A symmetric
// assertion fails on this family by construction, and weakening the
// golden to make it pass would delete the rule this stage exists to
// pin. The correct assertion is that FoldSchema's UnknownOps is a
// superset of Fold's, with the difference being exactly: (a)
// define-op/define-field/deprecate-field ops whose op_version body field
// is not canonical, (b) define-field ops whose max_length body field is
// present and not a JSON integer representable in int64 (WRIT-269), and
// (c) create ops on a "schema:"-prefixed ObjectID whose body namespace
// disagrees with the id's own suffix, or omits it entirely (WRIT-254).
func assertSchemaFoldSuperset(t *testing.T, sch writ.Schema, state writ.ObjectState, fixtureName, objectID string, opByID map[string]codec.Op) {
	t.Helper()

	schemaUnknown := make(map[string]bool, len(sch.UnknownOps))
	for _, u := range sch.UnknownOps {
		schemaUnknown[u.Commit] = true
	}
	genericUnknown := make(map[string]bool, len(state.UnknownOps))
	for _, u := range state.UnknownOps {
		genericUnknown[u.Commit] = true
	}

	for id := range genericUnknown {
		if !schemaUnknown[id] {
			t.Errorf("[%s/%s] op %s is unknown under writ.Fold but known under writ.FoldSchema; FoldSchema's UnknownOps must be a superset of Fold's (spec/schema-ops.md §3.1)",
				fixtureName, objectID, id)
		}
	}

	for id := range schemaUnknown {
		if genericUnknown[id] {
			continue
		}
		op, ok := opByID[id]
		if !ok {
			t.Errorf("[%s/%s] schema-unknown op %s has no corresponding op in this fixture", fixtureName, objectID, id)
			continue
		}
		var body map[string]any
		if len(op.Body) > 0 {
			// UseNumber, matching state.FoldSchema's own body decode: a
			// max_length check below needs to tell a JSON number from a
			// JSON string the same way FoldSchema does.
			dec := json.NewDecoder(bytes.NewReader(op.Body))
			dec.UseNumber()
			if err := dec.Decode(&body); err != nil {
				t.Errorf("[%s/%s] unmarshaling op %s body: %v", fixtureName, objectID, id, err)
				continue
			}
		}
		if op.OpType == "create" {
			if suffix, ok := strings.CutPrefix(op.ObjectID, "schema:"); ok {
				bodyNamespace, _ := body["namespace"].(string)
				if bodyNamespace != suffix {
					continue // permitted: namespace-implied-by-id quarantine (§3.4, WRIT-254)
				}
			}
		}
		if op.OpType != "define-op" && op.OpType != "define-field" && op.OpType != "deprecate-field" {
			t.Errorf("[%s/%s] op %s (op_type=%s) is schema-unknown but not generic-unknown; the only permitted asymmetries are the non-canonical op_version quarantine, the unrepresentable max_length quarantine, and the namespace-implied-by-id quarantine on a create, and none of those apply here",
				fixtureName, objectID, id, op.OpType)
			continue
		}
		opVersion, _ := body["op_version"].(string)
		if !canonicalOpVersionPattern.MatchString(opVersion) {
			continue // permitted: non-canonical op_version quarantine (§3.1)
		}
		if op.OpType == "define-field" {
			if raw, present := body["max_length"]; present && !maxLengthRepresentable(raw) {
				continue // permitted: unrepresentable max_length quarantine (§3.3, WRIT-269)
			}
		}
		t.Errorf("[%s/%s] op %s is schema-unknown but not generic-unknown, yet its op_version %q is canonical and its max_length (if any) is int64-representable; the only permitted asymmetries are the non-canonical op_version and unrepresentable max_length quarantines",
			fixtureName, objectID, id, opVersion)
	}
}
