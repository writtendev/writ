package fixtures_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/spec/fixtures"
)

// TestDocumentFamily registers the document fixture family and runs all descriptions
// carrying document and section collaborative objects through the typed fold golden test harness.
func TestDocumentFamily(t *testing.T) {
	fixtures.Run(t, fixtures.Family{
		Name:      "document",
		GoldenDir: "testdata/golden/document",
		Filter: func(desc *fixtures.Description) bool {
			if !strings.HasPrefix(desc.Name, "document-") {
				return false
			}
			for _, ref := range desc.Refs {
				for _, gen := range ref.History {
					for _, c := range gen.Commits {
						if c.Op != nil && (c.Op.ObjectType == "document" || c.Op.ObjectType == "section") {
							return true
						}
					}
				}
			}
			return false
		},
		Runner: runDocumentFixture,
	})
}

type DocumentGolden struct {
	Documents []DocumentObjectGolden `json:"documents,omitempty"`
	Sections  []SectionObjectGolden  `json:"sections,omitempty"`
}

type DocumentObjectGolden struct {
	ObjectID string        `json:"object_id"`
	Document writ.Document `json:"document"`
}

type SectionObjectGolden struct {
	ObjectID string       `json:"object_id"`
	Section  writ.Section `json:"section"`
}

func runDocumentFixture(t *testing.T, fix *fixtures.Fixture) ([]byte, error) {
	t.Helper()

	store, err := dag.OpenRepo(fix.Repo, identity.Identity{})
	if err != nil {
		return nil, fmt.Errorf("dag.OpenRepo failed: %w", err)
	}

	enumRes, err := store.Enumerate()
	if err != nil {
		return nil, fmt.Errorf("store.Enumerate failed: %w", err)
	}

	var golden DocumentGolden

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
		var docOps []codec.Op
		var secOps []codec.Op
		for _, op := range codecOps {
			if op.ObjectType == "document" {
				docOps = append(docOps, op)
			} else if op.ObjectType == "section" {
				secOps = append(secOps, op)
			}
		}

		if len(docOps) > 0 {
			docState, err := writ.FoldDocument(docOps)
			if err != nil {
				return nil, fmt.Errorf("writ.FoldDocument for %s in %s: %w", objID, fix.Name, err)
			}

			objectState, err := writ.Fold(codecOps, writ.DocumentRules())
			if err != nil {
				return nil, fmt.Errorf("writ.Fold for document %s in %s: %w", objID, fix.Name, err)
			}
			assertDocumentFoldAgreement(t, docState, objectState, fix.Name, objID)

			expectedJSON, err := canonicaljson.Marshal(mustJSON(t, docState))
			if err != nil {
				return nil, fmt.Errorf("canonicalizing document %s: %w", objID, err)
			}

			for i := 0; i < 100; i++ {
				shuffled := make([]codec.Op, len(docOps))
				copy(shuffled, docOps)
				r.Shuffle(len(shuffled), func(i, j int) {
					shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
				})

				shuffledDoc, err := writ.FoldDocument(shuffled)
				if err != nil {
					t.Fatalf("commutativity violation on permutation #%d for document %s in %s: %v", i, objID, fix.Name, err)
				}
				shuffledJSON, err := canonicaljson.Marshal(mustJSON(t, shuffledDoc))
				if err != nil {
					t.Fatalf("canonicalizing shuffled document on permutation #%d for %s in %s: %v", i, objID, fix.Name, err)
				}
				if !bytes.Equal(shuffledJSON, expectedJSON) {
					t.Fatalf("commutativity violation on permutation #%d for document %s in %s:\n got:  %s\n want: %s",
						i, objID, fix.Name, string(shuffledJSON), string(expectedJSON))
				}
			}

			golden.Documents = append(golden.Documents, DocumentObjectGolden{
				ObjectID: objID,
				Document: docState,
			})
		}

		if len(secOps) > 0 {
			secState, err := writ.FoldSection(secOps)
			if err != nil {
				return nil, fmt.Errorf("writ.FoldSection for %s in %s: %w", objID, fix.Name, err)
			}

			objectState, err := writ.Fold(codecOps, writ.SectionRules())
			if err != nil {
				return nil, fmt.Errorf("writ.Fold for section %s in %s: %w", objID, fix.Name, err)
			}
			assertSectionFoldAgreement(t, secState, objectState, fix.Name, objID)

			expectedJSON, err := canonicaljson.Marshal(mustJSON(t, secState))
			if err != nil {
				return nil, fmt.Errorf("canonicalizing section %s: %w", objID, err)
			}

			for i := 0; i < 100; i++ {
				shuffled := make([]codec.Op, len(secOps))
				copy(shuffled, secOps)
				r.Shuffle(len(shuffled), func(i, j int) {
					shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
				})

				shuffledSec, err := writ.FoldSection(shuffled)
				if err != nil {
					t.Fatalf("commutativity violation on permutation #%d for section %s in %s: %v", i, objID, fix.Name, err)
				}
				shuffledJSON, err := canonicaljson.Marshal(mustJSON(t, shuffledSec))
				if err != nil {
					t.Fatalf("canonicalizing shuffled section on permutation #%d for %s in %s: %v", i, objID, fix.Name, err)
				}
				if !bytes.Equal(shuffledJSON, expectedJSON) {
					t.Fatalf("commutativity violation on permutation #%d for section %s in %s:\n got:  %s\n want: %s",
						i, objID, fix.Name, string(shuffledJSON), string(expectedJSON))
				}
			}

			golden.Sections = append(golden.Sections, SectionObjectGolden{
				ObjectID: objID,
				Section:  secState,
			})
		}
	}

	if len(golden.Documents) == 0 && len(golden.Sections) == 0 {
		return nil, fmt.Errorf("document fixture %s yielded zero document or section objects", fix.Name)
	}

	b, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal document golden: %w", err)
	}
	return append(b, '\n'), nil
}

func assertDocumentFoldAgreement(t *testing.T, doc writ.Document, state writ.ObjectState, fixtureName, objectID string) {
	t.Helper()

	if title, ok := state.State["title"].(string); ok {
		if doc.Title != title {
			t.Errorf("[%s/%s] agreement mismatch on title: FoldDocument=%q, Fold=%q", fixtureName, objectID, doc.Title, title)
		}
	} else if doc.Title != "" {
		t.Errorf("[%s/%s] title present in FoldDocument (%q) but not in Fold", fixtureName, objectID, doc.Title)
	}

	var expectedLabels []string
	switch v := state.State["labels"].(type) {
	case []string:
		expectedLabels = append(expectedLabels, v...)
	case []any:
		for _, l := range v {
			if s, ok := l.(string); ok {
				expectedLabels = append(expectedLabels, s)
			}
		}
	}
	sort.Strings(expectedLabels)
	if len(doc.Labels) > 0 || len(expectedLabels) > 0 {
		if !reflect.DeepEqual(doc.Labels, expectedLabels) {
			t.Errorf("[%s/%s] agreement mismatch on labels: FoldDocument=%v, Fold=%v", fixtureName, objectID, doc.Labels, expectedLabels)
		}
	}
}

func assertSectionFoldAgreement(t *testing.T, sec writ.Section, state writ.ObjectState, fixtureName, objectID string) {
	t.Helper()

	var docID string
	switch v := state.State["document_id"].(type) {
	case string:
		docID = v
	case json.RawMessage:
		_ = json.Unmarshal(v, &docID)
	case []byte:
		_ = json.Unmarshal(v, &docID)
	}

	if docID != "" {
		if sec.DocumentID != docID {
			t.Errorf("[%s/%s] agreement mismatch on document_id: FoldSection=%q, Fold=%q", fixtureName, objectID, sec.DocumentID, docID)
		}
	} else if sec.DocumentID != "" {
		t.Errorf("[%s/%s] document_id present in FoldSection (%q) but not in Fold", fixtureName, objectID, sec.DocumentID)
	}

	if pos, ok := state.State["position"].(string); ok {
		if sec.Position != pos {
			t.Errorf("[%s/%s] agreement mismatch on position: FoldSection=%q, Fold=%q", fixtureName, objectID, sec.Position, pos)
		}
	} else if sec.Position != "" {
		t.Errorf("[%s/%s] position present in FoldSection (%q) but not in Fold", fixtureName, objectID, sec.Position)
	}

	if title, ok := state.State["title"].(string); ok {
		if sec.Title != title {
			t.Errorf("[%s/%s] agreement mismatch on title: FoldSection=%q, Fold=%q", fixtureName, objectID, sec.Title, title)
		}
	} else if sec.Title != "" {
		t.Errorf("[%s/%s] title present in FoldSection (%q) but not in Fold", fixtureName, objectID, sec.Title)
	}

	if del, ok := state.State["deleted"].(bool); ok {
		if sec.Deleted != del {
			t.Errorf("[%s/%s] agreement mismatch on deleted: FoldSection=%v, Fold=%v", fixtureName, objectID, sec.Deleted, del)
		}
	} else if sec.Deleted {
		t.Errorf("[%s/%s] deleted=true in FoldSection but false in Fold", fixtureName, objectID)
	}

	rawBody := state.State["body"]
	switch v := rawBody.(type) {
	case string:
		if sec.SettledBody() != v {
			t.Errorf("[%s/%s] agreement mismatch on body: FoldSection=%q, Fold=%q", fixtureName, objectID, sec.SettledBody(), v)
		}
	case []any:
		var expected []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				expected = append(expected, s)
			}
		}
		if !reflect.DeepEqual(sec.ConflictBodies(), expected) {
			t.Errorf("[%s/%s] agreement mismatch on conflicted body: FoldSection=%v, Fold=%v", fixtureName, objectID, sec.ConflictBodies(), expected)
		}
	}
}
