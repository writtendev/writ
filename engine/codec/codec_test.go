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
		ObjectID:   "rev-123",
		ObjectType: "review",
		OpType:     "create",
		OpVersion:  1,
	}

	got := codec.Message(env)
	want := "writ: create review/rev-123\n"
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
		ObjectID:   "rev-1",
		ObjectType: "review",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}

	parents := []string{"parent1", "parent2"}
	commit, err := codec.BuildCommit(env, author, parents, nil)
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

	if commit.Message != "writ: create review/rev-1\n" {
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

	if op.ObjectID != "rev-1" || op.ObjectType != "review" || op.OpType != "create" {
		t.Errorf("unexpected op fields: %+v", op)
	}
}

func TestDecodeCommitRejections(t *testing.T) {
	validRaw := []byte(`{"body":{},"object_id":"r1","object_type":"review","op_type":"create","op_version":1}`)
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

func TestValidateBody(t *testing.T) {
	t.Run("valid review op body", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "rev-1",
			ObjectType: "review",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Add feature"}`),
		}
		if err := codec.ValidateBody(env, nil); err != nil {
			t.Errorf("ValidateBody failed on valid review op: %v", err)
		}
	})

	t.Run("invalid review op body", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "rev-1",
			ObjectType: "review",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"description":"Missing title"}`),
		}
		if err := codec.ValidateBody(env, nil); err == nil {
			t.Errorf("ValidateBody accepted invalid review op body missing title")
		}
	})

	t.Run("valid comment op body", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "c-1",
			ObjectType: "comment",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"subject":{"object_type":"review","object_id":"r-1"},"text":"Looks good"}`),
		}
		if err := codec.ValidateBody(env, nil); err != nil {
			t.Errorf("ValidateBody failed on valid comment op: %v", err)
		}
	})

	t.Run("invalid comment op body", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "c-1",
			ObjectType: "comment",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"text":"Missing subject"}`),
		}
		if err := codec.ValidateBody(env, nil); err == nil {
			t.Errorf("ValidateBody accepted invalid comment op body missing subject")
		}
	})

	// Tier 5 (spec/op-envelope.md §Producer validation): with nil
	// vocabularies (the log declares nothing) and no embedded vocabulary
	// for this object type, ValidateBody now refuses rather than passing
	// it through — the inversion TestBuildCommitRefusesUndeclaredObjectTypes
	// pins for BuildCommit.
	t.Run("undeclared object type is refused", func(t *testing.T) {
		env := codec.Envelope{
			ObjectID:   "unknown-1",
			ObjectType: "custom_type",
			OpType:     "custom_action",
			OpVersion:  1,
			Body:       json.RawMessage(`{"any":"field"}`),
		}
		if err := codec.ValidateBody(env, nil); err == nil {
			t.Errorf("ValidateBody accepted an object_type declared by no schema and embedded by no vocabulary")
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
		ObjectID:   "rev-1",
		ObjectType: "review",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}

	c, err := codec.BuildCommit(env, author, []string{}, nil)
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
