package projection_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

// vocabulariesFrom turns a rule index into the producer vocabulary
// dag.Store.Append consults (spec/op-envelope.md §Producer validation).
// Writ hard-codes no object type but `schema`, so an op whose type no schema
// declares is refused before it is signed: a projection test that appends
// one has to declare it first, exactly as a consumer does. Deriving the
// vocabulary from the same rule index the projection is handed keeps one
// declaration behind both.
//
// A keyed-lww rule's key columns are declared as body fields too — they are
// part of every body the rule matches, and a schema source declaring such a
// field declares its key columns alongside it (engine/store_test.go's
// approval op) — so the derivation reads them off Key/KeyTypes rather than
// making each caller restate them.
func vocabulariesFrom(rules map[string][]state.Rule) codec.Vocabularies {
	vocabularies := make(codec.Vocabularies, len(rules))
	for objectType, typeRules := range rules {
		voc := codec.Vocabulary{
			Declared:       true,
			SchemaObjectID: "sch-acme",
			OpTypes:        make(map[codec.OpVersionKey]bool),
			Fields:         make(map[codec.OpVersionKey][]spec.FieldRule),
		}
		for _, r := range typeRules {
			key := codec.OpVersionKey{OpType: r.OpType, OpVersion: r.OpVersion}
			voc.OpTypes[key] = true
			voc.Fields[key] = append(voc.Fields[key], spec.FieldRule{
				OpType:     r.OpType,
				OpVersion:  r.OpVersion,
				Field:      r.Field,
				Target:     r.Target,
				Strategy:   r.Strategy,
				Key:        r.Key,
				KeyTypes:   r.KeyTypes,
				ValueType:  r.ValueType,
				Enum:       r.Enum,
				MaxLength:  r.MaxLength,
				ObjectType: objectType,
			})
			for _, col := range r.Key {
				voc.Fields[key] = append(voc.Fields[key], spec.FieldRule{
					OpType:     r.OpType,
					OpVersion:  r.OpVersion,
					Field:      col,
					Strategy:   r.Strategy,
					ValueType:  r.KeyTypes[col],
					ObjectType: objectType,
				})
			}
		}
		vocabularies[objectType] = voc
	}
	return vocabularies
}

// withVocabularies is the option every dag store in these tests is opened
// with, spelled once.
func withVocabularies(rules map[string][]state.Rule) dag.Option {
	return dag.WithProducerVocabularies(func() (codec.Vocabularies, error) {
		return vocabulariesFrom(rules), nil
	})
}

// appendRules is what the stores createTestStore hands back accept writes
// under: testRules() plus "waypoint", a type the schema-shrink and
// undeclared-type tests append producer-valid ops of while this projection
// instance's own schema does or does not declare it. The producer
// vocabulary and the projection's rule index are resolved independently in
// a real repo, so the two differing is a state writ reaches, not a fiction.
func appendRules() map[string][]state.Rule {
	rules := testRules()
	rules["waypoint"] = []state.Rule{
		{OpType: "create", OpVersion: 1, Field: "name", Strategy: "lww", ValueType: "string", ObjectType: "waypoint"},
	}
	return rules
}

func createTestStore(t *testing.T, writerID string) (*git.Repository, *dag.Store) {
	t.Helper()
	repo, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		t.Fatalf("git.Init: %v", err)
	}

	id := identity.Identity{
		WriterID: identity.WriterID(writerID),
		Author: identity.Author{
			Name:  "Test Writer",
			Email: "writer@example.com",
		},
	}

	store, err := dag.OpenRepo(repo, id, withVocabularies(appendRules()))
	if err != nil {
		t.Fatalf("dag.OpenRepo: %v", err)
	}

	return repo, store
}

func TestIncrementalRefoldMatchesColdRebuild(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open projection failed: %v", err)
	}
	defer db.Close()

	// 1. Initial op: create widget
	env1 := makeWidgetEnv("w-1", "create", map[string]any{
		"title":       "Initial Title",
		"description": "Initial Description",
	})
	_, err = store.Append(ctx, env1, nil)
	if err != nil {
		t.Fatalf("store.Append env1 failed: %v", err)
	}

	stats1, err := db.Refresh(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh 1 failed: %v", err)
	}
	if stats1.OpsDecoded != 1 || stats1.ObjectsTouched != 1 || stats1.Rebuilt {
		t.Fatalf("unexpected stats1: %+v", stats1)
	}
	if len(stats1.Changed) != 1 {
		t.Fatalf("expected 1 changed object in stats1, got %d", len(stats1.Changed))
	}
	expected1 := projection.ObjectChange{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpTypes:    []string{"create"},
		Created:    true,
	}
	if !reflect.DeepEqual(stats1.Changed[0], expected1) {
		t.Fatalf("unexpected stats1.Changed: %+v, want %+v", stats1.Changed[0], expected1)
	}

	var title, desc string
	err = db.DB().QueryRow("SELECT f_title, f_description FROM o_widget WHERE object_id = 'w-1'").Scan(&title, &desc)
	if err != nil {
		t.Fatalf("query widget failed: %v", err)
	}
	if title != "Initial Title" || desc != "Initial Description" {
		t.Fatalf("unexpected widget fields: title=%q desc=%q", title, desc)
	}

	// 2. Incremental op: update title and add revision
	env2 := makeWidgetEnv("w-1", "update", map[string]any{
		"title": "Updated Title",
	})
	_, err = store.Append(ctx, env2, nil)
	if err != nil {
		t.Fatalf("store.Append env2 failed: %v", err)
	}

	env3 := makeWidgetEnv("w-1", "revision", map[string]any{
		"base": "0000000000000000000000000000000000000001",
		"head": "0000000000000000000000000000000000000002",
	})
	_, err = store.Append(ctx, env3, nil)
	if err != nil {
		t.Fatalf("store.Append env3 failed: %v", err)
	}

	stats2, err := db.Refresh(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh 2 failed: %v", err)
	}
	if stats2.OpsDecoded != 2 || stats2.ObjectsTouched != 1 || stats2.Rebuilt {
		t.Fatalf("unexpected stats2: %+v", stats2)
	}
	if len(stats2.Changed) != 1 {
		t.Fatalf("expected 1 changed object in stats2, got %d", len(stats2.Changed))
	}
	expected2 := projection.ObjectChange{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpTypes:    []string{"revision", "update"},
		Created:    false,
	}
	if !reflect.DeepEqual(stats2.Changed[0], expected2) {
		t.Fatalf("unexpected stats2.Changed: %+v, want %+v", stats2.Changed[0], expected2)
	}

	incrementalDump, err := db.DumpTables()
	if err != nil {
		t.Fatalf("DumpTables incremental failed: %v", err)
	}

	// 3. Cold rebuild comparison
	statsCold, err := db.Rebuild(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Rebuild failed: %v", err)
	}
	if !statsCold.Rebuilt || statsCold.ObjectsTouched != 1 || len(statsCold.Changed) != 0 {
		t.Fatalf("unexpected statsCold: %+v", statsCold)
	}

	coldDump, err := db.DumpTables()
	if err != nil {
		t.Fatalf("DumpTables cold failed: %v", err)
	}

	if !reflect.DeepEqual(incrementalDump, coldDump) {
		t.Fatalf("incremental dump != cold dump:\nincremental: %+v\ncold: %+v", incrementalDump, coldDump)
	}
}

func TestNewWriterNamespaceDetected(t *testing.T) {
	ctx := context.Background()
	repo, storeA := createTestStore(t, "0123456789abcdef")

	storeB, err := dag.OpenRepo(repo, identity.Identity{
		WriterID: identity.WriterID("fedcba9876543210"),
		Author: identity.Author{
			Name:  "Writer B",
			Email: "writerB@example.com",
		},
	}, withVocabularies(appendRules()))
	if err != nil {
		t.Fatalf("dag.OpenRepo storeB: %v", err)
	}

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open projection failed: %v", err)
	}
	defer db.Close()

	// Writer A creates widget
	envA := makeWidgetEnv("w-multi", "create", map[string]any{
		"title": "Title From Writer A",
	})
	_, err = storeA.Append(ctx, envA, nil)
	if err != nil {
		t.Fatalf("storeA.Append: %v", err)
	}

	stats1, err := db.Refresh(storeA, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh 1: %v", err)
	}
	if stats1.ObjectsTouched != 1 {
		t.Fatalf("expected 1 object touched, got %d", stats1.ObjectsTouched)
	}

	// Writer B appends an endorsement on w-multi
	envB := makeWidgetEnv("w-multi", "endorse", map[string]any{
		"subject":  "email:writerb@example.com",
		"revision": "0000000000000000000000000000000000000001",
		"verdict":  "yes",
	})
	_, err = storeB.Append(ctx, envB, nil)
	if err != nil {
		t.Fatalf("storeB.Append: %v", err)
	}

	// Refresh should discover Writer B's new chain with no stored cursor
	stats2, err := db.Refresh(storeA, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh 2: %v", err)
	}
	if stats2.OpsDecoded != 1 || stats2.ObjectsTouched != 1 || stats2.Rebuilt {
		t.Fatalf("unexpected stats2: %+v", stats2)
	}

	var verdict string
	err = db.DB().QueryRow("SELECT f_verdict FROM o_widget__k_subject_revision WHERE object_id = 'w-multi' AND k_subject = 'email:writerb@example.com'").Scan(&verdict)
	if err != nil {
		t.Fatalf("query endorsement failed: %v", err)
	}
	if verdict != "yes" {
		t.Fatalf("expected verdict 'yes', got %q", verdict)
	}

	// Verify equal to cold rebuild
	incDump, _ := db.DumpTables()
	_, _ = db.Rebuild(storeA, projection.WithSchema(testRules()))
	coldDump, _ := db.DumpTables()

	if !reflect.DeepEqual(incDump, coldDump) {
		t.Fatalf("new writer incremental dump differs from cold dump")
	}
}

func TestRollbackTriggersRebuild(t *testing.T) {
	ctx := context.Background()
	repo, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open projection failed: %v", err)
	}
	defer db.Close()

	// Append 2 ops
	env1 := makeWidgetEnv("w-rb", "create", map[string]any{"title": "Title 1"})
	_, _ = store.Append(ctx, env1, nil)
	env2 := makeWidgetEnv("w-rb", "update", map[string]any{"title": "Title 2"})
	_, _ = store.Append(ctx, env2, nil)

	_, err = db.Refresh(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh before rewind failed: %v", err)
	}

	// Now rewind the ref by creating a brand new commit not in ancestry and pointing ref at it
	envDivergent := makeWidgetEnv("w-rb", "create", map[string]any{"title": "Divergent Title"})
	c := codec.Commit{
		Author: codec.Identity{
			Name:  "Test Writer",
			Email: "writer@example.com",
			When:  time.Unix(1700000000, 0).UTC(),
		},
		Committer: codec.Identity{
			Name:  "Test Writer",
			Email: "writer@example.com",
			When:  time.Unix(1700000000, 0).UTC(),
		},
		Message: "divergent commit",
		Tree: []codec.TreeEntry{
			{
				Name: "op.json",
				Mode: "100644",
				Data: envDivergent.Raw,
			},
		},
	}
	h, err := codec.WriteCommit(ctx, repo.Storer, &c, nil)
	if err != nil {
		t.Fatalf("WriteCommit: %v", err)
	}

	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	err = repo.Storer.SetReference(plumbing.NewReferenceFromStrings(refName.String(), h.String()))
	if err != nil {
		t.Fatalf("force-set reference: %v", err)
	}

	// Refresh should detect rollback (previous tip is not ancestor of new tip) and rebuild
	stats, err := db.Refresh(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh after rewind failed: %v", err)
	}
	if !stats.Rebuilt {
		t.Fatalf("expected Rebuilt=true after rewind, got false")
	}

	var title string
	err = db.DB().QueryRow("SELECT f_title FROM o_widget WHERE object_id = 'w-rb'").Scan(&title)
	if err != nil {
		t.Fatalf("query title: %v", err)
	}
	if title != "Divergent Title" {
		t.Fatalf("expected title 'Divergent Title', got %q", title)
	}
}

func TestDisappearedChainTriggersRebuild(t *testing.T) {
	ctx := context.Background()
	repo, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open projection failed: %v", err)
	}
	defer db.Close()

	env1 := makeWidgetEnv("w-del", "create", map[string]any{"title": "Title Del"})
	_, _ = store.Append(ctx, env1, nil)

	_, err = db.Refresh(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// Delete the chain ref
	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	err = repo.Storer.RemoveReference(refName)
	if err != nil {
		t.Fatalf("RemoveReference: %v", err)
	}

	// Refresh should detect chain disappeared and rebuild
	stats, err := db.Refresh(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh after ref delete failed: %v", err)
	}
	if !stats.Rebuilt {
		t.Fatalf("expected Rebuilt=true after ref delete, got false")
	}

	var count int
	_ = db.DB().QueryRow("SELECT COUNT(*) FROM objects").Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 objects after all refs deleted, got %d", count)
	}
}

func TestRefresh_WithTargetRefsResolution(t *testing.T) {
	ctx := context.Background()
	repo, store := createTestStore(t, "0123456789abcdef")

	// Create a dummy commit for a branch
	testCommitHash := plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")
	_ = repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), testCommitHash))
	_ = repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/remotes/origin/feat"), testCommitHash))
	_ = repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/v1.0"), testCommitHash))

	// Create an annotated tag pointing to testCommitHash
	tagObj := &object.Tag{
		Name: "v2.0",
		Tagger: object.Signature{
			Name:  "Test Tagger",
			Email: "tagger@example.com",
			When:  time.Now().UTC(),
		},
		Message:    "Release 2.0",
		TargetType: plumbing.CommitObject,
		Target:     testCommitHash,
	}
	tagEncoded := repo.Storer.NewEncodedObject()
	if err := tagObj.Encode(tagEncoded); err != nil {
		t.Fatal(err)
	}
	tagHash, err := repo.Storer.SetEncodedObject(tagEncoded)
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/v2.0"), tagHash))

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open projection failed: %v", err)
	}
	defer db.Close()

	env := makeWidgetEnv("w-target", "create", map[string]any{"title": "Target Ref Test"})
	_, _ = store.Append(ctx, env, nil)

	// Refresh with short branch name "main", remote branch "origin/feat", lightweight tag "v1.0", and annotated tag "v2.0"
	_, err = db.Refresh(store, projection.WithSchema(testRules()), projection.WithTargetRefs("main", "origin/feat", "v1.0", "v2.0"))
	if err != nil {
		t.Fatalf("Refresh with target refs failed: %v", err)
	}

	rows, err := db.DB().Query("SELECT ref_name, tip FROM code_tips ORDER BY ref_name")
	if err != nil {
		t.Fatalf("Query code_tips failed: %v", err)
	}
	defer rows.Close()

	tips := make(map[string]string)
	for rows.Next() {
		var refName, tip string
		if err := rows.Scan(&refName, &tip); err != nil {
			t.Fatal(err)
		}
		tips[refName] = tip
	}

	if tips["main"] != testCommitHash.String() {
		t.Errorf("code_tips[main] = %s, want %s", tips["main"], testCommitHash.String())
	}
	if tips["origin/feat"] != testCommitHash.String() {
		t.Errorf("code_tips[origin/feat] = %s, want %s", tips["origin/feat"], testCommitHash.String())
	}
	if tips["v1.0"] != testCommitHash.String() {
		t.Errorf("code_tips[v1.0] = %s, want %s", tips["v1.0"], testCommitHash.String())
	}
	if tips["v2.0"] != testCommitHash.String() {
		t.Errorf("code_tips[v2.0] = %s, want %s (annotated tag should peel to commit)", tips["v2.0"], testCommitHash.String())
	}
}

// gitrevisions resolves a bare name against refs/tags before refs/heads.
func TestRefresh_TargetRefTagBeatsBranch(t *testing.T) {
	ctx := context.Background()
	repo, store := createTestStore(t, "0123456789abcdef")

	branchCommit := plumbing.NewHash("1111111111111111111111111111111111111111")
	tagCommit := plumbing.NewHash("2222222222222222222222222222222222222222")
	_ = repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/release", branchCommit))
	_ = repo.Storer.SetReference(plumbing.NewHashReference("refs/tags/release", tagCommit))

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open projection failed: %v", err)
	}
	defer db.Close()

	env := makeWidgetEnv("w-precedence", "create", map[string]any{"title": "Precedence"})
	_, _ = store.Append(ctx, env, nil)

	if _, err := db.Refresh(store, projection.WithSchema(testRules()), projection.WithTargetRefs("release")); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	var tip string
	if err := db.DB().QueryRow("SELECT tip FROM code_tips WHERE ref_name = ?", "release").Scan(&tip); err != nil {
		t.Fatalf("query code_tips: %v", err)
	}
	if tip != tagCommit.String() {
		t.Errorf("code_tips[release] = %s, want tag %s", tip, tagCommit.String())
	}
}

func TestRefreshIncrementalEmptyObjectType(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open projection failed: %v", err)
	}
	defer db.Close()

	// 1. Initial op: create widget
	env1 := makeWidgetEnv("w-empty-type", "create", map[string]any{
		"title": "Initial Title",
	})
	op1, err := store.Append(ctx, env1, nil)
	if err != nil {
		t.Fatalf("store.Append env1 failed: %v", err)
	}

	stats1, err := db.Refresh(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh 1 failed: %v", err)
	}
	if len(stats1.Changed) != 1 || stats1.Changed[0].ObjectType != "widget" {
		t.Fatalf("unexpected stats1: %+v", stats1)
	}

	// 2. Incremental delta op for w-empty-type where ObjectType is omitted/empty
	payload := []byte(`{"body":{"title":"Updated Title"},"object_id":"w-empty-type","object_type":"widget","op_type":"update","op_version":1}`)
	op2 := codec.Op{
		ID: "op-update-2",
		Envelope: codec.Envelope{
			ObjectID:   "w-empty-type",
			ObjectType: "", // omitted / empty in delta batch
			OpType:     "update",
			OpVersion:  1,
			Body:       []byte(`{"title":"Updated Title"}`),
			Raw:        payload,
		},
		Parents: []string{op1.ID},
		Author: codec.Identity{
			Name:  "Test Writer",
			Email: "writer@example.com",
			When:  time.Unix(1700000001, 0).UTC(),
		},
		Committer: codec.Identity{
			Name:  "Test Writer",
			Email: "writer@example.com",
			When:  time.Unix(1700000001, 0).UTC(),
		},
		Message: "writ: update widget/w-empty-type\n",
	}

	deltaEnum := &dag.EnumerateResult{
		Ops: map[string][]codec.Op{
			"w-empty-type": {op2},
		},
		Cursors: dag.CursorSet{
			"refs/writ/0123456789abcdef/widget": "op-update-2",
		},
		DecodedCommits: 1,
	}

	stats2, err := db.Refresh(store, projection.WithSchema(testRules()), projection.WithEnumOverrideForTest(deltaEnum))
	if err != nil {
		t.Fatalf("Refresh 2 failed: %v", err)
	}
	if len(stats2.Changed) != 1 {
		t.Fatalf("expected 1 changed object in stats2, got %d", len(stats2.Changed))
	}
	if stats2.Changed[0].ObjectType != "widget" {
		t.Errorf("stats2.Changed[0].ObjectType = %q, want %q", stats2.Changed[0].ObjectType, "widget")
	}
	if stats2.Changed[0].Created {
		t.Errorf("stats2.Changed[0].Created = true, want false")
	}
}

func TestDetermineObjectTypePrecedence(t *testing.T) {
	// 1. Create op beats other ops even when not first
	ops := []codec.Op{
		{Envelope: codec.Envelope{ObjectType: "widget", OpType: "update"}},
		{Envelope: codec.Envelope{ObjectType: "gadget", OpType: "create"}},
	}
	if got := projection.DetermineObjectType(ops); got != "gadget" {
		t.Errorf("got %q, want 'gadget'", got)
	}

	// 2. First non-empty ObjectType when no create op
	ops2 := []codec.Op{
		{Envelope: codec.Envelope{ObjectType: "", OpType: "update"}},
		{Envelope: codec.Envelope{ObjectType: "widget", OpType: "update"}},
	}
	if got := projection.DetermineObjectType(ops2); got != "widget" {
		t.Errorf("got %q, want 'widget'", got)
	}

	// 3. Fallback when all empty
	ops3 := []codec.Op{
		{Envelope: codec.Envelope{ObjectType: "", OpType: "update"}},
	}
	if got := projection.DetermineObjectType(ops3); got != "" {
		t.Errorf("got %q, want ''", got)
	}
}

// TestCollidingLogDeclaredTypeStaysOpenable is WRIT-189 round 1's MAJOR-3
// finding, exercised at the level Store.Open actually calls: a log-declared
// object type ("widget--base-head") whose generated table name collides
// with widget's own base/head append-group table
// ("o_widget__base_head" — round 2 MAJOR-1 folded the separate "base" and
// "head" child tables into this one shared table, so that is what a
// collision has to target now) used to fail buildDescriptor with a hard
// error, which propagated all the way
// through ApplySchema and would have made writ.Open fail forever — data one
// writer wrote (a legal object type name under op-envelope's grammar)
// bricking the whole repository for every writer, with nothing removable
// from the log to fix it. Refresh (and so ApplySchema) must instead
// withhold only the colliding type's tables: its ops fall to unknown_ops,
// and widget's own tables — including the one it collided with — are
// unaffected.
func TestCollidingLogDeclaredTypeStaysOpenable(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	// The ordinary store.Append path enforces producer validation against
	// the vocabulary the store was opened with (appendRules, here), which
	// would refuse to write an op for "widget--base-head" long before it
	// ever reached buildDescriptor —
	// that gate is orthogonal to this finding (a real repo reaches
	// buildDescriptor with such an op only once a log-declared schema
	// object has already made it writable). So, exactly like
	// TestRefreshIncrementalEmptyObjectType, this constructs the colliding
	// op directly and hands it to Refresh via WithEnumOverrideForTest,
	// which is the same shape store.EnumerateSince itself would have
	// produced, with no producer-validation gate to route around.
	widgetEnv := makeWidgetEnv("w-1", "create", map[string]any{"title": "T"})
	widgetOp, err := store.Append(ctx, widgetEnv, nil)
	if err != nil {
		t.Fatalf("store.Append widget failed: %v", err)
	}

	stats1, err := db.Refresh(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh 1 failed: %v", err)
	}
	if len(stats1.Changed) != 1 || stats1.Changed[0].ObjectType != "widget" {
		t.Fatalf("unexpected stats1: %+v", stats1)
	}

	payload := []byte(`{"body":{"title":"colliding type"},"object_id":"collider-1","object_type":"widget--base-head","op_type":"create","op_version":1}`)
	collidingOp := codec.Op{
		ID: "op-collider-1",
		Envelope: codec.Envelope{
			ObjectID:   "collider-1",
			ObjectType: "widget--base-head",
			OpType:     "create",
			OpVersion:  1,
			Body:       []byte(`{"title":"colliding type"}`),
			Raw:        payload,
		},
		Author: codec.Identity{
			Name:  "Test Writer",
			Email: "writer@example.com",
			When:  time.Unix(1700000002, 0).UTC(),
		},
		Committer: codec.Identity{
			Name:  "Test Writer",
			Email: "writer@example.com",
			When:  time.Unix(1700000002, 0).UTC(),
		},
		Message: "writ: create widget--base-head/collider-1\n",
	}

	deltaEnum := &dag.EnumerateResult{
		Ops: map[string][]codec.Op{
			"collider-1": {collidingOp},
		},
		Cursors: dag.CursorSet{
			"refs/writ/0123456789abcdef/widget":            widgetOp.ID,
			"refs/writ/0123456789abcdef/widget--base-head": "op-collider-1",
		},
		DecodedCommits: 1,
	}

	rules := testRules()
	rules["widget--base-head"] = []state.Rule{
		{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget--base-head"},
	}

	// The regression: this call used to return a "generated table name...
	// is not unique" error, which is exactly what would have made
	// writ.Open fail on every subsequent attempt to open this repository.
	if _, err := db.Refresh(store, projection.WithSchema(rules), projection.WithEnumOverrideForTest(deltaEnum)); err != nil {
		t.Fatalf("Refresh with colliding log-declared type failed: %v", err)
	}

	var widgetTitle string
	if err := db.DB().QueryRow("SELECT f_title FROM o_widget WHERE object_id = 'w-1'").Scan(&widgetTitle); err != nil {
		t.Fatalf("query o_widget failed: %v", err)
	}
	if widgetTitle != "T" {
		t.Fatalf("o_widget.f_title = %q, want %q", widgetTitle, "T")
	}

	var unknownCount int
	if err := db.DB().QueryRow(
		"SELECT COUNT(*) FROM unknown_ops WHERE object_id = 'collider-1' AND object_type = 'widget--base-head'",
	).Scan(&unknownCount); err != nil {
		t.Fatalf("query unknown_ops failed: %v", err)
	}
	if unknownCount != 1 {
		t.Fatalf("expected the colliding type's op in unknown_ops, got count=%d", unknownCount)
	}
}

// TestFirstApplySchemaAfterSchemaLessRefreshRebuilds is WRIT-189 round 1's
// MEDIUM-4 finding: Refresh(store) with no schema at all folds every op to
// unknown_ops for lack of any installed rules, exactly like an absent
// schema always has. A later Refresh(store, WithSchema(rules)) is then this
// cache's first-ever ApplySchema (no prior digest recorded), which used to
// skip needs_rebuild unconditionally on that basis — leaving the
// incremental path to see the object's chain tip already at HEAD and so
// re-fold nothing, permanently stranding it in unknown_ops with its
// generated table forever empty. A first-ever ApplySchema must still force
// a rebuild when the cache already holds data folded before any schema
// existed.
func TestFirstApplySchemaAfterSchemaLessRefreshRebuilds(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	env := makeWidgetEnv("w-1", "create", map[string]any{"title": "T"})
	if _, err := store.Append(ctx, env, nil); err != nil {
		t.Fatalf("store.Append failed: %v", err)
	}

	// 1. Schema-less refresh: no installed rules, so the widget falls to
	// unknown_ops exactly like the absent-schema path always has.
	if _, err := db.Refresh(store); err != nil {
		t.Fatalf("Refresh (no schema) failed: %v", err)
	}
	var unknownBefore int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM unknown_ops WHERE object_id = 'w-1'").Scan(&unknownBefore); err != nil {
		t.Fatalf("query unknown_ops failed: %v", err)
	}
	if unknownBefore != 1 {
		t.Fatalf("expected w-1 in unknown_ops before any schema, got count=%d", unknownBefore)
	}

	// 2. First-ever ApplySchema on this cache. The regression: this used to
	// leave needs_rebuild unset, so the incremental path below would see no
	// delta (the chain tip was already recorded) and never re-fold w-1.
	stats, err := db.Refresh(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh (with schema) failed: %v", err)
	}
	if !stats.Rebuilt {
		t.Fatalf("expected the first ApplySchema over already-materialized data to force a rebuild, got Rebuilt=false")
	}

	var title string
	if err := db.DB().QueryRow("SELECT f_title FROM o_widget WHERE object_id = 'w-1'").Scan(&title); err != nil {
		t.Fatalf("query o_widget failed (w-1 never got materialized): %v", err)
	}
	if title != "T" {
		t.Fatalf("o_widget.f_title = %q, want %q", title, "T")
	}

	var unknownAfter int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM unknown_ops WHERE object_id = 'w-1'").Scan(&unknownAfter); err != nil {
		t.Fatalf("query unknown_ops failed: %v", err)
	}
	if unknownAfter != 0 {
		t.Fatalf("expected w-1 no longer in unknown_ops once widget has installed rules, got count=%d", unknownAfter)
	}
}
