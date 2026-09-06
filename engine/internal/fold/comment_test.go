package fold_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
	"github.com/writtendev/writ/spec"
)

func TestCommentRulesDriftGuard(t *testing.T) {
	allRules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules: %v", err)
	}

	var specCommentRules []fold.Rule
	for _, r := range allRules {
		if r.Vocabulary == "comments" {
			specCommentRules = append(specCommentRules, fold.Rule{
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

	if len(specCommentRules) == 0 {
		t.Fatal("no comment rules found in spec.FieldRules")
	}

	if !reflect.DeepEqual(fold.CommentRules, specCommentRules) {
		t.Fatalf("fold.CommentRules drifted from spec field rules:\n got:  %+v\n want: %+v", fold.CommentRules, specCommentRules)
	}
}

func TestFoldCommentsCycle(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ops := []codec.Op{
		{
			ID: "c1-create",
			Envelope: codec.Envelope{
				ObjectID:   "c-1",
				ObjectType: "comment",
				OpType:     "create",
				OpVersion:  1,
				Body:       []byte(`{"subject":{"object_type":"review","object_id":"r-1"},"in_reply_to":"c-2","text":"Comment 1"}`),
			},
			Author: codec.Identity{When: baseTime},
		},
		{
			ID: "c2-create",
			Envelope: codec.Envelope{
				ObjectID:   "c-2",
				ObjectType: "comment",
				OpType:     "create",
				OpVersion:  1,
				Body:       []byte(`{"subject":{"object_type":"review","object_id":"r-1"},"in_reply_to":"c-1","text":"Comment 2"}`),
			},
			Author: codec.Identity{When: baseTime.Add(time.Minute)},
		},
	}

	nodes, err := fold.FoldComments(ops)
	if err != nil {
		t.Fatalf("FoldComments failed: %v", err)
	}

	if len(nodes) != 2 {
		t.Fatalf("expected 2 root nodes for cycle, got %d", len(nodes))
	}

	nodeMap := make(map[string]fold.CommentNode)
	for _, n := range nodes {
		nodeMap[n.ObjectID] = n
	}

	c1, ok := nodeMap["c-1"]
	if !ok {
		t.Fatal("missing c-1 in roots")
	}
	if len(c1.Replies) != 0 {
		t.Errorf("expected 0 replies for c-1, got %d", len(c1.Replies))
	}
	if c1.Comment.InReplyTo != "c-2" {
		t.Errorf("expected InReplyTo 'c-2', got %q", c1.Comment.InReplyTo)
	}

	c2, ok := nodeMap["c-2"]
	if !ok {
		t.Fatal("missing c-2 in roots")
	}
	if len(c2.Replies) != 0 {
		t.Errorf("expected 0 replies for c-2, got %d", len(c2.Replies))
	}
	if c2.Comment.InReplyTo != "c-1" {
		t.Errorf("expected InReplyTo 'c-1', got %q", c2.Comment.InReplyTo)
	}
}

func TestFoldCommentEditOnly(t *testing.T) {
	ops := []codec.Op{
		{
			ID: "edit-1",
			Envelope: codec.Envelope{
				ObjectID:   "c-edit-only",
				ObjectType: "comment",
				OpType:     "edit",
				OpVersion:  1,
				Body:       []byte(`{"text":"Truncated edit text"}`),
			},
			Author: codec.Identity{When: time.Now().UTC()},
		},
	}

	cf, err := fold.FoldComment(ops)
	if err != nil {
		t.Fatalf("FoldComment failed: %v", err)
	}

	if cf.Text != "Truncated edit text" {
		t.Errorf("expected text 'Truncated edit text', got %q", cf.Text)
	}
	if cf.SubjectRaw != nil {
		t.Errorf("expected nil SubjectRaw, got %s", string(cf.SubjectRaw))
	}
	if cf.AnchorRaw != nil {
		t.Errorf("expected nil AnchorRaw, got %s", string(cf.AnchorRaw))
	}
	if cf.InReplyTo != "" {
		t.Errorf("expected empty InReplyTo, got %q", cf.InReplyTo)
	}
	if cf.Deleted {
		t.Errorf("expected Deleted false, got true")
	}
}

func TestFoldCommentUnknownOp(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ops := []codec.Op{
		{
			ID: "create-1",
			Envelope: codec.Envelope{
				ObjectID:   "c-unk",
				ObjectType: "comment",
				OpType:     "create",
				OpVersion:  1,
				Body:       []byte(`{"subject":{"object_type":"review","object_id":"r-1"},"text":"Initial text"}`),
			},
			Author: codec.Identity{When: baseTime},
		},
		{
			ID:      "future-op-1",
			Parents: []string{"create-1"},
			Envelope: codec.Envelope{
				ObjectID:   "c-unk",
				ObjectType: "comment",
				OpType:     "react",
				OpVersion:  2,
				Body:       []byte(`{"reaction":"thumbs_up"}`),
			},
			Author: codec.Identity{When: baseTime.Add(time.Minute)},
		},
		{
			ID:      "edit-1",
			Parents: []string{"future-op-1"},
			Envelope: codec.Envelope{
				ObjectID:   "c-unk",
				ObjectType: "comment",
				OpType:     "edit",
				OpVersion:  1,
				Body:       []byte(`{"text":"Edited text"}`),
			},
			Author: codec.Identity{When: baseTime.Add(2 * time.Minute)},
		},
	}

	cf, err := fold.FoldComment(ops)
	if err != nil {
		t.Fatalf("FoldComment: %v", err)
	}

	if len(cf.TotalOrder) != 3 {
		t.Fatalf("expected 3 ops in TotalOrder, got %d", len(cf.TotalOrder))
	}
	if len(cf.UnknownOps) != 1 {
		t.Fatalf("expected 1 UnknownOp, got %d", len(cf.UnknownOps))
	}
	if cf.UnknownOps[0].Commit != "future-op-1" || cf.UnknownOps[0].ObjectType != "comment" || cf.UnknownOps[0].OpType != "react" || cf.UnknownOps[0].OpVersion != 2 {
		t.Errorf("unexpected UnknownOp entry: %+v", cf.UnknownOps[0])
	}
	if cf.Text != "Edited text" {
		t.Errorf("expected text 'Edited text', got %q", cf.Text)
	}
}

func TestFoldCommentUnknownSubjectFields(t *testing.T) {
	subjectJSON := `{"custom_field":"future_val","nested":{"k":123},"object_id":"r-1","object_type":"review"}`
	op := codec.Op{
		ID: "create-sub",
		Envelope: codec.Envelope{
			ObjectID:   "c-sub",
			ObjectType: "comment",
			OpType:     "create",
			OpVersion:  1,
			Body:       []byte(`{"subject":` + subjectJSON + `,"text":"Hello"}`),
		},
		Author: codec.Identity{When: time.Now().UTC()},
	}

	cf, err := fold.FoldComment([]codec.Op{op})
	if err != nil {
		t.Fatalf("FoldComment: %v", err)
	}

	if string(cf.SubjectRaw) != subjectJSON {
		t.Fatalf("SubjectRaw mismatch:\n got:  %s\n want: %s", string(cf.SubjectRaw), subjectJSON)
	}

	var parsed map[string]any
	if err := json.Unmarshal(cf.SubjectRaw, &parsed); err != nil {
		t.Fatalf("Unmarshal SubjectRaw: %v", err)
	}
	if parsed["custom_field"] != "future_val" {
		t.Errorf("custom_field lost: %+v", parsed)
	}
}

func TestFoldCommentGenericEmptyScalars(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ops := []codec.Op{
		{
			ID: "c-create",
			Envelope: codec.Envelope{
				ObjectID:   "c-scalar",
				ObjectType: "comment",
				OpType:     "create",
				OpVersion:  1,
				Body:       []byte(`{"subject":{"object_type":"review","object_id":"r-1"},"text":"Initial text"}`),
			},
			Author: codec.Identity{When: baseTime},
		},
		{
			ID:      "c-edit",
			Parents: []string{"c-create"},
			Envelope: codec.Envelope{
				ObjectID:   "c-scalar",
				ObjectType: "comment",
				OpType:     "edit",
				OpVersion:  1,
				Body:       []byte(`{"text":""}`),
			},
			Author: codec.Identity{When: baseTime.Add(time.Minute)},
		},
		{
			ID:      "c-resolve",
			Parents: []string{"c-edit"},
			Envelope: codec.Envelope{
				ObjectID:   "c-scalar",
				ObjectType: "comment",
				OpType:     "resolve",
				OpVersion:  1,
				Body:       []byte(`{"resolved":true,"resolved_by":"   "}`),
			},
			Author: codec.Identity{When: baseTime.Add(2 * time.Minute)},
		},
	}

	res, err := fold.Fold(ops, fold.CommentRules)
	if err != nil {
		t.Fatalf("fold.Fold failed: %v", err)
	}

	// Generic fold state MUST retain "" for empty scalars
	if val, ok := res.State["text"]; !ok {
		t.Errorf("expected 'text' present in fold state")
	} else if val != "" {
		t.Errorf("expected 'text' to be %q, got %q", "", val)
	}

	if val, ok := res.State["resolved_by"]; !ok {
		t.Errorf("expected 'resolved_by' present in fold state")
	} else if val != "" {
		t.Errorf("expected 'resolved_by' to be %q, got %q", "", val)
	}

	// In addition, FoldComment should extract these empty scalars
	cf, err := fold.FoldComment(ops)
	if err != nil {
		t.Fatalf("fold.FoldComment failed: %v", err)
	}
	if cf.Text != "" {
		t.Errorf("expected cf.Text to be empty string, got %q", cf.Text)
	}
	if cf.ResolvedBy != "" {
		t.Errorf("expected cf.ResolvedBy to be empty string, got %q", cf.ResolvedBy)
	}
}

func TestFoldCommentResolveWithoutResolvedBy(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ops := []codec.Op{
		{
			ID: "c-create",
			Envelope: codec.Envelope{
				ObjectID:   "c-no-resolved-by",
				ObjectType: "comment",
				OpType:     "create",
				OpVersion:  1,
				Body:       []byte(`{"subject":{"object_type":"review","object_id":"r-1"},"text":"Initial text"}`),
			},
			Author: codec.Identity{When: baseTime},
		},
		{
			ID:      "c-resolve-no-resolved-by",
			Parents: []string{"c-create"},
			Envelope: codec.Envelope{
				ObjectID:   "c-no-resolved-by",
				ObjectType: "comment",
				OpType:     "resolve",
				OpVersion:  1,
				Body:       []byte(`{"resolved":true}`),
			},
			Author: codec.Identity{When: baseTime.Add(time.Minute)},
		},
	}

	cf, err := fold.FoldComment(ops)
	if err != nil {
		t.Fatalf("FoldComment failed: %v", err)
	}

	if cf.Resolved == nil || *cf.Resolved != true {
		t.Fatalf("expected Resolved to be &true, got %+v", cf.Resolved)
	}
	if cf.ResolvedBy != "" {
		t.Errorf("expected ResolvedBy to be empty string when resolved_by is absent, got %q", cf.ResolvedBy)
	}
}
