package codec_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/writtendev/writ/engine/codec"
)

func TestMessage(t *testing.T) {
	env := codec.Envelope{
		ObjectID:   "w-123",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
	}

	got := codec.Message(env)
	want := "writ: create widget/w-123\n"
	if got != want {
		t.Errorf("Message() = %q, want %q", got, want)
	}
}

func TestBuildCommit(t *testing.T) {
	when := time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("PST", -8*3600))
	author := codec.Identity{
		Name:  "Alice",
		Email: "alice@example.com",
		When:  when,
	}

	env := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}

	parents := []string{"parent1", "parent2"}
	commit, err := codec.BuildCommit(env, author, parents, widgetVocabulary())
	if err != nil {
		t.Fatalf("BuildCommit failed: %v", err)
	}

	if len(commit.Parents) != 2 || commit.Parents[0] != "parent1" || commit.Parents[1] != "parent2" {
		t.Errorf("unexpected parents: %v", commit.Parents)
	}

	// Committer must be byte-identical to author
	if commit.Author != commit.Committer {
		t.Errorf("committer != author: %+v vs %+v", commit.Committer, commit.Author)
	}

	// UTC offset must be +0000 (time.UTC)
	if commit.Author.When.Location() != time.UTC {
		t.Errorf("author time location is not UTC: %v", commit.Author.When.Location())
	}

	if commit.Message != "writ: create widget/w-1\n" {
		t.Errorf("unexpected message: %q", commit.Message)
	}

	if len(commit.Tree) != 1 {
		t.Fatalf("tree entries length = %d, want 1", len(commit.Tree))
	}

	entry := commit.Tree[0]
	if entry.Name != "op.json" || entry.Mode != "100644" {
		t.Errorf("unexpected tree entry: %+v", entry)
	}

	// Must successfully decode via DecodeCommit
	op, err := codec.DecodeCommit(*commit)
	if err != nil {
		t.Fatalf("DecodeCommit failed on built commit: %v", err)
	}

	if op.ObjectID != "w-1" || op.ObjectType != "widget" || op.OpType != "create" {
		t.Errorf("unexpected op fields: %+v", op)
	}
}

func TestDecodeCommitRejections(t *testing.T) {
	validRaw := []byte(`{"body":{},"object_id":"w1","object_type":"widget","op_type":"create","op_version":1}`)
	now := time.Now().UTC()
	alice := codec.Identity{Name: "Alice", Email: "alice@example.com", When: now}
	bob := codec.Identity{Name: "Bob", Email: "bob@example.com", When: now}

	t.Run("missing op.json", func(t *testing.T) {
		c := codec.Commit{
			Author:    alice,
			Committer: alice,
			Tree: []codec.TreeEntry{
				{Name: "other.txt", Mode: "100644", Data: []byte("hello")},
			},
		}
		_, err := codec.DecodeCommit(c)
		var rej *codec.RejectError
		if !errors.As(err, &rej) || rej.Reason != codec.RejectMissingOpJSON {
			t.Fatalf("got %v, want RejectMissingOpJSON", err)
		}
	})

	t.Run("extra tree entry", func(t *testing.T) {
		c := codec.Commit{
			Author:    alice,
			Committer: alice,
			Tree: []codec.TreeEntry{
				{Name: "op.json", Mode: "100644", Data: validRaw},
				{Name: "extra.txt", Mode: "100644", Data: []byte("extra")},
			},
		}
		_, err := codec.DecodeCommit(c)
		var rej *codec.RejectError
		if !errors.As(err, &rej) || rej.Reason != codec.RejectExtraTreeEntry {
			t.Fatalf("got %v, want RejectExtraTreeEntry", err)
		}
	})

	t.Run("op.json in subdirectory", func(t *testing.T) {
		c := codec.Commit{
			Author:    alice,
			Committer: alice,
			Tree: []codec.TreeEntry{
				{
					Name: "subdir",
					Mode: "040000",
					Entries: []codec.TreeEntry{
						{Name: "op.json", Mode: "100644", Data: validRaw},
					},
				},
			},
		}
		_, err := codec.DecodeCommit(c)
		var rej *codec.RejectError
		if !errors.As(err, &rej) || rej.Reason != codec.RejectOpJSONSubdirectory {
			t.Fatalf("got %v, want RejectOpJSONSubdirectory", err)
		}
	})

	t.Run("invalid op.json mode", func(t *testing.T) {
		c := codec.Commit{
			Author:    alice,
			Committer: alice,
			Tree: []codec.TreeEntry{
				{Name: "op.json", Mode: "100755", Data: validRaw},
			},
		}
		_, err := codec.DecodeCommit(c)
		var rej *codec.RejectError
		if !errors.As(err, &rej) || rej.Reason != codec.RejectInvalidOpJSONMode {
			t.Fatalf("got %v, want RejectInvalidOpJSONMode", err)
		}
	})

	t.Run("committer mismatch name", func(t *testing.T) {
		c := codec.Commit{
			Author:    alice,
			Committer: bob,
			Tree: []codec.TreeEntry{
				{Name: "op.json", Mode: "100644", Data: validRaw},
			},
		}
		_, err := codec.DecodeCommit(c)
		var rej *codec.RejectError
		if !errors.As(err, &rej) || rej.Reason != codec.RejectCommitterMismatch {
			t.Fatalf("got %v, want RejectCommitterMismatch", err)
		}
	})

	t.Run("committer mismatch timestamp", func(t *testing.T) {
		diffTime := alice
		diffTime.When = now.Add(time.Minute)
		c := codec.Commit{
			Author:    alice,
			Committer: diffTime,
			Tree: []codec.TreeEntry{
				{Name: "op.json", Mode: "100644", Data: validRaw},
			},
		}
		_, err := codec.DecodeCommit(c)
		var rej *codec.RejectError
		if !errors.As(err, &rej) || rej.Reason != codec.RejectCommitterMismatch {
			t.Fatalf("got %v, want RejectCommitterMismatch", err)
		}
	})
}

// TestValidateBody walks the tiers of spec/op-envelope.md §Producer
// validation from the outside: the bootstrap vocabulary writ embeds for
// `schema` (tier 1), a log-declared one (tier 2, where rules 3, 4, 5, and
// 6 are checked against the declaration rather than a JSON Schema), and the
// absence of both (tier 4, a refusal naming object_type). Only rules 3 and
// 4 are walked below — widgetVocabulary() has no keyed-lww rule for rule 5
// to bind to, and no subtest sends a null body value for rule 6; those two
// are pinned instead in producer_test.go's
// TestBuildCommitRejectsKeyedLWWKeyColumns and
// TestBuildCommitRejectsDeclaredFieldNullValue.
func TestValidateBody(t *testing.T) {
	t.Run("valid schema op body", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "sch-1",
			ObjectType: "schema",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"namespace":"acme"}`),
		}
		if err := codec.ValidateBody(env, nil); err != nil {
			t.Errorf("ValidateBody failed on valid schema op: %v", err)
		}
	})

	t.Run("invalid schema op body", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "sch-1",
			ObjectType: "schema",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"description":"Missing namespace"}`),
		}
		if err := codec.ValidateBody(env, nil); err == nil {
			t.Errorf("ValidateBody accepted a schema create body missing namespace")
		}
	})

	t.Run("valid log-declared op body", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "w-1",
			ObjectType: "widget",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Add feature"}`),
		}
		if err := codec.ValidateBody(env, widgetVocabulary()); err != nil {
			t.Errorf("ValidateBody failed on an op the log's schema declares: %v", err)
		}
	})

	// Rule 3 at tier 2: the declared rules are the only thing bounding
	// which fields are known, so a field none of them names is refused.
	t.Run("undeclared field is refused", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "w-1",
			ObjectType: "widget",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"headline":"no rule declares this"}`),
		}
		if err := codec.ValidateBody(env, widgetVocabulary()); err == nil {
			t.Errorf("ValidateBody accepted a body field the log's schema does not declare")
		}
	})

	// Rule 4 at tier 2: the op_type and op_version have to be ones the
	// declaring schema object names.
	t.Run("undeclared op type is refused", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "w-1",
			ObjectType: "widget",
			OpType:     "annotate",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Add feature"}`),
		}
		if err := codec.ValidateBody(env, widgetVocabulary()); err == nil {
			t.Errorf("ValidateBody accepted an op_type the log's schema does not declare")
		}
	})

	t.Run("undeclared op version is refused", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "w-1",
			ObjectType: "widget",
			OpType:     "create",
			OpVersion:  2,
			Body:       json.RawMessage(`{"title":"Add feature"}`),
		}
		if err := codec.ValidateBody(env, widgetVocabulary()); err == nil {
			t.Errorf("ValidateBody accepted an op_version the log's schema does not declare")
		}
	})

	// Tier 4 (spec/op-envelope.md §Producer validation): with nil
	// vocabularies the log declares nothing, and `schema` aside there is no
	// embedded vocabulary to fall back on, so ValidateBody refuses rather
	// than passing it through — the inversion
	// TestBuildCommitRefusesUndeclaredObjectTypes pins for BuildCommit.
	t.Run("undeclared object type is refused", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "unknown-1",
			ObjectType: "custom-type",
			OpType:     "custom-action",
			OpVersion:  1,
			Body:       json.RawMessage(`{"any":"field"}`),
		}
		if err := codec.ValidateBody(env, nil); err == nil {
			t.Errorf("ValidateBody accepted an object_type no schema in the log declares")
		}
	})
}

func TestFromGitCommitNil(t *testing.T) {
	_, err := codec.FromGitCommit(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "nil git commit") {
		t.Fatalf("expected nil git commit error, got %v", err)
	}
}

func TestWriteCommitRoundTrip(t *testing.T) {
	s := memory.NewStorage()
	when := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	author := codec.Identity{
		Name:  "Alice",
		Email: "alice@example.com",
		When:  when,
	}

	env := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}

	c, err := codec.BuildCommit(env, author, []string{}, widgetVocabulary())
	if err != nil {
		t.Fatalf("BuildCommit failed: %v", err)
	}

	signerCalled := false
	signer := codec.SignerFunc(func(_ context.Context, payload []byte) (string, error) {
		signerCalled = true
		return "-----BEGIN SSH SIGNATURE-----\nsig\n-----END SSH SIGNATURE-----", nil
	})

	hash, err := codec.WriteCommit(context.Background(), s, c, signer)
	if err != nil {
		t.Fatalf("WriteCommit failed: %v", err)
	}
	if !signerCalled {
		t.Errorf("expected signer to be called")
	}
	if hash.IsZero() || hash.String() != c.ID {
		t.Errorf("commit hash mismatch: %v vs %v", hash, c.ID)
	}

	// Verify go-git commit object stored
	gitCommit, err := object.GetCommit(s, hash)
	if err != nil {
		t.Fatalf("GetCommit failed: %v", err)
	}
	if gitCommit.Author.Name != "Alice" || gitCommit.PGPSignature == "" {
		t.Errorf("unexpected git commit: %+v", gitCommit)
	}
}
