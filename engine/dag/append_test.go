package dag_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
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
// writ.VocabulariesFromSchemas makes from a folded schema object, and, like
// spec/schema-ops.md §4.2's generosity, a field rule alone declares the op
// type it names.
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
			spec.FieldRule{OpType: "update", OpVersion: 1, Field: "seq", Strategy: "lww", ValueType: "int"},
			spec.FieldRule{OpType: "set-status", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "enum", Enum: []string{"open", "closed"}},
		),
		"waypoint": declaredType("waypoint",
			spec.FieldRule{OpType: "create", OpVersion: 1, Field: "text", Strategy: "lww", ValueType: "text"},
		),
	}, nil
}

// withVocabularies is the option every store in these tests is opened with,
// spelled once.
func withVocabularies() dag.Option {
	return dag.WithProducerVocabularies(testVocabularies)
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
	return dir, repo
}

func snapshotRefs(t *testing.T, repo *git.Repository) map[string]string {
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

func TestAppend_AtomicAndLocalChainOnly(t *testing.T) {
	dir, repo := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")

	// Set an unrelated branch and tag to ensure they are untouched
	dummyCommit := plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", dummyCommit))
	_ = repo.Storer.SetReference(plumbing.NewHashReference("refs/tags/v1.0.0", dummyCommit))

	fixedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store, err := dag.Open(dir, ident, withVocabularies(), dag.WithNow(func() time.Time { return fixedTime }))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	before := snapshotRefs(t, repo)

	env1 := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Widget 1"}`),
	}

	op1, err := store.Append(context.Background(), env1, nil)
	if err != nil {
		t.Fatalf("Append 1 failed: %v", err)
	}

	after1 := snapshotRefs(t, repo)

	// Assert exactly one ref moved: refs/writ/0123456789abcdef/widget
	expectedRef := "refs/writ/0123456789abcdef/widget"
	if len(after1) != len(before)+1 {
		t.Fatalf("expected ref count %d, got %d", len(before)+1, len(after1))
	}
	if after1[expectedRef] != op1.ID {
		t.Fatalf("ref %s = %s, want %s", expectedRef, after1[expectedRef], op1.ID)
	}
	for k, v := range before {
		if after1[k] != v {
			t.Errorf("ref %s unexpectedly changed from %s to %s", k, v, after1[k])
		}
	}

	// Verify commit is a root commit (0 parents)
	commitObj, err := repo.CommitObject(plumbing.NewHash(op1.ID))
	if err != nil {
		t.Fatalf("CommitObject failed: %v", err)
	}
	if len(commitObj.ParentHashes) != 0 {
		t.Fatalf("root op should have 0 parents, got %d", len(commitObj.ParentHashes))
	}

	// Append second op onto same chain
	env2 := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "update",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Widget 1 updated"}`),
	}

	op2, err := store.Append(context.Background(), env2, nil)
	if err != nil {
		t.Fatalf("Append 2 failed: %v", err)
	}

	after2 := snapshotRefs(t, repo)
	if len(after2) != len(after1) {
		t.Fatalf("ref count changed: %d -> %d", len(after1), len(after2))
	}
	if after2[expectedRef] != op2.ID {
		t.Fatalf("ref %s = %s, want %s", expectedRef, after2[expectedRef], op2.ID)
	}

	// Verify second commit has parents[0] = op1.ID (chain spine)
	commitObj2, err := repo.CommitObject(plumbing.NewHash(op2.ID))
	if err != nil {
		t.Fatalf("CommitObject 2 failed: %v", err)
	}
	if len(commitObj2.ParentHashes) != 1 || commitObj2.ParentHashes[0].String() != op1.ID {
		t.Fatalf("commit 2 parents = %v, want [%s]", commitObj2.ParentHashes, op1.ID)
	}
}

func TestAppend_CausalParents(t *testing.T) {
	dir, repo := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Append op 1 (root on widget chain)
	env1 := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}
	op1, err := store.Append(context.Background(), env1, nil)
	if err != nil {
		t.Fatalf("Append 1 failed: %v", err)
	}

	// Append op on the waypoint chain referencing op1 as causal parent
	envWaypoint := codec.Envelope{
		ObjectID:   "wp-1",
		ObjectType: "waypoint",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"text":"hello"}`),
	}
	opWaypoint, err := store.Append(context.Background(), envWaypoint, []string{op1.ID})
	if err != nil {
		t.Fatalf("Append waypoint failed: %v", err)
	}

	// Because the waypoint chain was empty, parents[0] is the causal parent op1.ID
	if len(opWaypoint.Parents) != 1 || opWaypoint.Parents[0] != op1.ID {
		t.Fatalf("waypoint parents = %v, want [%s]", opWaypoint.Parents, op1.ID)
	}

	// Now append a second waypoint op with another causal parent
	opWaypoint2, err := store.Append(context.Background(), envWaypoint, []string{op1.ID})
	if err != nil {
		t.Fatalf("Append second waypoint failed: %v", err)
	}
	// parents[0] must be the waypoint predecessor (opWaypoint.ID), parents[1] is causal parent (op1.ID)
	if len(opWaypoint2.Parents) != 2 || opWaypoint2.Parents[0] != opWaypoint.ID || opWaypoint2.Parents[1] != op1.ID {
		t.Fatalf("waypoint2 parents = %v, want [%s, %s]", opWaypoint2.Parents, opWaypoint.ID, op1.ID)
	}

	// Test invalid causal parents:
	// 1. Non-existent hash
	_, err = store.Append(context.Background(), envWaypoint, []string{"9999999999999999999999999999999999999999"})
	if !errors.Is(err, dag.ErrInvalidParent) {
		t.Errorf("expected ErrInvalidParent, got %v", err)
	}

	// 2. Non-op commit (commit without op.json)
	nonOpHash, err := writeNonOpCommit(repo)
	if err != nil {
		t.Fatalf("writeNonOpCommit failed: %v", err)
	}
	_, err = store.Append(context.Background(), envWaypoint, []string{nonOpHash.String()})
	if !errors.Is(err, dag.ErrNonOpParent) {
		t.Errorf("expected ErrNonOpParent, got %v", err)
	}
}

func writeNonOpCommit(repo *git.Repository) (plumbing.Hash, error) {
	// Create a commit with empty tree (no op.json)
	treeObj := repo.Storer.NewEncodedObject()
	treeObj.SetType(plumbing.TreeObject)
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	commit := &object.Commit{
		Author: object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now().UTC(),
		},
		Committer: object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now().UTC(),
		},
		Message:  "non-op commit",
		TreeHash: treeHash,
	}
	commitObj := repo.Storer.NewEncodedObject()
	commitObj.SetType(plumbing.CommitObject)
	if err := commit.Encode(commitObj); err != nil {
		return plumbing.ZeroHash, err
	}
	return repo.Storer.SetEncodedObject(commitObj)
}

func TestAppend_ConcurrentRace(t *testing.T) {
	dir, repo := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	const goroutines = 10
	const opsPerGoroutine = 10
	const totalOps = goroutines * opsPerGoroutine

	var wg sync.WaitGroup
	wg.Add(goroutines)

	type appendResult struct {
		op  *codec.Op
		err error
	}
	results := make(chan appendResult, totalOps)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				env := codec.Envelope{
					ObjectID:   fmt.Sprintf("w-%d-%d", gid, i),
					ObjectType: "widget",
					OpType:     "create",
					OpVersion:  1,
					Body:       json.RawMessage(`{"title":"Initial"}`),
				}
				op, err := store.Append(context.Background(), env, nil)
				results <- appendResult{op: op, err: err}
			}
		}(g)
	}

	wg.Wait()
	close(results)

	allOps := make(map[string]bool)
	for r := range results {
		if r.err != nil {
			t.Fatalf("concurrent append error: %v", r.err)
		}
		allOps[r.op.ID] = true
	}

	if len(allOps) != totalOps {
		t.Fatalf("expected %d unique ops, got %d", totalOps, len(allOps))
	}

	// Check final chain tip
	refName := dag.LocalRefName(ident.WriterID, "widget")
	ref, err := repo.Reference(refName, true)
	if err != nil {
		t.Fatalf("Reference failed: %v", err)
	}

	// Walk from final tip down to root along parents[0]
	// Verify that ALL totalOps are reachable along the single spine!
	spineCount := 0
	currHash := ref.Hash()
	visitedOnSpine := make(map[string]bool)

	for !currHash.IsZero() {
		commitObj, err := repo.CommitObject(currHash)
		if err != nil {
			t.Fatalf("CommitObject %s failed: %v", currHash, err)
		}
		if !allOps[currHash.String()] {
			t.Fatalf("commit %s on spine was not in appended ops", currHash)
		}
		visitedOnSpine[currHash.String()] = true
		spineCount++

		if len(commitObj.ParentHashes) == 0 {
			break
		}
		currHash = commitObj.ParentHashes[0]
	}

	if spineCount != totalOps {
		t.Fatalf("expected spine length %d, got %d (some ops were lost)", totalOps, spineCount)
	}
}

func TestAppend_WithSigner(t *testing.T) {
	dir, repo := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")

	signerCalled := false
	signer := dag.SignerFunc(func(_ context.Context, payload []byte) (string, error) {
		signerCalled = true
		return "-----BEGIN SSH SIGNATURE-----\nsignature-bytes\n-----END SSH SIGNATURE-----", nil
	})

	store, err := dag.Open(dir, ident, withVocabularies(), dag.WithSigner(signer))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	env := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}

	op, err := store.Append(context.Background(), env, nil)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	if !signerCalled {
		t.Errorf("signer was not called")
	}
	if op.Signature == "" {
		t.Errorf("op.Signature is empty")
	}

	commitObj, err := repo.CommitObject(plumbing.NewHash(op.ID))
	if err != nil {
		t.Fatalf("CommitObject failed: %v", err)
	}
	if commitObj.PGPSignature == "" {
		t.Errorf("commit.PGPSignature is empty in storage")
	}
}

func TestAppend_InvalidObjectType(t *testing.T) {
	dir, _ := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	env := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "Invalid_Type!",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{}`),
	}

	_, err = store.Append(context.Background(), env, nil)
	if err == nil {
		t.Fatalf("expected error on invalid object type")
	}
}

// flakyStorer wraps a real storage.Storer and fails the first failCASCount
// calls to CheckAndSetReference with storage.ErrReferenceHasChanged before
// delegating to the real one — a deterministic way to force Append's CAS
// loop to retry a known number of times without a genuine concurrent
// writer.
type flakyStorer struct {
	storage.Storer
	failCASCount int
	casAttempts  int32
}

func (f *flakyStorer) CheckAndSetReference(new, old *plumbing.Reference) error {
	n := atomic.AddInt32(&f.casAttempts, 1)
	if int(n) <= f.failCASCount {
		return storage.ErrReferenceHasChanged
	}
	return f.Storer.CheckAndSetReference(new, old)
}

// TestAppendResolvesVocabulariesOnceDespiteCASRetries pins the WRIT-188
// requirement that dag.WithProducerVocabularies's resolver is called once
// per Append call, not once per BuildCommit — BuildCommit runs inside
// Append's CAS retry loop (append.go), so a hook invoked at the call site
// would re-resolve on every contended attempt. flakyStorer forces the loop
// to retry a fixed number of times deterministically; the resolver must
// still have been called exactly once by the time Append returns.
func TestAppendResolvesVocabulariesOnceDespiteCASRetries(t *testing.T) {
	_, repo := initTestRepo(t)
	flaky := &flakyStorer{Storer: repo.Storer, failCASCount: 3}
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")

	var resolveCalls int32
	resolve := func() (codec.Vocabularies, error) {
		atomic.AddInt32(&resolveCalls, 1)
		return testVocabularies()
	}

	store, err := dag.OpenStorage(flaky, ident, dag.WithProducerVocabularies(resolve))
	if err != nil {
		t.Fatalf("OpenStorage failed: %v", err)
	}

	env := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}

	if _, err := store.Append(context.Background(), env, nil); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	if flaky.casAttempts <= int32(flaky.failCASCount) {
		t.Fatalf("expected more than %d CAS attempts to prove retries happened, got %d", flaky.failCASCount, flaky.casAttempts)
	}
	if resolveCalls != 1 {
		t.Fatalf("resolver called %d times across %d CAS attempts, want exactly 1", resolveCalls, flaky.casAttempts)
	}
}

// TestAppendOfSchemaObjectTypeNeverConsultsTheResolver pins
// spec/schema-ops.md §7's bootstrap exception at the one place that could
// silently reintroduce a dependency on it: object_type "schema" always
// validates against the engine's built-in table, never the log, so
// Append must never even call the producer-vocabularies resolver for a
// "schema" envelope — a resolver that fails must not be able to block
// writing the very "schema" ops that could fix whatever made it fail
// (the ruling that carved out tier 4 specifically to avoid a *permanent*
// write outage would not tolerate that coupling for tier 1 either).
//
// The control case proves the assertion is not vacuous: the identical
// failing resolver, wired to the identical store, does block a
// non-"schema" append — so a regression that started calling the
// resolver for "schema" too would fail this test, not silently pass it
// because the resolver happens to always return nil.
func TestAppendOfSchemaObjectTypeNeverConsultsTheResolver(t *testing.T) {
	_, repo := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")

	wantErr := errors.New("log schema resolution failed")
	resolve := func() (codec.Vocabularies, error) {
		return nil, wantErr
	}

	store, err := dag.OpenStorage(repo.Storer, ident, dag.WithProducerVocabularies(resolve))
	if err != nil {
		t.Fatalf("OpenStorage failed: %v", err)
	}

	schemaEnv := codec.Envelope{
		ObjectID:   "sch-1",
		ObjectType: "schema",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"namespace":"acme"}`),
	}
	if _, err := store.Append(context.Background(), schemaEnv, nil); err != nil {
		t.Fatalf("Append of a \"schema\" op must not depend on the log-sourced resolver, got: %v", err)
	}

	widgetEnv := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}
	if _, err := store.Append(context.Background(), widgetEnv, nil); !errors.Is(err, wantErr) {
		t.Fatalf("expected the same failing resolver to block a non-\"schema\" append (control case), got: %v", err)
	}
}
