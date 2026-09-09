package sync_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/spec"
)

// testSchemaObjectID is the schema object the declaration below is
// attributed to, so a producer rejection names one the way a real one does.
const testSchemaObjectID = "sch-acme"

// declaredType is one object type's entry in a resolved Vocabularies: the
// (op_type, op_version) pairs producer rule 4 accepts and the field rules
// rule 3 checks, both read off the rules given — the same derivation
// writ.VocabulariesFromSchemas makes from a folded schema object.
func declaredType(objectType string, rules ...spec.FieldRule) codec.Vocabulary {
	voc := codec.Vocabulary{
		Declared:       true,
		SchemaObjectID: testSchemaObjectID,
		OpTypes:        make(map[codec.OpVersionKey]bool),
		Fields:         make(map[codec.OpVersionKey][]spec.FieldRule),
	}
	for _, r := range rules {
		r.ObjectType = objectType
		key := codec.OpVersionKey{OpType: r.OpType, OpVersion: r.OpVersion}
		voc.OpTypes[key] = true
		voc.Fields[key] = append(voc.Fields[key], r)
	}
	return voc
}

// testVocabularies is what a repo whose log carries one schema object
// declaring these two types resolves to. Append consults it through
// dag.WithProducerVocabularies — the same wiring writ.Open uses — because
// `schema` aside, writ hard-codes no object type: an op of an undeclared
// type is refused before it is signed (spec/op-envelope.md §Producer
// validation).
func testVocabularies() (codec.Vocabularies, error) {
	return codec.Vocabularies{
		"widget": declaredType("widget",
			spec.FieldRule{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
			spec.FieldRule{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
			spec.FieldRule{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
			spec.FieldRule{OpType: "endorse", OpVersion: 1, Field: "revision", Strategy: "lww", ValueType: "git-oid"},
			spec.FieldRule{OpType: "endorse", OpVersion: 1, Field: "verdict", Strategy: "lww", ValueType: "enum", Enum: []string{"yes", "no"}},
		),
		"gadget": declaredType("gadget",
			spec.FieldRule{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		),
	}, nil
}

func testIdentity(wID string, name string, email string) identity.Identity {
	writerID, _ := identity.ParseWriterID(wID)
	return identity.Identity{
		WriterID: writerID,
		Author: identity.Author{
			Name:  name,
			Email: email,
		},
	}
}

func initTestRepo(t *testing.T) (string, *git.Repository) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit failed: %v", err)
	}

	// Configure basic git user config in local repo
	cmd := exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config user.name: %v", err)
	}
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config user.email: %v", err)
	}

	return dir, repo
}

func initBareRepo(t *testing.T) (string, *git.Repository) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, true)
	if err != nil {
		t.Fatalf("PlainInit bare failed: %v", err)
	}
	return dir, repo
}

func appendTestOp(t *testing.T, store *dag.Store, objType, objID, opType string, body map[string]any) string {
	t.Helper()
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	env := codec.Envelope{
		ObjectID:   objID,
		ObjectType: objType,
		OpType:     opType,
		OpVersion:  1,
		Body:       bodyBytes,
	}
	op, err := store.Append(context.Background(), env, nil)
	if err != nil {
		t.Fatalf("store.Append failed: %v", err)
	}
	return op.ID
}

func mustOpenStore(t *testing.T, dir string, ident identity.Identity) *dag.Store {
	t.Helper()
	fixedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store, err := dag.Open(dir, ident, dag.WithProducerVocabularies(testVocabularies), dag.WithNow(func() time.Time { return fixedTime }))
	if err != nil {
		t.Fatalf("dag.Open failed: %v", err)
	}
	return store
}

func snapshotAllRefs(t *testing.T, repo *git.Repository) map[string]string {
	t.Helper()
	iter, err := repo.References()
	if err != nil {
		t.Fatalf("References failed: %v", err)
	}
	defer iter.Close()
	refs := make(map[string]string)
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref != nil && ref.Type() == plumbing.HashReference {
			refs[ref.Name().String()] = ref.Hash().String()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ForEach ref: %v", err)
	}
	return refs
}
