package fold_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/engine/internal/fold"
	"github.com/writtendev/writ/spec"
)

func TestFoldObjectTypeInference(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rules := []fold.Rule{
		{
			OpType:    "create",
			OpVersion: 1,
			Field:     "title",
			Strategy:  "lww",
		},
		{
			OpType:    "update",
			OpVersion: 1,
			Field:     "title",
			Strategy:  "lww",
		},
	}

	tests := []struct {
		name     string
		ops      []codec.Op
		wantType string
	}{
		{
			name: "ops[0] empty ObjectType, subsequent create op has ObjectType",
			ops: []codec.Op{
				{
					ID: "op-update",
					Envelope: codec.Envelope{
						ObjectID:   "obj-1",
						ObjectType: "",
						OpType:     "update",
						OpVersion:  1,
						Body:       []byte(`{"title":"Updated Title"}`),
					},
					Parents: []string{"op-create"},
					Author:  codec.Identity{When: baseTime.Add(time.Minute)},
				},
				{
					ID: "op-create",
					Envelope: codec.Envelope{
						ObjectID:   "obj-1",
						ObjectType: "issue",
						OpType:     "create",
						OpVersion:  1,
						Body:       []byte(`{"title":"Initial Title"}`),
					},
					Author: codec.Identity{When: baseTime},
				},
			},
			wantType: "issue",
		},
		{
			name: "ops[0] empty ObjectType, subsequent update op has ObjectType",
			ops: []codec.Op{
				{
					ID: "op-1",
					Envelope: codec.Envelope{
						ObjectID:   "obj-1",
						ObjectType: "",
						OpType:     "update",
						OpVersion:  1,
						Body:       []byte(`{"title":"Title 1"}`),
					},
					Author: codec.Identity{When: baseTime},
				},
				{
					ID: "op-2",
					Envelope: codec.Envelope{
						ObjectID:   "obj-1",
						ObjectType: "review",
						OpType:     "update",
						OpVersion:  1,
						Body:       []byte(`{"title":"Title 2"}`),
					},
					Parents: []string{"op-1"},
					Author:  codec.Identity{When: baseTime.Add(time.Minute)},
				},
			},
			wantType: "review",
		},
		{
			name: "subsequent create op takes precedence over first non-empty op",
			ops: []codec.Op{
				{
					ID: "op-update",
					Envelope: codec.Envelope{
						ObjectID:   "obj-1",
						ObjectType: "generic",
						OpType:     "update",
						OpVersion:  1,
						Body:       []byte(`{"title":"Title"}`),
					},
					Parents: []string{"op-create"},
					Author:  codec.Identity{When: baseTime.Add(time.Minute)},
				},
				{
					ID: "op-create",
					Envelope: codec.Envelope{
						ObjectID:   "obj-1",
						ObjectType: "issue",
						OpType:     "create",
						OpVersion:  1,
						Body:       []byte(`{"title":"Title"}`),
					},
					Author: codec.Identity{When: baseTime},
				},
			},
			wantType: "issue",
		},
		{
			name: "all ops have empty ObjectType",
			ops: []codec.Op{
				{
					ID: "op-1",
					Envelope: codec.Envelope{
						ObjectID:   "obj-1",
						ObjectType: "",
						OpType:     "update",
						OpVersion:  1,
						Body:       []byte(`{"title":"Title"}`),
					},
					Author: codec.Identity{When: baseTime},
				},
			},
			wantType: "",
		},
		{
			name:     "empty ops slice",
			ops:      nil,
			wantType: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := fold.Fold(tc.ops, rules)
			if err != nil {
				t.Fatalf("Fold returned unexpected error: %v", err)
			}
			if res.ObjectType != tc.wantType {
				t.Errorf("res.ObjectType = %q, want %q", res.ObjectType, tc.wantType)
			}
		})
	}
}

func TestFoldKeyedLWWMultiRuleField(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	rev := "1111111111111111111111111111111111111111"

	rules := []fold.Rule{
		{
			OpType:    "approval",
			OpVersion: 1,
			Field:     "revision",
			Strategy:  "keyed-lww",
			Key:       []string{"subject", "revision"},
			KeyTypes:  map[string]string{"subject": "person-ref", "revision": "git-oid"},
		},
		{
			OpType:    "approval",
			OpVersion: 1,
			Field:     "verdict",
			Strategy:  "keyed-lww",
			Key:       []string{"subject", "revision"},
			KeyTypes:  map[string]string{"subject": "person-ref", "revision": "git-oid"},
		},
		{
			OpType:    "ci-status",
			OpVersion: 1,
			Field:     "revision",
			Strategy:  "keyed-lww",
			Key:       []string{"revision", "name"},
		},
		{
			OpType:    "ci-status",
			OpVersion: 1,
			Field:     "state",
			Strategy:  "keyed-lww",
			Key:       []string{"revision", "name"},
		},
	}

	opApp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-1",
			ObjectType: "review",
			OpType:     "approval",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"approve","subject":"user:alice"}`),
		},
		ID: "op-app",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	opCI := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-1",
			ObjectType: "review",
			OpType:     "ci-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"` + rev + `","name":"lint","state":"success"}`),
		},
		ID: "op-ci",
		Author: codec.Identity{
			Email: "ci@example.com",
			When:  now.Add(time.Minute),
		},
	}

	ops := []codec.Op{opApp, opCI}
	res, err := fold.Fold(ops, rules)
	if err != nil {
		t.Fatalf("Fold failed: %v", err)
	}

	wantRevision := []any{
		map[string]any{
			"key":   []string{rev, "lint"},
			"value": rev,
		},
		map[string]any{
			"key":   []string{"user:alice", rev},
			"value": rev,
		},
	}

	if !reflect.DeepEqual(res.State["revision"], wantRevision) {
		t.Errorf("revision mismatch:\n got:  %v\n want: %v", res.State["revision"], wantRevision)
	}

	// Reference fold cross-check
	var mergeOps []spec.MergeOp
	for _, o := range ops {
		var bm map[string]any
		_ = json.Unmarshal(o.Body, &bm)
		mergeOps = append(mergeOps, spec.MergeOp{
			ID:        o.ID,
			Time:      o.Author.When.Unix(),
			ObjectID:  o.ObjectID,
			OpType:    o.OpType,
			OpVersion: o.OpVersion,
			Author: spec.MergeAuthor{
				Email: o.Author.Email,
			},
			Body: bm,
		})
	}

	var fieldRules []spec.FieldRule
	for _, r := range rules {
		fieldRules = append(fieldRules, spec.FieldRule{
			OpType:    r.OpType,
			OpVersion: r.OpVersion,
			Field:     r.Field,
			Strategy:  r.Strategy,
			Key:       r.Key,
			ValueType: r.ValueType,
			Enum:      r.Enum,
			MaxLength: r.MaxLength,
			KeyTypes:  r.KeyTypes,
		})
	}

	refRes, err := spec.Fold(mergeOps, fieldRules)
	if err != nil {
		t.Fatalf("spec.Fold failed: %v", err)
	}

	if !reflect.DeepEqual(res.State["revision"], refRes.State["revision"]) {
		t.Errorf("engine vs spec fold revision mismatch:\n engine: %v\n spec:   %v", res.State["revision"], refRes.State["revision"])
	}

	engineRaw, err := json.Marshal(res.State)
	if err != nil {
		t.Fatalf("json.Marshal(res.State) failed: %v", err)
	}
	specRaw, err := json.Marshal(refRes.State)
	if err != nil {
		t.Fatalf("json.Marshal(refRes.State) failed: %v", err)
	}

	engineJSON, err := canonicaljson.Marshal(engineRaw)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal(engineRaw) failed: %v", err)
	}
	specJSON, err := canonicaljson.Marshal(specRaw)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal(specRaw) failed: %v", err)
	}
	if string(engineJSON) != string(specJSON) {
		t.Errorf("canonical JSON mismatch:\n engine: %s\n spec:   %s", string(engineJSON), string(specJSON))
	}
}
