package state_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	s "github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

func TestDocumentRulesDriftGuard(t *testing.T) {
	allRules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	var expectedRules []s.Rule
	for _, r := range allRules {
		if r.Vocabulary == "document" {
			expectedRules = append(expectedRules, s.Rule{
				OpType:    r.OpType,
				OpVersion: r.OpVersion,
				Field:     r.Field,
				Target:    r.Target,
				Strategy:  r.Strategy,
				Key:       r.Key,
				Lattice:   r.Lattice,
				ValueType: r.ValueType,
				Enum:      r.Enum,
				MaxLength: r.MaxLength,
				KeyTypes:  r.KeyTypes,
			})
		}
	}

	builtIn := s.DocumentRules()
	if !reflect.DeepEqual(builtIn, expectedRules) {
		t.Fatalf("DocumentRules() drifted from published document field-rules.json:\n got:  %+v\n want: %+v", builtIn, expectedRules)
	}
}

func TestSectionRulesDriftGuard(t *testing.T) {
	allRules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	var expectedRules []s.Rule
	for _, r := range allRules {
		if r.Vocabulary == "section" {
			expectedRules = append(expectedRules, s.Rule{
				OpType:    r.OpType,
				OpVersion: r.OpVersion,
				Field:     r.Field,
				Target:    r.Target,
				Strategy:  r.Strategy,
				Key:       r.Key,
				Lattice:   r.Lattice,
				ValueType: r.ValueType,
				Enum:      r.Enum,
				MaxLength: r.MaxLength,
				KeyTypes:  r.KeyTypes,
			})
		}
	}

	builtIn := s.SectionRules()
	if !reflect.DeepEqual(builtIn, expectedRules) {
		t.Fatalf("SectionRules() drifted from published section field-rules.json:\n got:  %+v\n want: %+v", builtIn, expectedRules)
	}
}

func TestFoldDocument_Basic(t *testing.T) {
	docID := "0123456789abcdef0123456789abcdef"
	now := time.Unix(100, 0).UTC()

	ops := []codec.Op{
		{
			ID: "op-1",
			Envelope: codec.Envelope{
				ObjectID:   docID,
				ObjectType: "document",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Original Title"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		},
		{
			ID:      "op-2",
			Parents: []string{"op-1"},
			Envelope: codec.Envelope{
				ObjectID:   docID,
				ObjectType: "document",
				OpType:     "update",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Updated Title"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Second)},
		},
		{
			ID:      "op-3",
			Parents: []string{"op-2"},
			Envelope: codec.Envelope{
				ObjectID:   docID,
				ObjectType: "document",
				OpType:     "link",
				OpVersion:  1,
				Body:       json.RawMessage(`{"target":"issue-123","target_type":"issue","relation":"implementation-plan"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(2 * time.Second)},
		},
		{
			ID:      "op-4",
			Parents: []string{"op-3"},
			Envelope: codec.Envelope{
				ObjectID:   docID,
				ObjectType: "document",
				OpType:     "label",
				OpVersion:  1,
				Body:       json.RawMessage(`{"add":["rfcs","storage"]}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(3 * time.Second)},
		},
		{
			ID:      "op-5",
			Parents: []string{"op-4"},
			Envelope: codec.Envelope{
				ObjectID:   docID,
				ObjectType: "document",
				OpType:     "label",
				OpVersion:  1,
				Body:       json.RawMessage(`{"remove":["storage"]}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(4 * time.Second)},
		},
	}

	doc, err := s.FoldDocument(ops)
	if err != nil {
		t.Fatalf("FoldDocument: %v", err)
	}

	if doc.Title != "Updated Title" {
		t.Errorf("Title = %q, want %q", doc.Title, "Updated Title")
	}
	if len(doc.Links) != 1 || doc.Links[0].Target != "issue-123" || doc.Links[0].Relation != "implementation-plan" {
		t.Errorf("Links = %+v, want 1 link to issue-123", doc.Links)
	}
	if len(doc.Labels) != 1 || doc.Labels[0] != "rfcs" {
		t.Errorf("Labels = %+v, want ['rfcs']", doc.Labels)
	}
}

func TestFoldSection_MultiValueConcurrentAndCollapse(t *testing.T) {
	secID := "sec0123456789abcdef0123456789abc"
	now := time.Unix(100, 0).UTC()

	// Root op
	opRoot := codec.Op{
		ID: "op-root",
		Envelope: codec.Envelope{
			ObjectID:   secID,
			ObjectType: "section",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"document_id":"doc-1","position":"V","title":"Intro","body":"Initial content"}`),
		},
		Author: codec.Identity{Email: "alice@example.com", When: now},
	}

	// Concurrent edits by Alice and Bob
	opAlice := codec.Op{
		ID:      "op-alice",
		Parents: []string{"op-root"},
		Envelope: codec.Envelope{
			ObjectID:   secID,
			ObjectType: "section",
			OpType:     "edit",
			OpVersion:  1,
			Body:       json.RawMessage(`{"body":"Alice edit"}`),
		},
		Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Second)},
	}

	opBob := codec.Op{
		ID:      "op-bob",
		Parents: []string{"op-root"},
		Envelope: codec.Envelope{
			ObjectID:   secID,
			ObjectType: "section",
			OpType:     "edit",
			OpVersion:  1,
			Body:       json.RawMessage(`{"body":"Bob edit"}`),
		},
		Author: codec.Identity{Email: "bob@example.com", When: now.Add(2 * time.Second)},
	}

	secConflicted, err := s.FoldSection([]codec.Op{opRoot, opAlice, opBob})
	if err != nil {
		t.Fatalf("FoldSection: %v", err)
	}
	if !secConflicted.IsConflicted() {
		t.Errorf("expected conflicted, got settled body: %v", secConflicted.Body)
	}
	bodies := secConflicted.ConflictBodies()
	if len(bodies) != 2 || bodies[0] != "Alice edit" || bodies[1] != "Bob edit" {
		t.Errorf("ConflictBodies = %v, want ['Alice edit', 'Bob edit']", bodies)
	}

	// Causal collapse op
	opResolve := codec.Op{
		ID:      "op-resolve",
		Parents: []string{"op-alice", "op-bob"},
		Envelope: codec.Envelope{
			ObjectID:   secID,
			ObjectType: "section",
			OpType:     "edit",
			OpVersion:  1,
			Body:       json.RawMessage(`{"body":"Resolved content"}`),
		},
		Author: codec.Identity{Email: "alice@example.com", When: now.Add(3 * time.Second)},
	}

	secResolved, err := s.FoldSection([]codec.Op{opRoot, opAlice, opBob, opResolve})
	if err != nil {
		t.Fatalf("FoldSection resolved: %v", err)
	}
	if secResolved.IsConflicted() {
		t.Errorf("expected settled, got conflicted: %v", secResolved.Body)
	}
	if secResolved.SettledBody() != "Resolved content" {
		t.Errorf("SettledBody = %q, want 'Resolved content'", secResolved.SettledBody())
	}
}

func TestFoldSection_MoveUpdateDelete(t *testing.T) {
	secID := "sec0123456789abcdef0123456789abc"
	now := time.Unix(100, 0).UTC()

	ops := []codec.Op{
		{
			ID: "op-1",
			Envelope: codec.Envelope{
				ObjectID:   secID,
				ObjectType: "section",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"document_id":"doc-1","position":"V","title":"Heading","body":"Body"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		},
		{
			ID:      "op-2",
			Parents: []string{"op-1"},
			Envelope: codec.Envelope{
				ObjectID:   secID,
				ObjectType: "section",
				OpType:     "move",
				OpVersion:  1,
				Body:       json.RawMessage(`{"position":"k"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Second)},
		},
		{
			ID:      "op-3",
			Parents: []string{"op-2"},
			Envelope: codec.Envelope{
				ObjectID:   secID,
				ObjectType: "section",
				OpType:     "update",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"New Heading"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(2 * time.Second)},
		},
		{
			ID:      "op-4",
			Parents: []string{"op-3"},
			Envelope: codec.Envelope{
				ObjectID:   secID,
				ObjectType: "section",
				OpType:     "delete",
				OpVersion:  1,
				Body:       json.RawMessage(`{}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(3 * time.Second)},
		},
	}

	sec, err := s.FoldSection(ops)
	if err != nil {
		t.Fatalf("FoldSection: %v", err)
	}
	if sec.Position != "k" {
		t.Errorf("Position = %q, want 'k'", sec.Position)
	}
	if sec.Title != "New Heading" {
		t.Errorf("Title = %q, want 'New Heading'", sec.Title)
	}
	if !sec.Deleted {
		t.Errorf("Deleted = false, want true")
	}
}
